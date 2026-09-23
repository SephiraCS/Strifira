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
