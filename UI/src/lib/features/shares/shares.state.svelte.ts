import { browser } from '$app/environment';
import { listShares } from '$lib/api/shares';
import type { ScreenShare } from '$lib/types/share';

/**
 * Workspace-wide screen share state. Driven by REST loads plus the
 * share.started / share.ended WebSocket events the workspace layout
 * dispatches; a transition always triggers a reload rather than local
 * mutation so viewer counts and GC'd shares stay truthful.
 */
class SharesState {
	active = $state<ScreenShare[]>([]);
	recent = $state<ScreenShare[]>([]);
	enabled = $state(false);
	loaded = $state(false);

	private slug = '';
	private loadSeq = 0;

	constructor() {
		if (browser) {
			window.addEventListener('ws:share.started', ((e: CustomEvent<{ id?: string; started_by?: string }>) => {
				this.handleStarted(e.detail);
			}) as EventListener);
			window.addEventListener('ws:share.ended', ((e: CustomEvent<{ id?: string }>) => {
				this.handleEnded(e.detail);
			}) as EventListener);
		}
	}

	setEnabled(enabled: boolean) {
		this.enabled = enabled;
		if (!enabled) {
			this.active = [];
			this.recent = [];
			this.loaded = false;
			this.slug = '';
		}
	}

	async load(slug: string) {
		if (!slug) return;
		this.slug = slug;
		const seq = ++this.loadSeq;
		try {
			const list = await listShares(slug);
			if (seq !== this.loadSeq || slug !== this.slug) return;
			this.active = list.active;
			this.recent = list.recent;
			this.loaded = true;
		} catch {
			// Keep whatever we had; the next WS event or navigation retries.
		}
	}

	async reload() {
		if (this.slug) await this.load(this.slug);
	}

	handleStarted(payload: { id?: string; started_by?: string }) {
		if (!this.slug) return;
		void this.reload();
	}

	handleEnded(payload: { id?: string }) {
		if (!payload?.id) return;
		// Optimistically drop it so open viewer pages unmount immediately;
		// the reload reconciles counts and history.
		this.active = this.active.filter((s) => s.id !== payload.id);
		void this.reload();
	}

	findActive(id: string): ScreenShare | undefined {
		return this.active.find((s) => s.id === id);
	}
}

export const sharesState = new SharesState();
