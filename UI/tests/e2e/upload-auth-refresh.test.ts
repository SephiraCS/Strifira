import { test, expect, type Page } from '@playwright/test';
import zlib from 'node:zlib';

// The editor's upload request must go through the api client, which refreshes
// an expired access token on 401 and retries. A bare fetch failed with
// "Authentication required" whenever a paste or upload happened more than
// 15 minutes after sign-in, while the rest of the app silently refreshed —
// so to users only image uploads and pastes appeared broken.

const CRC_TABLE: number[] = [];
for (let n = 0; n < 256; n++) {
	let c = n;
	for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
	CRC_TABLE[n] = c >>> 0;
}
function crc32(buf: Buffer): number {
	let c = 0xffffffff;
	for (const byte of buf) c = CRC_TABLE[(c ^ byte) & 0xff] ^ (c >>> 8);
	return (c ^ 0xffffffff) >>> 0;
}
function pngChunk(type: string, data: Buffer): Buffer {
	const len = Buffer.alloc(4);
	len.writeUInt32BE(data.length);
	const body = Buffer.concat([Buffer.from(type, 'ascii'), data]);
	const crc = Buffer.alloc(4);
	crc.writeUInt32BE(crc32(body));
	return Buffer.concat([len, body, crc]);
}
function solidPng(width: number, height: number): Buffer {
	const ihdr = Buffer.alloc(13);
	ihdr.writeUInt32BE(width, 0);
	ihdr.writeUInt32BE(height, 4);
	ihdr[8] = 8;
	ihdr[9] = 2;
	const stride = width * 3;
	const raw = Buffer.alloc((stride + 1) * height);
	for (let y = 0; y < height; y++) {
		const row = y * (stride + 1);
		raw[row] = 0;
		for (let x = 0; x < stride; x += 3) {
			raw[row + 1 + x] = 0x30;
			raw[row + 2 + x] = 0x80;
			raw[row + 3 + x] = 0xf0;
		}
	}
	return Buffer.concat([
		Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
		pngChunk('IHDR', ihdr),
		pngChunk('IDAT', zlib.deflateSync(raw)),
		pngChunk('IEND', Buffer.alloc(0))
	]);
}

const PNG = solidPng(400, 200);
const ASSET = '/api/public/assets/shot.png';
const teamId = '00000000-0000-0000-0000-000000000010';

const issue = {
	id: 'i1',
	team_id: teamId,
	identifier: 'ENG-1',
	title: 'Issue with image',
	description: '<p>Short description</p>',
	status: 'todo',
	status_info: { id: 'todo', name: 'Todo', category: 'unstarted', color: '#888888' },
	priority: 0,
	labels: [],
	assignees: [],
	created_at: '2026-01-01T00:00:00Z',
	updated_at: '2026-01-01T00:00:00Z'
};

