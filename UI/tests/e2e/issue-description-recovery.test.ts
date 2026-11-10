import { expect, test, type Page } from '@playwright/test';

const teamId = '00000000-0000-0000-0000-000000000010';
const originalDescription = '<p>Short description</p>';

function deferred() {
	let resolve!: () => void;
	const promise = new Promise<void>((complete) => (resolve = complete));
	return { promise, resolve };
}

async function mockIssue(page: Page) {
	const state = {
		issue: {
			id: 'issue-one',
			identifier: 'ENG-1',
			team_id: teamId,
			title: 'Description recovery issue',
			description: originalDescription,
			status: 'todo',
			status_id: 'status-todo',
			status_info: {
				id: 'status-todo',
				name: 'Todo',
				category: 'unstarted',
				color: '#888888',
				position: 0
			},
			priority: 0,
			labels: [],
			assignees: [],
			created_at: '2026-10-01T00:00:00Z',
			updated_at: '2026-10-01T00:00:00Z'
		},
		nextPatchGate: undefined as Promise<void> | undefined,
		nextReadGate: undefined as Promise<void> | undefined,
		failNextPatch: false
	};
	const calls = {
		reads: 0,
		completedReads: 0,
		writes: [] as string[],
		completedWrites: 0,
		activeWrites: 0,
		maxActiveWrites: 0
	};
	await page.route('https://raw.githubusercontent.com/**/releases.json', (route) => route.fulfill({ json: [] }));
	await page.route('**/api/**', async (route) => {
		const path = new URL(route.request().url()).pathname;
		if (path === '/api/auth/me')
			return route.fulfill({
				json: {
					id: 'user-one',
					name: 'Test User',
					display_name: 'Test User',
					email: 'test@example.com'
				}
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
					team_workflow_sort_overrides: {}
				}
			});
		const workspace = {
			id: 'workspace-one',
			slug: 'test',
			name: 'Test Workspace',
			current_user_role: 'owner',
			screen_sharing_enabled: false
		};
		if (path === '/api/workspaces') return route.fulfill({ json: [workspace] });
		if (path === '/api/workspaces/test') return route.fulfill({ json: workspace });
		if (path === '/api/workspaces/test/teams')
			return route.fulfill({
				json: [
					{
						id: teamId,
						name: 'Engineering',
						key: 'ENG',
						icon: 'layers',
						color: '#6366f1'
					}
				]
			});
		if (path === `/api/workspaces/test/teams/${teamId}/statuses`)
			return route.fulfill({
				json: [
					{
						...state.issue.status_info,
						team_id: teamId,
						slug: 'todo',
						is_default: true
					}
				]
			});
		if (path === '/api/notifications') return route.fulfill({ json: { notifications: [], unread_count: 0 } });
		if (path === '/api/workspaces/test/github/status')
			return route.fulfill({ json: { configured: false, connected: false } });
		if (path === '/api/workspaces/test/ai-settings')
			return route.fulfill({
				json: {
					provider: 'openai_compatible',
					base_url: '',
					model: '',
					has_api_key: false
				}
			});
		if (path === '/api/workspaces/test/issues')
			return route.fulfill({
				json: { data: [state.issue], total_count: 1, page: 1, has_more: false }
			});
		if (path === '/api/workspaces/test/issues/ENG-1') {
			if (route.request().method() === 'PATCH') {
				const html = route.request().postDataJSON().description as string;
				calls.writes.push(html);
				calls.activeWrites++;
				calls.maxActiveWrites = Math.max(calls.maxActiveWrites, calls.activeWrites);
				const held = state.nextPatchGate;
				state.nextPatchGate = undefined;
				try {
					if (held) await held;
					if (state.failNextPatch) {
						state.failNextPatch = false;
						return await route.fulfill({
							status: 503,
							json: { error: { message: 'Description save unavailable' } }
						});
					}
					state.issue = { ...state.issue, description: html };
					return await route.fulfill({ json: state.issue });
				} finally {
					calls.activeWrites--;
					calls.completedWrites++;
				}
			}
			calls.reads++;
			// Capture at request start: a blocked GET remains stale after PATCH commits.
			const snapshot = { ...state.issue };
			const held = state.nextReadGate;
			state.nextReadGate = undefined;
			if (held) await held;
			await route.fulfill({ json: snapshot });
			calls.completedReads++;
			return;
		}
		return route.fulfill({ json: [] });
	});
	return { state, calls };
}

