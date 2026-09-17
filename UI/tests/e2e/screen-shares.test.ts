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
	title: 'Bug triage walkthrough',
	room_name: 'ku-abc12345def67890',
	status: 'active',
	viewer_count: 2,
	created_at: new Date(Date.now() - 5 * 60 * 1000).toISOString(),
	ended_at: null
};

const endedShare = {
	...activeShare,
	id: '00000000-0000-0000-0000-000000000091',
	title: 'Yesterday demo',
	status: 'ended',
	viewer_count: 0,
	ended_at: new Date(Date.now() - 26 * 60 * 60 * 1000).toISOString(),
	created_at: new Date(Date.now() - 27 * 60 * 60 * 1000).toISOString()
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

test('lists active and recent shares and joins from the card', async ({ page }) => {
	await stubCommon(page, true);
	await page.goto('/test/shares');

	await expect(page.getByRole('heading', { name: 'Screen sharing' })).toBeVisible();
	await expect(page.getByText('Bug triage walkthrough')).toBeVisible();
	await expect(page.getByText('2 watching')).toBeVisible();
	await expect(page.getByText('Yesterday demo')).toBeVisible();

	let joinRequested = false;
	await page.route(`**/api/workspaces/test/shares/${shareId}/join`, (route) => {
		joinRequested = true;
		return route.fulfill({
			json: {
				share: activeShare,
				token: 'not-a-real-jwt',
				server_url: 'wss://livekit.invalid'
			}
		});
	});

	await page.getByRole('button', { name: /join/i }).first().click();
	await expect(page).toHaveURL(new RegExp(`/test/shares/${shareId}$`));
	await expect.poll(() => joinRequested).toBe(true);
	// LiveKit cannot connect to .invalid; the stage must land in the
	// ended/error state rather than spinning forever.
	await expect(page.getByText(/this screen share has ended|could not join/i)).toBeVisible({
		timeout: 15_000
	});
});

test('viewer page follows the share.ended workspace event', async ({ page }) => {
	await stubCommon(page, true);
	await page.route(`**/api/workspaces/test/shares/${shareId}/join`, (route) =>
		route.fulfill({
			json: {
				share: activeShare,
				token: 'not-a-real-jwt',
				server_url: 'wss://livekit.invalid'
			}
		})
	);

	await page.goto(`/test/shares/${shareId}`);
	await expect(page.getByText('Bug triage walkthrough')).toBeVisible();

	await page.evaluate((id) => {
		window.dispatchEvent(new CustomEvent('ws:share.ended', { detail: { id } }));
	}, shareId);

	await expect(page.getByText('This screen share has ended')).toBeVisible();
});

test('start flow posts a share and navigates to the stage', async ({ page }) => {
	await stubCommon(page, true);
	let createdBody: { title?: string } | null = null;
	await page.route('**/api/workspaces/test/shares', async (route) => {
		if (route.request().method() !== 'POST') {
			return route.fulfill({ json: { active: [], recent: [] } });
		}
		createdBody = route.request().postDataJSON();
		return route.fulfill({
			json: {
				share: { ...activeShare, started_by: '00000000-0000-0000-0000-000000000001', started_by_user: undefined },
				token: 'not-a-real-jwt',
				server_url: 'wss://livekit.invalid'
			}
		});
	});
	await page.route(`**/api/workspaces/test/shares/${shareId}/join`, (route) =>
		route.fulfill({
			json: {
				share: { ...activeShare, started_by: '00000000-0000-0000-0000-000000000001' },
				token: 'not-a-real-jwt',
				server_url: 'wss://livekit.invalid'
			}
		})
	);

	await page.goto('/test/shares');
	await page.getByRole('button', { name: 'Share my screen' }).click();
	await page.getByPlaceholder('e.g. Bug triage walkthrough').fill('Design review');
	await page.getByRole('button', { name: 'Start sharing' }).click();

	await expect(page).toHaveURL(new RegExp(`/test/shares/${shareId}$`));
	await expect.poll(() => createdBody).toEqual({ title: 'Design review' });
});
