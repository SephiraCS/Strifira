import { test, expect, type Page } from '@playwright/test';
import zlib from 'node:zlib';

// The create-issue dialog renders pasted images as large inline nodes. Clicking
// one selects it (its wrapper is a drag handle), and Enter / Shift+Enter used
// to replace the selected node with a line break — silently destroying the
// image. These tests pin the breaks-beside-the-image behavior.

// --- minimal PNG encoder so the editor gets a real, large image ---
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
	ihdr[8] = 8; // bit depth
	ihdr[9] = 2; // truecolor
	const stride = width * 3;
	const raw = Buffer.alloc((stride + 1) * height);
	for (let y = 0; y < height; y++) {
		const row = y * (stride + 1);
		raw[row] = 0; // filter none
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

async function mockWorkspace(page: Page, createdIssues: string[]) {
	await page.route('https://raw.githubusercontent.com/**/releases.json', (route) => route.fulfill({ json: [] }));
	await page.route('**/api/**', (route) => {
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
		if (path === '/api/workspaces/test/issues' && route.request().method() === 'POST') {
			createdIssues.push(route.request().postData() ?? '');
			return route.fulfill({ json: { ...issue, id: 'i2', identifier: 'ENG-2' } });
		}
		if (path === '/api/workspaces/test/issues/ENG-1' || path === '/api/workspaces/test/issues')
			return route.fulfill({
				json: path.endsWith('/issues') ? { data: [issue], total_count: 1, page: 1, has_more: false } : issue
			});
		if (path === '/api/workspaces/test/upload' && route.request().method() === 'POST')
			return route.fulfill({ json: { url: ASSET, filename: 'shot.png', content_type: 'image/png', size: PNG.length } });
		if (path === ASSET) return route.fulfill({ status: 200, contentType: 'image/png', body: PNG });
		return route.fulfill({ status: 404, json: { error: { message: `Unhandled ${path}` } } });
	});
}

async function uploadImage(page: Page, dialog: ReturnType<Page['getByRole']>) {
	const [chooser] = await Promise.all([
		page.waitForEvent('filechooser'),
		dialog.getByRole('button', { name: 'Upload image' }).click()
	]);
	await chooser.setFiles({ name: 'shot.png', mimeType: 'image/png', buffer: PNG });
	const image = dialog.locator('.rich-editor img.image-viewer-trigger');
	await expect(image).toBeVisible();
	return image;
}

test.describe('create issue image editing', () => {
	test('Shift+Enter above a selected image inserts a line break without destroying the image', async ({
		page
	}) => {
		const createdIssues: string[] = [];
		await mockWorkspace(page, createdIssues);

		await page.goto('/test/issue/ENG-1');
		await page.getByTitle('New issue', { exact: true }).click();
		const dialog = page.getByRole('dialog');
		const editor = dialog.locator('.rich-editor .tiptap');
		await editor.waitFor();

		const image = await uploadImage(page, dialog);

		// A plain click on the big inline image selects it and opens the preview;
		// dismissing the preview must not also dismiss the create dialog.
		const box = await image.boundingBox();
		await page.mouse.click(box!.x + 10, box!.y + 5);
		const viewer = page.getByRole('dialog', { name: 'Image viewer' });
		await expect(viewer).toBeVisible();
		await page.keyboard.press('Escape');
		await expect(viewer).toBeHidden();
		await expect(page.locator('#create-issue-title')).toBeVisible();

		await page.keyboard.press('Shift+Enter');

		// The image survives and a break now sits before it, so typing lands on
		// the line above the image.
		await expect(image).toBeVisible();
		await expect
			.poll(() =>
				editor.evaluate((el) => {
					const html = el.innerHTML;
					return html.indexOf('<br') > -1 && html.indexOf('<br') < html.indexOf('resizable-image-wrapper');
				})
			)
			.toBe(true);
		await page.keyboard.type('Screenshot below:');
		await expect(editor).toContainText('Screenshot below:');

		await page.locator('#create-issue-title').fill('Bug with screenshot');
		await dialog.getByRole('button', { name: 'Create issue' }).click();
		expect(createdIssues).toHaveLength(1);
		const payload = JSON.parse(createdIssues[0]);
		expect(payload.title).toBe('Bug with screenshot');
		expect(payload.description).toContain('Screenshot below:');
		expect(payload.description).toContain(ASSET);
	});

	test('Enter above a selected image splits the paragraph without destroying the image', async ({ page }) => {
		const createdIssues: string[] = [];
		await mockWorkspace(page, createdIssues);

		await page.goto('/test/issue/ENG-1');
		await page.getByTitle('New issue', { exact: true }).click();
		const dialog = page.getByRole('dialog');
		const editor = dialog.locator('.rich-editor .tiptap');
		await editor.waitFor();

		const image = await uploadImage(page, dialog);

		const box = await image.boundingBox();
		await page.mouse.click(box!.x + 10, box!.y + 5);
		await page.keyboard.press('Escape');
		await expect(page.getByRole('dialog', { name: 'Image viewer' })).toBeHidden();
		await expect(page.locator('#create-issue-title')).toBeVisible();

		await page.keyboard.press('Enter');

		await expect(image).toBeVisible();
		await expect
			.poll(() =>
				editor.evaluate((el) => {
					const paragraphs = Array.from(el.querySelectorAll('p'));
					return paragraphs.length === 2 && paragraphs[1].contains(el.querySelector('img.image-viewer-trigger'));
				})
			)
			.toBe(true);
	});
});