async function openIssue(page: Page) {
	await page.clock.install();
	await page.goto('/test/issue/ENG-1');
	await page.getByText('No activity yet', { exact: true }).waitFor();
	await expect(page.getByText('No activity yet', { exact: true })).toBeVisible();
	const description = page.locator('.rich-editor .tiptap').first();
	await expect(description).toHaveText('Short description');
	return description;
}

async function dispatchRefresh(page: Page, source: 'update' | 'reconnect') {
	await page.evaluate((source) => {
		window.dispatchEvent(
			new CustomEvent(source === 'update' ? 'ws:issue-updated' : 'ws:reconnected', {
				detail: source === 'update' ? { identifier: 'ENG-1' } : { workspaceSlug: 'test' }
			})
		);
	}, source);
}

for (const source of ['update', 'reconnect'] as const) {
	test(`defers ${source} GET until an in-flight description PATCH settles and preserves editor undo`, async ({
		page
	}) => {
		const { state, calls } = await mockIssue(page);
		const description = await openIssue(page);
		const initialReads = calls.reads;
		const patch = deferred();
		state.nextPatchGate = patch.promise;
		try {
			await description.click();
			await page.keyboard.press('Control+End');
			await page.keyboard.type(' appended text');
			await page.clock.fastForward(2100);
			await expect.poll(() => calls.writes.length).toBe(1);
			await dispatchRefresh(page, source);
			await page.clock.fastForward(6000);
			expect(calls.reads).toBe(initialReads);
			expect(calls.completedWrites).toBe(0);
			await page.locator('.rich-editor .tiptap').last().click();
			await expect(description).toHaveText('Short description appended text');
		} finally {
			patch.resolve();
		}
		await expect.poll(() => calls.completedWrites).toBe(1);
		await page.clock.runFor(300);
		await expect.poll(() => calls.reads).toBe(initialReads + 1);
		await expect(description).toHaveText('Short description appended text');
		await description.click();
		await page.keyboard.press('Control+z');
		await expect(description).toHaveText('Short description');
	});
}

test('rejects a GET snapshot overlapping a local description mutation', async ({ page }) => {
	const { state, calls } = await mockIssue(page);
	const description = await openIssue(page);
	const initialReads = calls.reads;
	const read = deferred();
	state.nextReadGate = read.promise;
	try {
		await dispatchRefresh(page, 'update');
		await page.clock.runFor(250);
		await expect.poll(() => calls.reads).toBe(initialReads + 1);
		await description.fill('Latest local description');
		await page.clock.fastForward(2100);
		await expect.poll(() => calls.completedWrites).toBe(1);
		await page.locator('.rich-editor .tiptap').last().click();
		await expect(description).toHaveText('Latest local description');
	} finally {
		read.resolve();
	}
	await expect.poll(() => calls.completedReads).toBe(initialReads + 1);
	await page.evaluate(() => Promise.resolve());
	await description.click();
	await page.locator('.rich-editor .tiptap').last().click();
	await expect(description).toHaveText('Latest local description');
	expect(state.issue.description).toBe('<p>Latest local description</p>');
});

test('serializes a pending description edit and its undo so the server finishes with the latest draft', async ({
	page
}) => {
	const { state, calls } = await mockIssue(page);
	const description = await openIssue(page);
	const patch = deferred();
	state.nextPatchGate = patch.promise;
	try {
		await description.click();
		await page.keyboard.press('Control+End');
		await page.keyboard.type(' appended text');
		await page.clock.fastForward(2100);
		await expect.poll(() => calls.writes.length).toBe(1);
		await page.keyboard.press('Control+z');
		await expect(description).toHaveText('Short description');
		await page.clock.fastForward(2100);
		expect(calls.writes).toHaveLength(1);
	} finally {
		patch.resolve();
	}
	await expect.poll(() => calls.completedWrites).toBe(2);
	expect(calls.maxActiveWrites).toBe(1);
	expect(calls.writes).toEqual(['<p>Short description appended text</p>', originalDescription]);
	expect(state.issue.description).toBe(originalDescription);
	await page.locator('.rich-editor .tiptap').last().click();
	await expect(description).toHaveText('Short description');
});

