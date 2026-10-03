import { test, expect, type Page } from '@playwright/test';

const webhook = {
	id: 'webhook-1',
	url: 'https://hooks.example.com/ku',
	events: ['issue.created'],
	is_active: true,
	created_at: '2026-01-01T00:00:00Z',
	updated_at: '2026-01-01T00:00:00Z'
};

const template = {
	id: 'template-1',
	workspace_id: 'workspace-1',
	team_id: null,
	title: 'Bug report',
	description: null,
	status: null,
	priority: 2,
	label_ids: [],
	assignee_id: null,
	next_run_at: null,
	is_active: true,
	created_by: 'user-1',
	created_at: '2026-01-01T00:00:00Z',
	updated_at: '2026-01-01T00:00:00Z'
};

async function mockSettings(page: Page, gets: string[] = []) {
	const deletes: string[] = [];

	await page.route('https://raw.githubusercontent.com/**/releases.json', (route) => route.fulfill({ json: [] }));
	await page.route('**/api/**', async (route) => {
		const request = route.request();
		const path = new URL(request.url()).pathname;
		if (!path.startsWith('/api/')) return route.continue();
		if (request.method() === 'GET') gets.push(path);

		if (path === '/api/auth/me') {
			return route.fulfill({
				json: { id: 'user-1', email: 'test@example.com', name: 'Test User', display_name: 'Test User', avatar_url: null }
			});
		}
		if (path === '/api/preferences') {
			return route.fulfill({
				json: {
					font_size: 'default',
					pointer_cursors: true,
					theme_mode: 'dark',
					light_theme: 'light',
					dark_theme: 'dark',
					workflow_sort_mode: 'default',
					workflow_sort_order: ['backlog', 'unstarted', 'started', 'completed', 'cancelled'],
					team_workflow_sort_overrides: {}
				}
			});
		}
		if (path === '/api/workspaces') {
			return route.fulfill({ json: [{ id: 'workspace-1', name: 'Test Workspace', slug: 'test' }] });
		}
		if (path === '/api/workspaces/test') {
			return route.fulfill({
				json: {
					id: 'workspace-1',
					name: 'Test Workspace',
					slug: 'test',
					logo_url: null,
					current_user_role: 'owner',
					created_at: '2026-01-01T00:00:00Z',
					updated_at: '2026-01-01T00:00:00Z'
				}
			});
		}
		if (path === '/api/workspaces/test/webhooks' && request.method() === 'GET') {
			return route.fulfill({ json: [webhook] });
		}
		if (path === '/api/workspaces/test/issue-templates' && request.method() === 'GET') {
			return route.fulfill({ json: [template] });
		}
		if (request.method() === 'DELETE') {
			deletes.push(path);
			return route.fulfill({ status: 204, body: '' });
		}
		if (
			path === '/api/workspaces/test/teams' ||
			path === '/api/workspaces/test/projects' ||
			path === '/api/workspaces/test/labels' ||
			path === '/api/workspaces/test/members' ||
			path === '/api/workspaces/test/views'
		) {
			return route.fulfill({ json: [] });
		}
		if (path === '/api/notifications') {
			return route.fulfill({ json: { notifications: [], unread_count: 0 } });
		}
		return route.fulfill({ status: 404, json: { error: { code: 'NOT_FOUND', message: `Unhandled ${path}` } } });
	});

	return deletes;
}

test('a webhook is deleted only after confirming', async ({ page }) => {
	const deletes = await mockSettings(page);
	await page.goto('/test/settings/webhooks');

	const deleteButton = page.getByRole('button', { name: `Delete webhook ${webhook.url}` });
	await expect(deleteButton).toBeVisible();

	// Cancelling keeps the webhook and sends nothing.
	await deleteButton.click();
	const dialog = page.getByRole('alertdialog', { name: 'Delete webhook?' });
	await expect(dialog).toBeVisible();
	await expect(dialog).toContainText(webhook.url);
	await dialog.getByRole('button', { name: 'Cancel' }).click();
	await expect(dialog).toBeHidden();

	// Escape dismisses too.
	await deleteButton.click();
	await expect(dialog).toBeVisible();
	await page.keyboard.press('Escape');
	await expect(dialog).toBeHidden();
	expect(deletes).toEqual([]);
	await expect(page.getByText(webhook.url)).toBeVisible();

	await deleteButton.click();
	await dialog.getByRole('button', { name: 'Delete webhook' }).click();
	await expect.poll(() => deletes).toEqual([`/api/workspaces/test/webhooks/${webhook.id}`]);
	await expect(page.getByText(webhook.url)).toBeHidden();
});

