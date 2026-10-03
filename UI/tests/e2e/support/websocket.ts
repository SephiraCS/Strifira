import type { Page } from '@playwright/test';

type SocketControl = {
	__wsBlocked: boolean;
	__wsHoldOpen: boolean;
	__wsHeldOpens: Array<() => void>;
	__wsDisconnects: number;
};

/**
 * Wraps the page's WebSocket so a test controls the workspace socket:
 * - `blocked`: connection attempts go to an unreachable address and fail
 *   without ever opening (an outage).
 * - `holdOpen`: the socket connects, but the app's `onopen` handler only runs
 *   once `releaseWebSocketOpen` is called (a slow first subscription).
 * Combine with `page.routeWebSocket` to answer the unblocked connections.
 */
export async function installControllableWebSocket(
	page: Page,
	{ blocked = false, holdOpen = false }: { blocked?: boolean; holdOpen?: boolean } = {}
) {
	await page.addInitScript(
		({ initiallyBlocked, initiallyHeld }) => {
			const w = window as unknown as SocketControl;
			w.__wsBlocked = initiallyBlocked;
			w.__wsHoldOpen = initiallyHeld;
			w.__wsHeldOpens = [];
			w.__wsDisconnects = 0;
			window.addEventListener('ws:disconnected', () => (w.__wsDisconnects += 1));
			const NativeWebSocket = window.WebSocket;
			// Plain ES only: this function is serialized into the page, so no
			// class fields or private members (their transpiled helpers would
			// not exist there).
			window.WebSocket = class extends NativeWebSocket {
				constructor(url: string | URL, protocols?: string | string[]) {
					super(w.__wsBlocked ? 'ws://127.0.0.1:9/' : url, protocols);
					let appOnOpen: ((this: WebSocket, event: Event) => unknown) | null = null;
					Object.defineProperty(this, 'onopen', {
						get: () => appOnOpen,
						set: (handler) => (appOnOpen = handler)
					});
					this.addEventListener('open', (event) => {
						const deliver = () => appOnOpen?.call(this, event);
						if (w.__wsHoldOpen) w.__wsHeldOpens.push(deliver);
						else deliver();
					});
				}
			};
		},
		{ initiallyBlocked: blocked, initiallyHeld: holdOpen }
	);
}

/** Number of `ws:disconnected` events the layout has dispatched. */
export const webSocketDisconnects = (page: Page) =>
	page.evaluate(() => (window as unknown as SocketControl).__wsDisconnects);

/** Fails every later connection attempt until `restoreWebSocket`. */
export async function blockWebSocket(page: Page) {
	await page.evaluate(() => ((window as unknown as SocketControl).__wsBlocked = true));
}

/** Lets the next attempt through and reconnects now instead of after backoff. */
export async function restoreWebSocket(page: Page) {
	await page.evaluate(() => {
		(window as unknown as SocketControl).__wsBlocked = false;
		window.dispatchEvent(new Event('online'));
	});
}

/** Runs the held `onopen` handlers and stops holding later ones. */
export async function releaseWebSocketOpen(page: Page) {
	await page.evaluate(() => {
		const w = window as unknown as SocketControl;
		w.__wsHoldOpen = false;
		for (const deliver of w.__wsHeldOpens.splice(0)) deliver();
	});
}
