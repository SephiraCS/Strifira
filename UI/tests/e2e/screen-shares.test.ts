import { expect, test } from '@playwright/test';

const shareId = '00000000-0000-0000-0000-000000000090';
const presenter = {
	id: '00000000-0000-0000-0000-000000000003',
	name: 'Filippo Rossi',
	display_name: 'Filippo',
	avatar_url: null
};

function workspacePayload(enabled: boolean) {
	return {
		id: '00000000-0000-0000-0000-000000000002',
		name: 'Test Workspace',
		slug: 'test',
		logo_url: null,
		owner_id: '00000000-0000-0000-0000-000000000001',
		share_link_min_role: 'member',
		current_user_role: 'member',
		screen_sharing_enabled: enabled,
		created_at: '2026-01-01T00:00:00Z',
		updated_at: '2026-01-01T00:00:00Z'
	};
}

const activeShare = {
	id: shareId,
	workspace_id: '00000000-0000-0000-0000-000000000002',
	started_by: presenter.id,
	started_by_user: presenter,
	room_name: 'ku-abc12345def67890',
	status: 'active',
	viewer_count: 2,
	created_at: new Date(Date.now() - 5 * 60 * 1000).toISOString(),
	ended_at: null
};

const endedShare = {
	...activeShare,
	id: '00000000-0000-0000-0000-000000000091',
	status: 'ended',
	viewer_count: 0,
	ended_at: new Date(Date.now() - 26 * 60 * 60 * 1000).toISOString(),
	created_at: new Date(Date.now() - 27 * 60 * 60 * 1000).toISOString()
};

const badJoin = {
	share: activeShare,
	token: 'not-a-real-jwt',
	server_url: 'wss://livekit.invalid'
};

async function stubCommon(page: import('@playwright/test').Page, enabled: boolean) {
	await page.route('https://raw.githubusercontent.com/carbogninalberto/kuayle/main/UI/static/releases.json', (route) =>
		route.fulfill({ json: [] })
	);
	await page.route('**/api/**', async (route) => {
		const path = new URL(route.request().url()).pathname;
		if (path === '/api/auth/me') {
			return route.fulfill({
				json: {
					id: '00000000-0000-0000-0000-000000000001',
					email: 'test@example.com',
					name: 'Test User',
					display_name: 'Test User',
					avatar_url: null
				}
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
		if (path === '/api/workspaces/test') {
			return route.fulfill({ json: workspacePayload(enabled) });
		}
		if (path === '/api/workspaces') {
			return route.fulfill({ json: [workspacePayload(enabled)] });
		}
		if (path === '/api/workspaces/test/teams') {
			return route.fulfill({ json: [] });
		}
		if (path === '/api/workspaces/test/members') {
			return route.fulfill({
				json: [
					{
						user_id: presenter.id,
						email: 'filippo@example.com',
						name: presenter.name,
						avatar_url: null,
						role: 'member',
						created_at: '2026-01-01T00:00:00Z'
					}
				]
			});
		}
		if (path === '/api/workspaces/test/projects') {
			return route.fulfill({ json: [] });
		}
		if (path === '/api/workspaces/test/labels') {
			return route.fulfill({ json: [] });
		}
		if (path === '/api/workspaces/test/views') {
			return route.fulfill({ json: [] });
		}
		if (path === '/api/notifications') {
			return route.fulfill({ json: { unread_count: 0, notifications: [] } });
		}
		if (path === `/api/workspaces/test/shares/${shareId}/join`) {
			return route.fulfill({ json: badJoin });
		}
		if (path === '/api/workspaces/test/shares') {
			return route.fulfill({
				json: enabled ? { active: [activeShare], recent: [endedShare] } : { active: [], recent: [] }
			});
		}
		return route.fulfill({ json: [] });
	});
}

test('shares module hidden when screen sharing is not configured', async ({ page }) => {
	await stubCommon(page, false);
	await page.goto('/test/inbox');
	await expect(page.getByRole('link', { name: 'Screen sharing' })).toHaveCount(0);
});

test('wall renders the presenter-named tile and recent history', async ({ page }) => {
	await stubCommon(page, true);
	await page.goto('/test/shares');

	await expect(page.getByRole('heading', { name: 'Screen sharing' })).toBeVisible();
	// One tile for the active share (join token fetched, LiveKit target
	// unreachable in the rig, so the tile lands in its ended state).
	const tile = page.locator(`[data-share-id="${shareId}"]`);
	await expect(tile).toHaveCount(1);
	await expect(tile).toContainText('Filippo');
	// Shares are presenter-named: no free-text title is rendered anywhere.
	await expect(page.getByText('Bug triage walkthrough')).toHaveCount(0);
	// History section is presenter-named too.
	const recent = page.getByRole('heading', { name: 'Recent' }).locator('..');
	await expect(recent).toContainText('Filippo');
});

test('start flow posts a titleless share and renders the own tile', async ({ page }) => {
	await stubCommon(page, true);
	let createdBody: unknown = null;
	await page.route('**/api/workspaces/test/shares', async (route) => {
		if (route.request().method() !== 'POST') {
			return route.fulfill({ json: { active: [], recent: [endedShare] } });
		}
		createdBody = route.request().postDataJSON();
		return route.fulfill({
			json: {
				share: { ...activeShare, started_by: '00000000-0000-0000-0000-000000000001' },
				token: 'not-a-real-jwt',
				server_url: 'wss://livekit.invalid'
			}
		});
	});

	await page.goto('/test/shares');
	await page.getByRole('button', { name: 'Share my screen' }).click();

	// No title dialog exists anymore: the share starts immediately and
	// the presenter's own tile mounts.
	await expect(page.locator(`[data-share-id="${shareId}"]`)).toHaveCount(1);
	await expect.poll(() => createdBody).toEqual({});
	// The header action hides while presenting.
	await expect(page.getByRole('button', { name: 'Share my screen' })).toHaveCount(0);
});

test('share.ended event removes the tile from the wall', async ({ page }) => {
	await stubCommon(page, true);
	await page.goto('/test/shares');
	await expect(page.locator(`[data-share-id="${shareId}"]`)).toHaveCount(1);

	await page.evaluate((id) => {
		window.dispatchEvent(new CustomEvent('ws:share.ended', { detail: { id } }));
	}, shareId);

	await expect(page.locator(`[data-share-id="${shareId}"]`)).toHaveCount(0);
});