test('an issue template is deleted only after confirming, and its delete button is keyboard reachable', async ({
	page
}) => {
	const deletes = await mockSettings(page);
	await page.goto('/test/settings/templates');

	const deleteButton = page.getByRole('button', { name: `Delete template ${template.title}` });
	await expect(page.getByText(template.title)).toBeVisible();
	// Hover-only before; focus must reveal it now.
	await deleteButton.focus();
	await expect(deleteButton).toHaveCSS('opacity', '1');

	await deleteButton.click();
	const dialog = page.getByRole('alertdialog', { name: 'Delete template?' });
	await expect(dialog).toContainText(template.title);
	await dialog.getByRole('button', { name: 'Cancel' }).click();
	await expect(dialog).toBeHidden();
	expect(deletes).toEqual([]);

	await deleteButton.click();
	await dialog.getByRole('button', { name: 'Delete template' }).click();
	await expect.poll(() => deletes).toEqual([`/api/workspaces/test/issue-templates/${template.id}`]);
	await expect(page.getByText(template.title)).toBeHidden();
});

test('the delete dialog keeps Tab inside it and returns focus to its trigger when cancelled', async ({ page }) => {
	await mockSettings(page);
	await page.goto('/test/settings/webhooks');

	const deleteButton = page.getByRole('button', { name: `Delete webhook ${webhook.url}` });
	await deleteButton.click();
	const dialog = page.getByRole('alertdialog', { name: 'Delete webhook?' });
	const cancel = dialog.getByRole('button', { name: 'Cancel' });
	const confirm = dialog.getByRole('button', { name: 'Delete webhook' });
	await expect(cancel).toBeFocused();

	// Tab and Shift+Tab cycle between the two buttons and never reach the page.
	await page.keyboard.press('Tab');
	await expect(confirm).toBeFocused();
	await page.keyboard.press('Tab');
	await expect(cancel).toBeFocused();
	await page.keyboard.press('Shift+Tab');
	await expect(confirm).toBeFocused();
	await page.keyboard.press('Shift+Tab');
	await expect(cancel).toBeFocused();

	const activeLabel = () => page.evaluate(() => document.activeElement?.getAttribute('aria-label'));
	await cancel.click();
	await expect(dialog).toBeHidden();
	await expect(deleteButton).toBeFocused();
	expect(await activeLabel()).toBe(`Delete webhook ${webhook.url}`);

	// Opened from the keyboard, Escape restores focus too.
	await deleteButton.focus();
	await page.keyboard.press('Enter');
	await expect(cancel).toBeFocused();
	await page.keyboard.press('Escape');
	await expect(dialog).toBeHidden();
	await expect(deleteButton).toBeFocused();
	expect(await activeLabel()).toBe(`Delete webhook ${webhook.url}`);
});

test('workspace collections reload after the realtime connection drops and reconnects', async ({ page }) => {
	let connections = 0;
	await page.routeWebSocket('**/api/workspaces/*/ws', (ws) => {
		connections += 1;
		// Drop the first connection; the layout reconnects after its backoff.
		if (connections === 1) setTimeout(() => ws.close(), 500);
	});
	const gets: string[] = [];
	await mockSettings(page, gets);
	const projectLoads = () => gets.filter((path) => path === '/api/workspaces/test/projects').length;
	const labelLoads = () => gets.filter((path) => path === '/api/workspaces/test/labels').length;
	await page.goto('/test/settings/webhooks');
	await expect(page.getByText(webhook.url)).toBeVisible();

	// The first connection does not trigger a reload.
	await expect.poll(() => connections).toBe(1);
	expect(projectLoads()).toBe(1);
	expect(labelLoads()).toBe(1);

	// Events missed while disconnected are recovered by reloading everything.
	await expect.poll(() => connections, { timeout: 10_000 }).toBe(2);
	await expect.poll(projectLoads).toBe(2);
	await expect.poll(labelLoads).toBe(2);
});

