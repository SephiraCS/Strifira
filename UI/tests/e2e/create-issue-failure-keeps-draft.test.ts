import { test, expect, type Page } from '@playwright/test';

// A failed issue creation must never discard the draft. The dialog stays
// open with the title, the description (an uploaded image included) and
// everything else the user typed, so the retry sends the same ticket.
// Attachments must also survive an expired access token: uploads go through
// the API client, which refreshes the session and retries on 401.

// 1x1 transparent PNG — the upload itself is mocked at the API level, the
// editor only needs a decodable image to render the node.
const PNG = Buffer.from(
	'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==',
	'base64'
);
const ASSET = '/api/workspaces/test/assets/asset-1';
const CREATE_ERROR = 'Create issue failed (mocked)';
const teamId = '00000000-0000-0000-0000-000000000010';

const issue = {
	id: 'i1',
	team_id: teamId,
	identifier: 'ENG-1',
	title: 'Host issue',
	description: '<p>Short description</p>',
	status: 'todo',
	status_info: { id: 'todo', name: 'Todo', category: 'unstarted', color: '#888888' },
	priority: 0,
	labels: [],
	assignees: [],
	created_at: '2026-01-01T00:00:00Z',
	updated_at: '2026-01-01T00:00:00Z'
};

function deferred() {
	let resolve!: () => void;
	const promise = new Promise<void>((complete) => (resolve = complete));
	return { promise, resolve };
}

async function mockWorkspace(
	page: Page,
	state: { failuresLeft: number; nextCreateGate?: Promise<void>; rejectUploadsWith401?: number },
	posts: string[],
	calls: { uploads: number; refreshes: number } = { uploads: 0, refreshes: 0 }
) {
	await page.route('https://raw.githubusercontent.com/**/releases.json', (route) => route.fulfill({ json: [] }));
	await page.route('**/api/**', async (route) => {
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
		if (path === '/api/workspaces')
			return route.fulfill({ json: [{ id: 'w1', name: 'Test Workspace', slug: 'test' }] });
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
			posts.push(route.request().postData() ?? '');
			const held = state.nextCreateGate;
			state.nextCreateGate = undefined;
			if (held) await held;
			if (state.failuresLeft > 0) {
				state.failuresLeft--;
				return route.fulfill({ status: 500, json: { error: { message: CREATE_ERROR } } });
			}
			return route.fulfill({ json: { ...issue, id: 'i2', identifier: 'ENG-2' } });
		}
		if (path === '/api/workspaces/test/issues/ENG-1' || path === '/api/workspaces/test/issues')
			return route.fulfill({
				json: path.endsWith('/issues') ? { data: [issue], total_count: 1, page: 1, has_more: false } : issue
			});
		if (path === '/api/auth/refresh' && route.request().method() === 'POST') {
			calls.refreshes++;
			return route.fulfill({ json: {} });
		}
		if (path === '/api/workspaces/test/upload' && route.request().method() === 'POST') {
			calls.uploads++;
			if ((state.rejectUploadsWith401 ?? 0) > 0) {
				state.rejectUploadsWith401!--;
				return route.fulfill({ status: 401, json: { error: { message: 'access token expired' } } });
			}
			return route.fulfill({ json: { url: ASSET, filename: 'shot.png', content_type: 'image/png', size: PNG.length } });
		}
		return route.fulfill({ status: 404, json: { error: { message: `Unhandled ${path}` } } });
	});
}

test('a failed creation keeps the dialog open with the whole draft, and the retry succeeds', async ({ page }) => {
	const state = { failuresLeft: 1, nextCreateGate: undefined as Promise<void> | undefined };
	const posts: string[] = [];
	await mockWorkspace(page, state, posts);

	await page.goto('/test/issue/ENG-1');
	await page.getByTitle('New issue', { exact: true }).click();
	const dialog = page.getByRole('dialog');
	const editor = dialog.locator('.rich-editor .tiptap');
	await editor.waitFor();

	// Description: text plus an uploaded screenshot — the exact kind of
	// ticket that used to vanish when the create request failed.
	await editor.click();
	await page.keyboard.type('Screenshot attached below:');
	const [chooser] = await Promise.all([
		page.waitForEvent('filechooser'),
		dialog.getByRole('button', { name: 'Upload image' }).click()
	]);
	await chooser.setFiles({ name: 'shot.png', mimeType: 'image/png', buffer: PNG });
	const image = dialog.locator('.rich-editor img.image-viewer-trigger');
	await expect(image).toBeVisible();

	const title = page.locator('#create-issue-title');
	await title.fill('Bug report with screenshot');

	// First attempt: the request hangs, then the server rejects it.
	const firstCreate = deferred();
	state.nextCreateGate = firstCreate.promise;
	await dialog.getByRole('button', { name: 'Create issue' }).click();

	// While in flight the button is disabled (and shows it is working), so
	// neither a click nor Ctrl+Enter can fire a duplicate request.
	await expect(dialog.getByRole('button', { name: 'Creating…' })).toBeDisabled();
	await title.press('Control+Enter');
	await expect.poll(() => posts.length).toBe(1);

	firstCreate.resolve();
	await expect(page.locator('[data-sonner-toast]').filter({ hasText: CREATE_ERROR })).toBeVisible();

	// The failure must not close the dialog or drop any of the draft.
	await expect(title).toBeVisible();
	await expect(title).toHaveValue('Bug report with screenshot');
	await expect(image).toBeVisible();
	await expect(editor).toContainText('Screenshot attached below:');
	await expect(dialog.getByRole('button', { name: 'Create issue' })).toBeEnabled();

	// The retry sends the preserved draft and closes the dialog.
	await dialog.getByRole('button', { name: 'Create issue' }).click();
	await expect(page.locator('#create-issue-title')).toBeHidden();

	expect(posts).toHaveLength(2);
	const payload = JSON.parse(posts[1]);
	expect(payload.title).toBe('Bug report with screenshot');
	expect(payload.description).toContain('Screenshot attached below:');
	expect(payload.description).toContain(ASSET);
});

test('an image upload rejected by an expired access token is refreshed and retried', async ({ page }) => {
	const state = { failuresLeft: 0, rejectUploadsWith401: 1 };
	const posts: string[] = [];
	const calls = { uploads: 0, refreshes: 0 };
	await mockWorkspace(page, state, posts, calls);

	await page.goto('/test/issue/ENG-1');
	await page.getByTitle('New issue', { exact: true }).click();
	const dialog = page.getByRole('dialog');
	const editor = dialog.locator('.rich-editor .tiptap');
	await editor.waitFor();

	const [chooser] = await Promise.all([
		page.waitForEvent('filechooser'),
		dialog.getByRole('button', { name: 'Upload image' }).click()
	]);
	await chooser.setFiles({ name: 'shot.png', mimeType: 'image/png', buffer: PNG });

	// The 401 never reaches the user: the session is refreshed and the
	// upload retried, so the image lands in the description.
	const image = dialog.locator('.rich-editor img.image-viewer-trigger');
	await expect(image).toBeVisible();
	expect(calls.uploads).toBe(2);
	expect(calls.refreshes).toBeGreaterThanOrEqual(1);

	await page.locator('#create-issue-title').fill('Screenshot after token expiry');
	await dialog.getByRole('button', { name: 'Create issue' }).click();
	await expect(page.locator('#create-issue-title')).toBeHidden();

	const payload = JSON.parse(posts[0]);
	expect(payload.title).toBe('Screenshot after token expiry');
	expect(payload.description).toContain(ASSET);
});
