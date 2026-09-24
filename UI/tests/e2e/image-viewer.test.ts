import { test, expect } from '@playwright/test';

const TOKEN = 'image-viewer-token';
const IMAGE_SRC = '/api/public/assets/image-viewer-asset';

// 1x1 transparent PNG; the issue description stretches it with an explicit width.
const PNG = Buffer.from(
	'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==',
	'base64'
);

const meta = {
	scope: 'workspace',
	scope_name: 'Test',
	workspace_name: 'Test Workspace',
	filters: {},
	statuses: [{ id: 'todo', name: 'Todo', category: 'unstarted', color: '#888888', position: 0 }]
};

const issue = {
	identifier: 'ENG-1',
	title: 'Issue with a diagram',
	description: `<p><img src="${IMAGE_SRC}" alt="diagram.png" width="240"></p>`,
	status: 'todo',
	status_info: { id: 'todo', name: 'Todo', category: 'unstarted', color: '#888888' },
	priority: 2,
	labels: [],
	assignees: [],
	created_at: '2026-01-01T00:00:00Z',
	updated_at: '2026-01-01T00:00:00Z'
};

test.describe('issue image viewer', () => {
	test.use({ permissions: ['clipboard-read', 'clipboard-write'] });

	test('opens an image in the editable issue description on click', async ({ page }) => {
		const teamId = '00000000-0000-0000-0000-000000000010';
		await page.route('https://raw.githubusercontent.com/**/releases.json', (route) => route.fulfill({ json: [] }));
		await page.route('**/api/**', (route) => {
			const path = new URL(route.request().url()).pathname;
			if (path === '/api/auth/me') return route.fulfill({ json: { id: 'u1', email: 'test@example.com', name: 'Test User', display_name: 'Test User' } });
			if (path === '/api/preferences') return route.fulfill({ json: { font_size: 'default', pointer_cursors: true, theme_mode: 'dark', light_theme: 'light', dark_theme: 'dark', workflow_sort_mode: 'default', workflow_sort_order: ['backlog', 'unstarted', 'started', 'completed', 'cancelled'], team_workflow_sort_overrides: {}, issues_group_by: 'status' } });
			if (path === '/api/workspaces') return route.fulfill({ json: [{ id: 'w1', name: 'Test Workspace', slug: 'test' }] });
			if (path === '/api/workspaces/test') return route.fulfill({ json: { id: 'w1', name: 'Test Workspace', slug: 'test', current_user_role: 'owner' } });
			if (path === '/api/workspaces/test/teams') return route.fulfill({ json: [{ id: teamId, name: 'Engineering', key: 'ENG', color: '#6366f1', icon: 'layers' }] });
			if (['/api/workspaces/test/projects', '/api/workspaces/test/labels', '/api/workspaces/test/members', '/api/workspaces/test/views', `/api/workspaces/test/teams/${teamId}/statuses`, `/api/workspaces/test/teams/${teamId}/cycles`, '/api/workspaces/test/issues/ENG-1/comments', '/api/workspaces/test/issues/ENG-1/history'].includes(path)) return route.fulfill({ json: [] });
			if (path === '/api/notifications') return route.fulfill({ json: { notifications: [], unread_count: 0 } });
			if (path === '/api/workspaces/test/issues/ENG-1') return route.fulfill({ json: { ...issue, id: 'i1', team_id: teamId } });
			if (path === '/api/workspaces/test/issues') return route.fulfill({ json: { data: [], total_count: 0, page: 1, has_more: false } });
			if (path === IMAGE_SRC) return route.fulfill({ status: 200, contentType: 'image/png', body: PNG });
			return route.fulfill({ status: 404, json: { error: { message: `Unhandled ${path}` } } });
		});

		await page.goto('/test/issue/ENG-1');
		const image = page.locator('.rich-editor img[alt="diagram.png"]');
		await expect(image).toBeVisible();
		await image.click();
		await expect(page.getByRole('dialog', { name: 'Image viewer' })).toBeVisible();
		await page.keyboard.press('Escape');
		await expect(page.getByRole('dialog', { name: 'Image viewer' })).toBeHidden();
		await expect(page.getByRole('button', { name: 'Resize image' })).toBeVisible();
	});

	test('opens from a shared issue image and offers zoom, download, copy and close', async ({ page }) => {
		await page.route('https://raw.githubusercontent.com/**/releases.json', (route) => route.fulfill({ json: [] }));
		await page.route('**/api/public/**', (route) => {
			const { pathname } = new URL(route.request().url());
			if (pathname === `/api/public/share/${TOKEN}`) return route.fulfill({ json: meta });
			if (pathname === `/api/public/share/${TOKEN}/issues`) {
				return route.fulfill({ json: { data: [issue], total_count: 1, page: 1, has_more: false } });
			}
			if (pathname.startsWith('/api/public/assets/')) {
				return route.fulfill({
					status: 200,
					headers: {
						'content-type': 'image/png',
						'content-disposition': 'inline; filename="diagram.png"'
					},
					body: PNG
				});
			}
			return route.fulfill({ status: 404, json: { error: { message: `Unhandled ${pathname}` } } });
		});

		await page.goto(`/share/${TOKEN}`);
		await page.getByText('Issue with a diagram').click();

		// The delegated action turns rendered images into accessible buttons.
		const trigger = page.getByRole('button', { name: 'View image: diagram.png' });
		await expect(trigger).toBeVisible();
		await trigger.click();

		const viewer = page.getByRole('dialog', { name: 'Image viewer' });
		await expect(viewer).toBeVisible();

		const zoomLabel = viewer.getByRole('button', { name: /Zoom level/ });
		await expect(zoomLabel).toHaveText('100%');
		await viewer.getByRole('button', { name: 'Zoom in' }).click();
		await expect(zoomLabel).toHaveText('125%');
		await viewer.getByRole('button', { name: 'Zoom out' }).click();
		await expect(zoomLabel).toHaveText('100%');

		const downloadPromise = page.waitForEvent('download');
		await viewer.getByRole('button', { name: 'Download' }).click();
		const download = await downloadPromise;
		expect(download.suggestedFilename()).toBe('diagram.png');

		await viewer.getByRole('button', { name: 'Copy image', exact: true }).click();
		await expect(page.getByText(/Image (copied to clipboard|link copied)/)).toBeVisible();

		await page.keyboard.press('Escape');
		await expect(viewer).toBeHidden();
	});
});