async function mockWorkspace(page: Page, auth: { refreshCount: number }) {
	await page.route('https://raw.githubusercontent.com/**/releases.json', (route) => route.fulfill({ json: [] }));
	await page.route('**/api/**', (route) => {
		const method = route.request().method();
		const path = new URL(route.request().url()).pathname;
		if (path === '/api/auth/me')
			return route.fulfill({
				json: { id: 'u1', email: 'test@example.com', name: 'Test User', display_name: 'Test User' }
			});
		if (path === '/api/auth/refresh' && method === 'POST') {
			auth.refreshCount += 1;
			return route.fulfill({ json: {} });
		}
		if (path === '/api/preferences')
			return route.fulfill({
				json: {
					font_size: 'default',
					pointer_cursors: true,
					theme_mode: 'dark',
					light_theme: 'light',
					dark_theme: 'dark',
					workflow_sort_mode: 'default',
					workflow_sort_order: ['backlog', 'unstarted', 'started', 'completed', 'cancelled'],
					team_workflow_sort_overrides: {},
					issues_group_by: 'status'
				}
			});
		if (path === '/api/workspaces') return route.fulfill({ json: [{ id: 'w1', name: 'Test Workspace', slug: 'test' }] });
		if (path === '/api/workspaces/test')
			return route.fulfill({ json: { id: 'w1', name: 'Test Workspace', slug: 'test', current_user_role: 'owner' } });
		if (path === '/api/workspaces/test/teams')
			return route.fulfill({
				json: [{ id: teamId, name: 'Engineering', key: 'ENG', color: '#6366f1', icon: 'layers' }]
			});
		if (
			[
				'/api/workspaces/test/projects',
				'/api/workspaces/test/labels',
				'/api/workspaces/test/members',
				'/api/workspaces/test/views',
				'/api/workspaces/test/issue-templates',
				`/api/workspaces/test/teams/${teamId}/statuses`,
				`/api/workspaces/test/teams/${teamId}/cycles`,
				'/api/workspaces/test/issues/ENG-1/comments',
				'/api/workspaces/test/issues/ENG-1/history'
			].includes(path)
		)
			return route.fulfill({ json: [] });
		if (path === '/api/notifications') return route.fulfill({ json: { notifications: [], unread_count: 0 } });
		if (path === '/api/workspaces/test/issues/ENG-1' || path === '/api/workspaces/test/issues')
			return route.fulfill({
				json: path.endsWith('/issues') ? { data: [issue], total_count: 1, page: 1, has_more: false } : issue
			});
		if (path === '/api/workspaces/test/upload' && method === 'POST') {
			// The access token has expired: the first attempt is refused until
			// the api client rotates it via /api/auth/refresh.
			if (auth.refreshCount === 0)
				return route.fulfill({
					status: 401,
					json: { error: { code: 'UNAUTHORIZED', message: 'Authentication required' } }
				});
			return route.fulfill({
				json: { url: ASSET, filename: 'shot.png', content_type: 'image/png', size: PNG.length }
			});
		}
		if (path === ASSET) return route.fulfill({ status: 200, contentType: 'image/png', body: PNG });
		return route.fulfill({ status: 404, json: { error: { message: `Unhandled ${path}` } } });
	});
}

test.describe('upload with an expired access token', () => {
	test('image upload refreshes the token on 401, retries, and inserts the image', async ({ page }) => {
		const auth = { refreshCount: 0 };
		await mockWorkspace(page, auth);

		await page.goto('/test/issue/ENG-1');
		await page.getByTitle('New issue', { exact: true }).click();
		const dialog = page.getByRole('dialog');
		const editor = dialog.locator('.rich-editor .tiptap');
		await editor.waitFor();
		await page.locator('#create-issue-title').fill('Screenshot after idle');

		const [chooser] = await Promise.all([
			page.waitForEvent('filechooser'),
			dialog.getByRole('button', { name: 'Upload image' }).click()
		]);
		await chooser.setFiles({ name: 'shot.png', mimeType: 'image/png', buffer: PNG });

		// The retried upload must actually insert the image, not just succeed.
		const image = dialog.locator('.rich-editor img.image-viewer-trigger');
		await expect(image).toBeVisible();
		await expect(image).toHaveAttribute('src', ASSET);
		expect(auth.refreshCount).toBe(1);
	});

	test('a paste that hits 401 refreshes the token and still lands the image', async ({ page }) => {
		const auth = { refreshCount: 0 };
		await mockWorkspace(page, auth);

		await page.goto('/test/issue/ENG-1');
		await page.getByTitle('New issue', { exact: true }).click();
		const dialog = page.getByRole('dialog');
		const editor = dialog.locator('.rich-editor .tiptap');
		await editor.waitFor();
		await page.locator('#create-issue-title').fill('Pasted screenshot');
		await editor.click();

		// Real clipboard paste: write the image to the clipboard, then paste
		// into the focused editor with the keyboard.
		await page.context().grantPermissions(['clipboard-read', 'clipboard-write']);
		await page.evaluate(async (bytes) => {
			const blob = new Blob([new Uint8Array(bytes)], { type: 'image/png' });
			await navigator.clipboard.write([new ClipboardItem({ 'image/png': blob })]);
		}, Array.from(PNG));
		await page.keyboard.press('Control+V');

		const image = dialog.locator('.rich-editor img.image-viewer-trigger');
		await expect(image).toBeVisible({ timeout: 10000 });
		await expect(image).toHaveAttribute('src', ASSET);
		expect(auth.refreshCount).toBe(1);
	});
});