test('preserves a failed description draft across reconnect and blur, then retries the save', async ({ page }) => {
	const { state, calls } = await mockIssue(page);
	const description = await openIssue(page);
	const initialReads = calls.reads;
	state.failNextPatch = true;
	await description.fill('Unsaved local description');
	await page.clock.fastForward(2100);
	await expect.poll(() => calls.completedWrites).toBe(1);
	const failure = page.getByRole('main').getByRole('alert').filter({ hasText: 'Failed to update description' });
	await expect(failure).toBeVisible();
	await dispatchRefresh(page, 'reconnect');
	await page.clock.fastForward(6000);
	expect(calls.reads).toBe(initialReads);
	await page.locator('.rich-editor .tiptap').last().click();
	await expect(description).toHaveText('Unsaved local description');
	await failure.getByRole('button', { name: 'Retry', exact: true }).click();
	await expect.poll(() => calls.completedWrites).toBe(2);
	await expect(failure).toHaveCount(0);
	await expect(description).toHaveText('Unsaved local description');
	expect(state.issue.description).toBe('<p>Unsaved local description</p>');
});

test('description exposes file controls and persists an uploaded image after reopening', async ({ page }) => {
	const { state } = await mockIssue(page);
	const png = Buffer.from(
		'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+ip1sAAAAASUVORK5CYII=',
		'base64'
	);
	const asset = '/api/workspaces/test/assets/00000000-0000-0000-0000-000000000027';
	await page.route('**/api/workspaces/test/upload', (route) => {
		expect(route.request().headers()['content-type']).toContain('multipart/form-data');
		expect(route.request().postDataBuffer()?.includes(png)).toBe(true);
		return route.fulfill({ json: { url: asset, filename: 'task.png', content_type: 'image/png' } });
	});
	await page.route(`**${asset}`, (route) => route.fulfill({ contentType: 'image/png', body: png }));
	const description = await openIssue(page);
	const controls = page.locator('.rich-editor').first().locator('..');
	await expect(controls.getByRole('button', { name: 'Attach files', exact: true })).toBeVisible();
	const chooser = page.waitForEvent('filechooser');
	await controls.getByRole('button', { name: 'Upload image', exact: true }).click();
	await (await chooser).setFiles({ name: 'task.png', mimeType: 'image/png', buffer: png });
	const image = description.locator('img');
	await expect(image).toHaveAttribute('src', asset);
	await expect
		.poll(() => image.evaluate((img: HTMLImageElement) => img.naturalWidth > 0 && img.naturalHeight > 0))
		.toBe(true);
	await page.clock.fastForward(2100);
	await expect.poll(() => state.issue.description).toContain(asset);
	expect(state.issue.description).toContain('Short description');
	await page.reload();
	await expect(page.locator('.rich-editor .tiptap').first().locator('img')).toHaveAttribute('src', asset);
});

test('unavailable upload preserves the description draft and allows retry', async ({ page }) => {
	const { state } = await mockIssue(page);
	await page.route('**/api/workspaces/test/upload', (route) =>
		route.fulfill({
			status: 503,
			json: {
				error: {
					code: 'STORAGE_UNAVAILABLE',
					message: 'File storage is temporarily unavailable. Please try again shortly.'
				}
			}
		})
	);
	const description = await openIssue(page);
	await description.fill('Keep this draft');
		const controls = page.locator('.rich-editor').first().locator('..');
	for (let attempt = 0; attempt < 2; attempt++) {
		const chooser = page.waitForEvent('filechooser');
		await controls.getByRole('button', { name: 'Attach files', exact: true }).click();
		await (await chooser).setFiles({ name: 'notes.txt', mimeType: 'text/plain', buffer: Buffer.from('notes') });
		await expect(
			page.getByText('File storage is temporarily unavailable. Please try again shortly.').last()
		).toBeVisible();
		await expect(description).toHaveText('Keep this draft');
		await expect(description.locator('img, [data-type="attachment"]')).toHaveCount(0);
	}
	await page.clock.fastForward(2100);
	await expect.poll(() => state.issue.description).toBe('<p>Keep this draft</p>');
});
