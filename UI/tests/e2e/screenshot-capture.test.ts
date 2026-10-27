import { expect, test, type Page } from '@playwright/test';
import zlib from 'node:zlib';

// The screenshot capture feature rides getDisplayMedia (screen/window/page)
// and the existing upload endpoint. The browser cannot open its real capture
// picker in tests, so every test swaps it for a canvas-backed MediaStream —
// the whole capture pipeline (frame grab, scroll-stitch, crop, upload) then
// runs against a real stream, only the source is synthetic.

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
	title: 'Issue with screenshot',
	description: '<p>Short description</p>',
	status: 'todo',
	status_info: { id: 'todo', name: 'Todo', category: 'unstarted', color: '#888888' },
	priority: 0,
	labels: [],
	assignees: [],
	created_at: '2026-01-01T00:00:00Z',
	updated_at: '2026-01-01T00:00:00Z'
};

async function stubDisplayCapture(page: Page) {
	await page.addInitScript(() => {
		const canvas = document.createElement('canvas');
		canvas.width = 1280;
		canvas.height = 800;
		const ctx = canvas.getContext('2d');
		if (ctx) {
			ctx.fillStyle = '#2563eb';
			ctx.fillRect(0, 0, canvas.width, canvas.height);
			ctx.fillStyle = '#fff';
			ctx.font = '48px monospace';
			ctx.fillText('Mock capture frame', 60, 100);
		}
		const stream = canvas.captureStream(30);
		navigator.mediaDevices.getDisplayMedia = async () => stream;
	});
}

async function mockWorkspace(page: Page, uploads: { count: number }) {
	let uploadCalls = 0;
	await page.route('https://raw.githubusercontent.com/**/releases.json', (route) => route.fulfill({ json: [] }));
	await page.route('**/api/**', (route) => {
		const method = route.request().method();
		const path = new URL(route.request().url()).pathname;
		if (path === '/api/auth/me')
			return route.fulfill({
				json: { id: 'u1', email: 'test@example.com', name: 'Test User', display_name: 'Test User' }
			});
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
			uploadCalls += 1;
			uploads.count = uploadCalls;
			return route.fulfill({
				json: { url: ASSET, filename: 'screenshot.png', content_type: 'image/png', size: PNG.length }
			});
		}
		if (path === ASSET) return route.fulfill({ status: 200, contentType: 'image/png', body: PNG });
		return route.fulfill({ status: 404, json: { error: { message: `Unhandled ${path}` } } });
	});
}

async function openCaptureMenu(page: Page) {
	await page.getByTitle('Capture screen', { exact: true }).click();
	const menu = page.locator('[data-slot="popover-content"]').filter({ hasText: 'Full page' });
	await menu.getByRole('button', { name: 'Full page' }).waitFor();
	return menu;
}

test.describe('screenshot capture', () => {
	test('the capture menu lists every mode next to the page title actions', async ({ page }) => {
		const uploads = { count: 0 };
		await mockWorkspace(page, uploads);
		await stubDisplayCapture(page);
		await page.goto('/test/issue/ENG-1');

		await page.getByTitle('Capture screen', { exact: true }).click();

		for (const mode of ['Entire screen', 'Specific window', 'Full page', 'Selected area']) {
			await expect(page.getByRole('button', { name: mode })).toBeVisible();
		}
		await page.keyboard.press('Escape');
		expect(uploads.count).toBe(0);
	});

	test('a full-page capture previews and lands in workspace files', async ({ page }) => {
		const uploads = { count: 0 };
		await mockWorkspace(page, uploads);
		await stubDisplayCapture(page);
		await page.goto('/test/issue/ENG-1');

		const menu = await openCaptureMenu(page);
		await menu.getByRole('button', { name: 'Full page' }).click();

		const dialog = page.getByRole('dialog').filter({ hasText: 'Screenshot' });
		await dialog.waitFor();
		await expect(dialog.locator('img')).toBeVisible();

		await dialog.getByRole('button', { name: 'Continue' }).click();

		const destination = page.getByRole('dialog').filter({ hasText: 'Where should this capture go?' });
		await destination.waitFor();
		await expect(destination.getByText('Comment on ENG-1')).toBeVisible();
		await destination.getByRole('button', { name: 'Workspace files' }).click();
		await destination.getByRole('button', { name: 'Attach' }).click();

		await expect(page.locator('[data-sonner-toast]').filter({ hasText: 'Capture saved to workspace files.' })).toBeVisible();
		expect(uploads.count).toBe(1);
		await expect(destination).not.toBeVisible();
	});

	test('a full-page capture can be attached as a comment on the open issue', async ({ page }) => {
		const uploads = { count: 0 };
		await mockWorkspace(page, uploads);
		await stubDisplayCapture(page);

		let commentCreated = false;
		await page.route('**/api/workspaces/test/issues/ENG-1/comments', (route) => {
			if (route.request().method() === 'POST') {
				commentCreated = true;
				return route.fulfill({ json: { id: 'c1', body: '<p><img src="' + ASSET + '"></p>' } });
			}
			return route.fulfill({ json: [] });
		});

		await page.goto('/test/issue/ENG-1');
		const menu = await openCaptureMenu(page);
		await menu.getByRole('button', { name: 'Full page' }).click();

		const dialog = page.getByRole('dialog').filter({ hasText: 'Screenshot' });
		await dialog.waitFor();
		await dialog.getByRole('button', { name: 'Continue' }).click();

		const destination = page.getByRole('dialog').filter({ hasText: 'Where should this capture go?' });
		await destination.getByRole('button', { name: 'Comment on ENG-1' }).click();
		await destination.getByRole('button', { name: 'Attach' }).click();

		await expect(
			page.locator('[data-sonner-toast]').filter({ hasText: 'Capture attached as a comment on ENG-1.' })
		).toBeVisible();
		expect(commentCreated).toBe(true);
	});

	test('the selected-area mode opens the crop stage and applies a crop', async ({ page }) => {
		const uploads = { count: 0 };
		await mockWorkspace(page, uploads);
		await stubDisplayCapture(page);
		await page.goto('/test/issue/ENG-1');

		const menu = await openCaptureMenu(page);
		await menu.getByRole('button', { name: 'Selected area' }).click();

		const cropStage = page.locator('[role="dialog"][aria-label="Select area"]');
		await cropStage.waitFor();

		const stage = cropStage.locator('img');
		const box = await stage.boundingBox();
		if (!box) throw new Error('crop image not laid out');
		await page.mouse.move(box.x + box.width * 0.2, box.y + box.height * 0.2);
		await page.mouse.down();
		await page.mouse.move(box.x + box.width * 0.7, box.y + box.height * 0.7, { steps: 8 });
		await page.mouse.up();

		const apply = cropStage.getByRole('button', { name: 'Apply crop' });
		await expect(apply).toBeEnabled();
		await apply.click();

		const preview = page.getByRole('dialog').filter({ hasText: 'Screenshot' });
		await preview.waitFor();
		await preview.getByRole('button', { name: 'Cancel' }).click();
		await expect(preview).not.toBeVisible();
		expect(uploads.count).toBe(0);
	});
});
