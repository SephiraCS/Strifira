import { browser } from '$app/environment';
import { page } from '$app/state';
import { listDocuments } from '$lib/api/docs';
import type { DocTreeNode } from '$lib/types/docs';
import { buildTree, breadcrumbOf, type DocTreeEntry } from './tree';

export { buildTree, breadcrumbOf };
export type { DocTreeEntry };

const RECENTS_KEY = 'ku:docs:recents';
const RECENTS_LIMIT = 12;

function loadRecents(): string[] {
	if (!browser) return [];
	try {
		const raw = localStorage.getItem(RECENTS_KEY);
		const parsed = raw ? (JSON.parse(raw) as unknown) : [];
		return Array.isArray(parsed) ? parsed.filter((id): id is string => typeof id === 'string') : [];
	} catch {
		return [];
	}
}

/**
 * Workspace-wide documents state: the flat page list (tree assembled by the
 * UI), recently opened ids, and debounced refresh on doc.* WebSocket events.
 */
class DocsState {
	docs = $state<DocTreeNode[]>([]);
	loading = $state(false);
	loadedSlug = $state('');
	error = $state(false);
	recents = $state<string[]>(loadRecents());

	private requestId = 0;
	private refreshTimer: ReturnType<typeof setTimeout> | null = null;

	readonly tree = $derived.by(() => buildTree(this.docs.filter((d) => !d.is_template)));

	constructor() {
		if (!browser) return;
		window.addEventListener('ws:doc-event', () => this.scheduleRefresh());
		window.addEventListener('app:refresh', ((e: Event) => {
			const detail = (e as CustomEvent<{ slug?: string }>).detail;
			if (detail?.slug && detail.slug !== page.params.workspaceSlug) return;
			this.scheduleRefresh();
		}) as EventListener);
	}

	async load(slug: string, force = false) {
		if (!slug) return;
		if (this.loading && this.loadedSlug === slug && !force) return;
		const request = ++this.requestId;
		this.loading = true;
		this.error = false;
		try {
			const docs = await listDocuments(slug);
			if (request !== this.requestId) return;
			if (this.loadedSlug !== slug) this.recents = loadRecents();
			this.docs = docs;
			this.loadedSlug = slug;
		} catch {
			if (request === this.requestId) this.error = true;
		} finally {
			if (request === this.requestId) this.loading = false;
		}
	}

	scheduleRefresh() {
		const slug = page.params.workspaceSlug;
		if (!slug || this.loadedSlug !== slug) return;
		if (this.refreshTimer) clearTimeout(this.refreshTimer);
		this.refreshTimer = setTimeout(() => {
			this.refreshTimer = null;
			void this.load(slug, true);
		}, 300);
	}

	byId(id: string): DocTreeNode | undefined {
		return this.docs.find((d) => d.id === id);
	}

	titleById(id: string): string | undefined {
		return this.byId(id)?.title;
	}

	markOpened(slug: string, id: string) {
		this.recents = [id, ...this.recents.filter((r) => r !== id)].slice(0, RECENTS_LIMIT);
		if (!browser) return;
		try {
			localStorage.setItem(RECENTS_KEY, JSON.stringify(this.recents));
		} catch {
			// Private browsing or quota: recents are best-effort.
		}
	}

	/** Path of ancestor titles, oldest first, for breadcrumbs. */
	breadcrumb(id: string): DocTreeNode[] {
		return breadcrumbOf(this.docs, id);
	}
}

export const docsState = new DocsState();