// Sends the workspace socket to an unreachable address while `__wsBlocked` is
// set, so connection attempts fail without ever opening, and counts the
// layout's disconnect events.
async function installBlockableWebSocket(page: Page, blocked: boolean) {
	await page.addInitScript((initiallyBlocked) => {
		const w = window as unknown as { __wsBlocked: boolean; __wsDisconnects: number };
		w.__wsBlocked = initiallyBlocked;
		w.__wsDisconnects = 0;
		window.addEventListener('ws:disconnected', () => (w.__wsDisconnects += 1));
		const NativeWebSocket = window.WebSocket;
		window.WebSocket = class extends NativeWebSocket {
			constructor(url: string | URL, protocols?: string | string[]) {
				super(w.__wsBlocked ? 'ws://127.0.0.1:9/' : url, protocols);
			}
		};
	}, blocked);
}

const disconnects = (page: Page) =>
	page.evaluate(() => (window as unknown as { __wsDisconnects: number }).__wsDisconnects);

// Lets the next attempt through and reconnects now instead of after backoff.
async function restoreWebSocket(page: Page) {
	await page.evaluate(() => {
		(window as unknown as { __wsBlocked: boolean }).__wsBlocked = false;
		window.dispatchEvent(new Event('online'));
	});
}

test('collections reload when the first connection opens only after failed attempts', async ({ page }) => {
	let connections = 0;
	await page.routeWebSocket('**/api/workspaces/*/ws', () => {
		connections += 1;
	});
	await installBlockableWebSocket(page, true);
	const gets: string[] = [];
	await mockSettings(page, gets);
	const projectLoads = () => gets.filter((path) => path === '/api/workspaces/test/projects').length;
	const labelLoads = () => gets.filter((path) => path === '/api/workspaces/test/labels').length;
	await page.goto('/test/settings/webhooks');
	await expect(page.getByText(webhook.url)).toBeVisible();
	await expect.poll(() => disconnects(page)).toBeGreaterThan(0);
	expect(projectLoads()).toBe(1);
	expect(labelLoads()).toBe(1);

	await restoreWebSocket(page);
	await expect.poll(() => connections).toBe(1);
	await expect.poll(projectLoads).toBe(2);
	await expect.poll(labelLoads).toBe(2);
});

test('pages never reuse a collection loaded while the realtime connection is down', async ({ page }) => {
	const sockets: { close: () => Promise<void> }[] = [];
	await page.routeWebSocket('**/api/workspaces/*/ws', (ws) => {
		sockets.push(ws);
	});
	await installBlockableWebSocket(page, false);
	const gets: string[] = [];
	await mockSettings(page, gets);
	// Teams are loaded by the workspace layout and, on settings pages, by the
	// settings layout too; both reload them on a refresh.
	const teamLoads = () => gets.filter((path) => path === '/api/workspaces/test/teams').length;
	await page.goto('/test/settings/webhooks');
	await expect(page.getByText(webhook.url)).toBeVisible();
	await expect.poll(() => sockets.length).toBe(1);
	await expect.poll(teamLoads).toBe(2);

	// Drop the socket and keep it down.
	await page.evaluate(() => ((window as unknown as { __wsBlocked: boolean }).__wsBlocked = true));
	await sockets[0].close();
	await expect.poll(() => disconnects(page)).toBeGreaterThan(0);

	// A refresh during the outage reloads teams in both layouts...
	await page.evaluate(() =>
		window.dispatchEvent(new CustomEvent('app:refresh', { detail: { slug: 'test', resources: ['teams'] } }))
	);
	await expect.poll(teamLoads).toBe(4);

	// ...but the projects page fetches its own copy instead of reusing the
	// workspace layout's.
	await page.locator('body').click({ position: { x: 1, y: 1 } });
	await page.keyboard.press('g');
	await page.keyboard.press('p');
	await expect(page).toHaveURL(/\/test\/projects$/);
	await expect.poll(teamLoads).toBe(5);

	// Recovery reloads everything again.
	await restoreWebSocket(page);
	await expect.poll(() => sockets.length).toBe(2);
	await expect.poll(teamLoads).toBe(6);
});
