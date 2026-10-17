import { browser } from '$app/environment';
import { nextFocus, reconcileOrder } from './wall-layout';

export type WallLayout = 'grid' | 'spotlight';

const LAYOUT_KEY = 'shares_wall_layout';

/**
 * Client-side share wall presentation state: which layout the wall uses,
 * which share holds the spotlight, and the user's tile order. The layout
 * mode persists in localStorage; focus and order live per session, since
 * share ids are ephemeral. Like presenterSession this is a module singleton,
 * so the wall keeps its arrangement across in-app navigation.
 */
class WallState {
	layout = $state<WallLayout>(
		browser && localStorage.getItem(LAYOUT_KEY) === 'spotlight' ? 'spotlight' : 'grid'
	);
	focusedId = $state<string | null>(null);
	order = $state<string[]>([]);

	setLayout(layout: WallLayout) {
		this.layout = layout;
		if (layout === 'grid') this.focusedId = null;
		if (browser) localStorage.setItem(LAYOUT_KEY, layout);
	}

	/** Click-to-focus: focusing a share always takes the spotlight. */
	focusShare(id: string) {
		this.focusedId = id;
		if (this.layout !== 'spotlight') this.setLayout('spotlight');
	}

	/** Escape: drop the spotlight and return to the grid. */
	clearFocus() {
		this.setLayout('grid');
	}

	setOrder(order: string[]) {
		this.order = order;
	}

	/**
	 * Keeps the saved order aligned with the live shares and the focus
	 * valid; the wall calls this whenever its tile list changes. Assignments
	 * are guarded: this runs from an effect, and writing a fresh array on
	 * every pass would feed the effect back into itself.
	 */
	syncShares(ids: string[]) {
		const next = reconcileOrder(this.order, ids);
		const changed =
			next.length !== this.order.length || next.some((id, index) => id !== this.order[index]);
		if (changed) this.order = next;
		const fallbackFocus = ids[0] ?? null;
		if (this.focusedId && !ids.includes(this.focusedId) && this.focusedId !== fallbackFocus) {
			this.focusedId = fallbackFocus;
		}
	}

	cycleFocus(ids: string[], delta: number) {
		const next = nextFocus(ids, this.focusedId, delta);
		if (next) this.focusedId = next;
	}
}

export const wallState = new WallState();
