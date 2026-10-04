import { listLabels } from '$lib/api/labels';
import { listMembers } from '$lib/api/members';
import { listProjects } from '$lib/api/projects';
import { listTeams } from '$lib/api/teams';
import type { Label } from '$lib/types/label';
import type { Project } from '$lib/types/project';
import type { Team } from '$lib/types/team';
import type { WorkspaceMember } from '$lib/types/workspace';

/**
 * The workspace layout loads teams, projects, labels and members for the
 * sidebar, command palette and dialogs, and reloads each one whenever an API
 * mutation or realtime event emits `app:refresh` for it. A page that needs one
 * of these collections while a request for it (the layout's or another
 * page's) is still in flight waits for that request instead of sending its
 * own; a refresh invalidates every pending request, and readers of an
 * invalidated one get the replacement's result. Finished results are
 * never reused: once the request settles its entry is removed, and the next
 * reader fetches fresh, because not every change to these collections emits
 * an invalidation event.
 */
type Resources = {
	teams: Team[];
	projects: Project[];
	labels: Label[];
	members: WorkspaceMember[];
};

type ResourceName = keyof Resources;

const fetchers: { [K in ResourceName]: (slug: string) => Promise<Resources[K]> } = {
	teams: listTeams,
	projects: listProjects,
	labels: listLabels,
	members: listMembers
};

let currentSlug: string | null = null;
const requests = new Map<ResourceName, Promise<unknown>>();
// Requests dropped by a refresh or replaced before they settled: their
// responses may predate the change, so readers must not receive them.
const invalidated = new WeakSet<Promise<unknown>>();

function invalidate(resource: ResourceName) {
	const request = requests.get(resource);
	if (request) invalidated.add(request);
	requests.delete(resource);
}

// A refresh (an API mutation or a realtime event) means a pending request may
// return pre-change data. This listener is registered when the module is first
// imported, so it runs before the layout's `app:refresh` handler starts the
// replacement request.
if (typeof window !== 'undefined') {
	window.addEventListener('app:refresh', (event) => {
		const detail = (event as CustomEvent<{ slug?: string; resources?: string[] } | null>).detail;
		if (detail?.slug && detail.slug !== currentSlug) return;
		const resources = detail?.resources;
		if (!resources || resources.length === 0) {
			for (const resource of [...requests.keys()]) invalidate(resource);
			return;
		}
		for (const resource of Object.keys(fetchers) as ResourceName[]) {
			// The layout reloads teams together with the workspace itself.
			if (resources.includes(resource) || (resource === 'teams' && resources.includes('workspace'))) {
				invalidate(resource);
			}
		}
	});
}

/** Forget every pending request; called when the layout switches workspace. */
export function resetWorkspaceResources(slug: string) {
	currentSlug = slug;
	for (const resource of [...requests.keys()]) invalidate(resource);
}

// Make `request` the current entry for `resource`, superseding (and so
// invalidating) any earlier pending one. It is forgotten once it settles.
function track(resource: ResourceName, request: Promise<unknown>) {
	invalidate(resource);
	requests.set(resource, request);
	const forget = () => {
		if (requests.get(resource) === request) requests.delete(resource);
	};
	request.then(forget, forget);
}

/** Record the layout's request for `resource` so pages can share it while it is pending. */
export function shareWorkspaceResource<K extends ResourceName>(
	slug: string,
	resource: K,
	request: Promise<Resources[K]>
): Promise<Resources[K]> {
	if (slug === currentSlug) track(resource, request);
	return request;
}

/**
 * The workspace's `resource`: the pending request for it when there is one
 * (the layout's or another page's), otherwise a new fetch that is tracked the
 * same way so concurrent readers share it and refreshes invalidate it.
 */
export function getWorkspaceResource<K extends ResourceName>(
	slug: string,
	resource: K,
	retryFailure = true
): Promise<Resources[K]> {
	if (slug !== currentSlug) return fetchers[resource](slug);
	let request = requests.get(resource) as Promise<Resources[K]> | undefined;
	if (!request) {
		request = fetchers[resource](slug);
		track(resource, request);
	}
	const pending = request;
	// A refresh, a newer request or a workspace switch can invalidate the
	// request while it is in flight. Its response then predates that change,
	// so the reader goes through the replacement if one is pending, or fetches
	// fresh. `forget` was attached first, so a settled request is out of the
	// map by the time these handlers run.
	const stale = () => slug !== currentSlug || invalidated.has(pending);
	return pending.then(
		// Hand out a copy so a page reassigning or pushing to its list never
		// touches the array other readers received.
		(data) => (stale() ? getWorkspaceResource(slug, resource, retryFailure) : ([...data] as Resources[K])),
		(error) => {
			if (stale()) return getWorkspaceResource(slug, resource, retryFailure);
			// A failed request (e.g. the layout's) gets one fresh attempt,
			// which is tracked like any other; a second failure is the
			// caller's to handle.
			if (retryFailure) return getWorkspaceResource(slug, resource, false);
			throw error;
		}
	);
}

export const workspaceTeams = (slug: string) => getWorkspaceResource(slug, 'teams');
export const workspaceProjects = (slug: string) => getWorkspaceResource(slug, 'projects');
export const workspaceLabels = (slug: string) => getWorkspaceResource(slug, 'labels');
export const workspaceMembers = (slug: string) => getWorkspaceResource(slug, 'members');
