import type { Page } from '@playwright/test';

type SocketControl = {
	__wsBlocked: boolean;
	__wsDisconnects: number;
};

/**
 * Wraps the page's WebSocket so a test can simulate an outage: while blocked,
 * connection attempts go to an unreachable address and fail without ever
 * opening. Combine with `page.routeWebSocket` to answer unblocked connections.
 */
export async function installControllableWebSocket(
	page: Page,
	{ blocked = false }: { blocked?: boolean } = {}
) {
	await page.addInitScript(
		({ initiallyBlocked }) => {
			const w = window as unknown as SocketControl;
			w.__wsBlocked = initiallyBlocked;
			w.__wsDisconnects = 0;
			window.addEventListener('ws:disconnected', () => (w.__wsDisconnects += 1));
			const NativeWebSocket = window.WebSocket;
			window.WebSocket = class extends NativeWebSocket {
				constructor(url: string | URL, protocols?: string | string[]) {
					super(w.__wsBlocked ? 'ws://127.0.0.1:9/' : url, protocols);
				}
			};
		},
		{ initiallyBlocked: blocked }
	);
}

/** Number of `ws:disconnected` events the layout has dispatched. */
export const webSocketDisconnects = (page: Page) =>
	page.evaluate(() => (window as unknown as SocketControl).__wsDisconnects);

/** Lets the next attempt through and reconnects now instead of after backoff. */
export async function restoreWebSocket(page: Page) {
	await page.evaluate(() => {
		(window as unknown as SocketControl).__wsBlocked = false;
		window.dispatchEvent(new Event('online'));
	});
}
