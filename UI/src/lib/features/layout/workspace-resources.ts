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
 * mutation or realtime event emits `app:refresh` for it. Pages that need the
 * same collections share the layout's latest request instead of sending their
 * own: while it is in flight they wait for it, afterwards they reuse its
 * result. When the layout has no request for the workspace (or it failed),
 * the page fetches directly, so callers always get data.
 *
 * A finished result is only reused while the realtime connection could have
 * told us it changed. Once the workspace's websocket fails or drops, events
 * are lost: every entry is dropped, and until the socket is back, finished
 * results are never reused (requests still in flight are shared). Results
 * fetched during the outage stay unreusable after recovery; the layout reloads
 * everything when the socket reconnects. Results older than MAX_AGE_MS are not
 * reused either, so a missed event cannot leave pages stale forever.
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

const MAX_AGE_MS = 60_000;

type SharedRequest = {
	request: Promise<unknown>;
	/** When the request resolved; null while it is in flight. */
	settledAt: number | null;
	/** Started while the websocket was down, so changes may have been missed. */
	startedOffline: boolean;
};

let currentSlug: string | null = null;
// Workspaces whose websocket has failed or dropped and not yet reopened. The
// socket may report before the layout calls resetWorkspaceResources, so this
// is kept per workspace. Before its first attempt settles a workspace counts
// as online.
const offlineWorkspaces = new Set<string>();
const isOffline = () => currentSlug !== null && offlineWorkspaces.has(currentSlug);
const requests = new Map<ResourceName, SharedRequest>();

// A refresh (an API mutation or a realtime event) makes the shared result
// stale. This listener is registered when the module is first imported, so it
// runs before any component's `app:refresh` handler: a handler that reads a
// resource before the layout has reloaded it fetches its own fresh copy.
if (typeof window !== 'undefined') {
	window.addEventListener('app:refresh', (event) => {
		const detail = (event as CustomEvent<{ slug?: string; resources?: string[] } | null>).detail;
		if (detail?.slug && detail.slug !== currentSlug) return;
		const resources = detail?.resources;
		if (!resources || resources.length === 0) {
			requests.clear();
			return;
		}
		for (const resource of Object.keys(fetchers) as ResourceName[]) {
			// The layout reloads teams together with the workspace itself.
			if (resources.includes(resource) || (resource === 'teams' && resources.includes('workspace'))) {
				requests.delete(resource);
			}
		}
	});
	const eventWorkspace = (event: Event) =>
		(event as CustomEvent<{ workspaceSlug?: string } | null>).detail?.workspaceSlug ?? currentSlug;
	window.addEventListener('ws:disconnected', (event) => {
		const workspaceSlug = eventWorkspace(event);
		if (!workspaceSlug) return;
		offlineWorkspaces.add(workspaceSlug);
		if (workspaceSlug === currentSlug) requests.clear();
	});
	window.addEventListener('ws:reconnected', (event) => {
		const workspaceSlug = eventWorkspace(event);
		if (workspaceSlug) offlineWorkspaces.delete(workspaceSlug);
	});
}

/** Forget every shared request; called when the layout switches workspace. */
export function resetWorkspaceResources(slug: string) {
	currentSlug = slug;
	requests.clear();
}

/** Record the layout's request for `resource` so pages can share it. */
export function shareWorkspaceResource<K extends ResourceName>(
	slug: string,
	resource: K,
	request: Promise<Resources[K]>
): Promise<Resources[K]> {
	if (slug === currentSlug) {
		const entry: SharedRequest = { request, settledAt: null, startedOffline: isOffline() };
		requests.set(resource, entry);
		request.then(
			() => {
				entry.settledAt = Date.now();
			},
			() => {
				// A failed request is not reused; the next reader fetches again.
				if (requests.get(resource) === entry) requests.delete(resource);
			}
		);
	}
	return request;
}

/** The workspace's `resource`, from the layout's latest request when there is one. */
export function getWorkspaceResource<K extends ResourceName>(slug: string, resource: K): Promise<Resources[K]> {
	const entry = slug === currentSlug ? requests.get(resource) : undefined;
	if (entry && entry.settledAt !== null) {
		if (Date.now() - entry.settledAt > MAX_AGE_MS) {
			requests.delete(resource);
			return fetchers[resource](slug);
		}
		// Changes may have been missed while it was fetched or since.
		if (isOffline() || entry.startedOffline) return fetchers[resource](slug);
	}
	const shared = entry?.request as Promise<Resources[K]> | undefined;
	if (!shared) return fetchers[resource](slug);
	// Hand out a copy so a page reassigning or pushing to its list never
	// touches the array other readers received.
	return shared.then((data) => [...data] as Resources[K]).catch(() => fetchers[resource](slug));
}

export const workspaceTeams = (slug: string) => getWorkspaceResource(slug, 'teams');
export const workspaceProjects = (slug: string) => getWorkspaceResource(slug, 'projects');
export const workspaceLabels = (slug: string) => getWorkspaceResource(slug, 'labels');
export const workspaceMembers = (slug: string) => getWorkspaceResource(slug, 'members');
