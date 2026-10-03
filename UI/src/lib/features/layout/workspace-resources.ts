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
 * A finished result is only reused if the workspace's websocket was open for
 * the whole time since the request started, so any change to it would have
 * emitted a refresh. The backend does not replay events, so a result fetched
 * before the socket first opened, or while it was down, may already be stale:
 * it is never reused once finished (requests still in flight are shared), and
 * when the socket opens the layout reloads every such collection (see
 * `takeUnsubscribedWorkspaceResources`). A drop clears every entry. Results
 * older than MAX_AGE_MS are not reused either, so a missed event cannot leave
 * pages stale forever.
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
	/** Started while the websocket was not open, so changes may have been missed. */
	startedOffline: boolean;
};

let currentSlug: string | null = null;
// Workspaces whose websocket is open. The socket may open before the layout
// calls resetWorkspaceResources, so this is kept per workspace. Until its
// socket first opens a workspace counts as offline.
const connectedWorkspaces = new Set<string>();
const isOffline = () => currentSlug === null || !connectedWorkspaces.has(currentSlug);
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
		connectedWorkspaces.delete(workspaceSlug);
		if (workspaceSlug === currentSlug) requests.clear();
	});
	window.addEventListener('ws:reconnected', (event) => {
		const workspaceSlug = eventWorkspace(event);
		if (workspaceSlug) connectedWorkspaces.add(workspaceSlug);
	});
}

/** Forget every shared request; called when the layout switches workspace. */
export function resetWorkspaceResources(slug: string) {
	currentSlug = slug;
	requests.clear();
}

/** The layout closed `slug`'s socket without a disconnect event (it switched workspace). */
export function workspaceSocketClosed(slug: string) {
	connectedWorkspaces.delete(slug);
}

/**
 * Called when `slug`'s socket opens: drops the shared requests that started
 * before it was open and returns their names. Changes made between such a
 * request's read and the subscription emitted no event, so the layout reloads
 * these collections. Collections it has not requested yet need nothing.
 */
export function takeUnsubscribedWorkspaceResources(slug: string): ResourceName[] {
	if (slug !== currentSlug) return [];
	const stale = [...requests].filter(([, entry]) => entry.startedOffline).map(([resource]) => resource);
	for (const resource of stale) requests.delete(resource);
	return stale;
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
	if (!entry || !shared) return fetchers[resource](slug);
	// A refresh, reconnect or workspace switch can invalidate the entry while
	// it is in flight. Its response then predates that change, so the reader
	// goes through the replacement request (or fetches) instead.
	const isCurrent = () => slug === currentSlug && requests.get(resource) === entry;
	return shared.then(
		// Hand out a copy so a page reassigning or pushing to its list never
		// touches the array other readers received.
		(data) => (isCurrent() ? ([...data] as Resources[K]) : getWorkspaceResource(slug, resource)),
		// A failed request has already removed its own entry (its handler was
		// attached first); use a replacement if there is one.
		() => (isCurrent() ? fetchers[resource](slug) : getWorkspaceResource(slug, resource))
	);
}

export const workspaceTeams = (slug: string) => getWorkspaceResource(slug, 'teams');
export const workspaceProjects = (slug: string) => getWorkspaceResource(slug, 'projects');
export const workspaceLabels = (slug: string) => getWorkspaceResource(slug, 'labels');
export const workspaceMembers = (slug: string) => getWorkspaceResource(slug, 'members');
