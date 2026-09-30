import { test, expect, type Page, type Route } from '@playwright/test';

const teamId = '00000000-0000-0000-0000-000000000010';

const issue = {
	id: 'i1',
	team_id: teamId,
	identifier: 'ENG-1',
	title: 'Issue with AI',
	description: '<p>Short description</p>',
	status: 'todo',
	status_info: { id: 'todo', name: 'Todo', category: 'unstarted', color: '#888888' },
	priority: 0,
	labels: [],
	assignees: [],
	created_at: '2026-01-01T00:00:00Z',
	updated_at: '2026-01-01T00:00:00Z'
};

type ReworkRequest = { text: string; context?: string };

type MockOptions = {
	aiConfigured: boolean;
	description?: string;
	reworkRequests?: string[];
	textRequests?: ReworkRequest[];
	expand?: (route: Route) => Promise<void>;
	rework?: (route: Route) => Promise<void>;
};

async function mockWorkspace(page: Page, opts: MockOptions) {
	let currentIssue = { ...issue, description: opts.description ?? issue.description };
	await page.route('https://raw.githubusercontent.com/**/releases.json', (route) => route.fulfill({ json: [] }));
	await page.route('**/api/**', (route) => {
		const url = new URL(route.request().url());
		const path = url.pathname;
		if (!path.startsWith('/api/')) return route.continue();

		if (path === '/api/auth/me') {
			return route.fulfill({
				json: { id: 'u1', email: 'test@example.com', name: 'Test User', display_name: 'Test User' }
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
					team_workflow_sort_overrides: {},
					issues_group_by: 'status'
				}
			});
		}
		if (path === '/api/workspaces') {
			return route.fulfill({ json: [{ id: 'w1', name: 'Test Workspace', slug: 'test' }] });
		}
		if (path === '/api/workspaces/test') {
			return route.fulfill({ json: { id: 'w1', name: 'Test Workspace', slug: 'test', current_user_role: 'owner' } });
		}
		if (path === '/api/workspaces/test/ai-settings') {
			return route.fulfill({
				json: {
					provider: 'openai_compatible',
					base_url: opts.aiConfigured ? 'https://ai.example.test/v1' : '',
					model: opts.aiConfigured ? 'test-model' : '',
					has_api_key: opts.aiConfigured,
					description_expand_prompt: 'Expand this issue description.',
					default_prompt: 'Expand this issue description.',
					issue_copy_prompt: '',
					default_issue_copy_prompt: '',
					created_at: '2026-01-01T00:00:00Z',
					updated_at: '2026-01-01T00:00:00Z'
				}
			});
		}
		if (path === '/api/workspaces/test/issues/ENG-1/expand-description') {
			if (opts.reworkRequests) {
				opts.reworkRequests.push(`${route.request().method()} ${route.request().postData() ?? ''}`);
			}
			if (opts.expand) return opts.expand(route);
			return route.fulfill({ json: { description: '<p>Expanded by AI</p>' } });
		}
		if (path === '/api/workspaces/test/ai/rework') {
			opts.textRequests?.push(route.request().postDataJSON());
			if (opts.rework) return opts.rework(route);
			return route.fulfill({ json: { text: '<p>Rewritten draft by AI</p>' } });
		}
		if (path === '/api/workspaces/test/teams') {
			return route.fulfill({
				json: [{ id: teamId, name: 'Engineering', key: 'ENG', color: '#6366f1', icon: 'layers' }]
			});
		}
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
		) {
			return route.fulfill({ json: [] });
		}
		if (path === '/api/notifications') {
			return route.fulfill({ json: { notifications: [], unread_count: 0 } });
		}
		if (path === '/api/workspaces/test/issues/ENG-1' || path === '/api/workspaces/test/issues') {
			if (route.request().method() === 'PATCH') currentIssue = { ...currentIssue, ...route.request().postDataJSON() };
			return route.fulfill({
				json: path.endsWith('/issues')
					? { data: [currentIssue], total_count: 1, page: 1, has_more: false }
					: currentIssue
			});
		}
		return route.fulfill({ status: 404, json: { error: { message: `Unhandled ${path}` } } });
	});
}

test.describe('AI rework', () => {
	test('issue page exposes a persistent Improve with AI button that reworks the description', async ({ page }) => {
		const reworkRequests: string[] = [];
		await mockWorkspace(page, { aiConfigured: true, reworkRequests });

		await page.goto('/test/issue/ENG-1');

		const improve = page.getByRole('button', { name: 'Improve with AI', exact: true }).first();
		await expect(improve).toBeVisible();
		await expect(improve).toHaveAttribute('title', 'Improve with AI');
		await expect(improve).toHaveCSS('opacity', '1');
		await expect(improve.locator('..')).toHaveCSS('opacity', '1');

		await improve.click();

		await expect.poll(() => reworkRequests.length).toBe(1);
		await expect(page.locator('.rich-editor').first()).toContainText('Expanded by AI');
		const description = page.locator('.rich-editor .tiptap').first();
		await page.locator('.rich-editor .tiptap').last().click();
		await expect(description).toHaveText('Expanded by AI');
		await description.click();
		await page.keyboard.press('Control+z');
		await expect(description).toHaveText('Short description');
	});

	test('clicking Improve with AI on an unconfigured workspace shows a setup hint instead of a generic failure', async ({
		page
	}) => {
		const reworkRequests: string[] = [];
		await mockWorkspace(page, { aiConfigured: false, reworkRequests });

		await page.goto('/test/issue/ENG-1');

		const improve = page.getByRole('button', { name: 'Improve with AI', exact: true }).first();
		await expect(improve).toBeVisible();
		// The tooltip points at the configuration path instead of the action.
		await expect(improve).toHaveAttribute('title', /configure/i);

		await improve.click();

		await expect(page.getByText('AI is not configured')).toBeVisible();
		expect(reworkRequests).toHaveLength(0);
	});

	test('generates an empty existing issue description from the issue title', async ({ page }) => {
		const reworkRequests: string[] = [];
		await mockWorkspace(page, { aiConfigured: true, description: '', reworkRequests });
		await page.goto('/test/issue/ENG-1');
		await page.getByRole('button', { name: 'Improve with AI', exact: true }).first().click();
		await expect.poll(() => reworkRequests.length).toBe(1);
		expect(JSON.parse(reworkRequests[0].slice(5))).toEqual({ selected_text: '' });
		await expect(page.locator('.rich-editor .tiptap').first()).toHaveText('Expanded by AI');
	});

	test('reviews AI output containing rich content without removing images, attachments, or mentions', async ({
		page
	}) => {
		await mockWorkspace(page, {
			aiConfigured: true,
			description:
				'<p>Short description <img src="/api/public/assets/example.png" alt="Diagram" width="50%"><a data-type="attachment" data-filename="plan.pdf" data-size="1024" href="/api/public/assets/plan.pdf?download=1">plan.pdf</a><span data-type="mention" data-id="u1" data-kind="user" data-label="Test User">@Test User</span></p>'
		});
		await page.goto('/test/issue/ENG-1');
		const description = page.locator('.rich-editor .tiptap').first();
		await expect(description.getByRole('img', { name: 'Diagram', exact: true })).toHaveAttribute('width', '50%');
		await expect(description.locator('[data-type="attachment"]')).toHaveAttribute('data-filename', 'plan.pdf');
		await expect(description.locator('[data-type="attachment"]')).toHaveAttribute('data-size', '1024');
		await expect(description.locator('[data-type="attachment"]')).toHaveAttribute(
			'href',
			'/api/public/assets/plan.pdf?download=1'
		);
		await expect(description.locator('[data-type="mention"]')).toHaveAttribute('data-id', 'u1');
		const original = await description.innerHTML();
		await page.getByRole('button', { name: 'Improve with AI', exact: true }).first().click();
		const suggestion = page.getByRole('region', { name: 'AI suggestion' });
		await expect(suggestion).toContainText('images, attachments, or mentions');
		await expect(suggestion).toContainText('Expanded by AI');
		expect(await description.innerHTML()).toBe(original);
		await suggestion.getByRole('button', { name: 'Insert suggestion below' }).click();
		await expect(description).toContainText('Expanded by AI');
		await expect(description.getByRole('img', { name: 'Diagram', exact: true })).toHaveAttribute(
			'src',
			'/api/public/assets/example.png'
		);
		await expect(description.getByRole('img', { name: 'Diagram', exact: true })).toHaveAttribute('width', '50%');
		await expect(description.locator('[data-type="attachment"]')).toHaveAttribute('data-filename', 'plan.pdf');
		await expect(description.locator('[data-type="attachment"]')).toHaveAttribute('data-size', '1024');
		await expect(description.locator('[data-type="attachment"]')).toHaveAttribute(
			'href',
			'/api/public/assets/plan.pdf?download=1'
		);
		await expect(description.locator('[data-type="mention"]')).toHaveAttribute('data-id', 'u1');
		await page.keyboard.press('Control+z');
		expect(await description.innerHTML()).toBe(original);
	});

	test('keeps edits made while a selected-text AI request is pending and permits discarding the stale suggestion', async ({
		page
	}) => {
		let completeRequest!: () => void;
		const pending = new Promise<void>((resolve) => {
			completeRequest = resolve;
		});
		const reworkRequests: string[] = [];
		await mockWorkspace(page, {
			aiConfigured: true,
			reworkRequests,
			expand: async (route) => {
				await pending;
				await route.fulfill({ json: { description: '<p>Expanded by AI</p>' } });
			}
		});
		await page.goto('/test/issue/ENG-1');
		const description = page.locator('.rich-editor .tiptap').first();
		await description.click();
		await page.keyboard.press('Control+Home');
		await page.keyboard.press('Control+Shift+ArrowRight');
		await page.getByRole('button', { name: 'Rework with AI', exact: true }).click();
		await expect.poll(() => reworkRequests.length).toBe(1);
		expect(JSON.parse(reworkRequests[0].slice(5)).selected_text).toBe('Short');
		await description.fill('My new local description');
		completeRequest();
		const suggestion = page.getByRole('region', { name: 'AI suggestion' });
		await expect(suggestion).toContainText('Your text changed while AI was working');
		await expect(description).toHaveText('My new local description');
		await suggestion.getByRole('button', { name: 'Discard suggestion' }).click();
		await expect(suggestion).toBeHidden();
		await expect(description).toHaveText('My new local description');
	});

	test('can improve a comment draft using its visible AI button without selecting text', async ({ page }) => {
		const textRequests: ReworkRequest[] = [];
		await mockWorkspace(page, { aiConfigured: true, textRequests });
		await page.goto('/test/issue/ENG-1');
		const comment = page.locator('.rich-editor .tiptap').last();
		await comment.fill('Please review this implementation');
		const editorControls = page.locator('.rich-editor').last().locator('..');
		await editorControls.getByRole('button', { name: 'Improve with AI', exact: true }).click();
		await expect(comment).toHaveText('Rewritten draft by AI');
		await comment.click();
		await page.keyboard.press('Control+z');
		await expect(comment).toHaveText('Please review this implementation');
		expect(textRequests).toEqual([
			{ text: 'Please review this implementation', context: 'Comment on issue ENG-1: Issue with AI' }
		]);
	});

	test('can generate a new issue description from its title using a visible button', async ({ page }) => {
		const textRequests: ReworkRequest[] = [];
		await mockWorkspace(page, { aiConfigured: true, textRequests });
		await page.goto('/test/issue/ENG-1');
		await expect(page.getByRole('button', { name: 'Improve with AI', exact: true }).first()).toBeVisible();
		await page.getByTitle('New issue', { exact: true }).click();
		await page.locator('#create-issue-title').fill('Make screen sharing resilient');
		const dialog = page.getByRole('dialog');
		await dialog.getByRole('button', { name: 'Generate description with AI' }).click();
		await expect(dialog.locator('.rich-editor .tiptap')).toHaveText('Rewritten draft by AI');
		expect(textRequests).toEqual([
			{
				text: 'Write an issue description for: Make screen sharing resilient',
				context:
					'New issue titled: Make screen sharing resilient. No description exists yet. Generate a complete issue description from this title, including background, scope, and acceptance criteria.'
			}
		]);
		await expect(dialog.getByRole('button', { name: 'Improve with AI', exact: true })).toBeVisible();
	});

	test('a provider failure preserves the description and releases the button for retry', async ({ page }) => {
		let attempts = 0;
		await mockWorkspace(page, {
			aiConfigured: true,
			expand: async (route) => {
				attempts++;
				if (attempts === 1) {
					await route.fulfill({
						status: 502,
						json: { error: { code: 'AI_PROVIDER_ERROR', message: 'AI provider is temporarily unavailable' } }
					});
				} else {
					await route.fulfill({ json: { description: '<p>Expanded after retry</p>' } });
				}
			}
		});
		await page.goto('/test/issue/ENG-1');
		const improve = page.getByRole('button', { name: 'Improve with AI', exact: true }).first();
		await improve.click();
		await expect(page.getByText('AI provider is temporarily unavailable', { exact: true })).toBeVisible();
		await expect(page.locator('.rich-editor .tiptap').first()).toHaveText('Short description');
		await expect(improve).toBeEnabled();
		await improve.click();
		await expect(page.locator('.rich-editor .tiptap').first()).toHaveText('Expanded after retry');
	});

	test('empty comment AI action explains how to begin without sending an empty request', async ({ page }) => {
		const textRequests: ReworkRequest[] = [];
		await mockWorkspace(page, { aiConfigured: true, textRequests });
		await page.goto('/test/issue/ENG-1');
		const commentControls = page.locator('.rich-editor').last().locator('..');
		await commentControls.getByRole('button', { name: 'Improve with AI', exact: true }).click();
		await expect(page.getByText('Write some text before improving it with AI.', { exact: true })).toBeVisible();
		expect(textRequests).toEqual([]);
	});
});

for (const remoteDescription of ['<p>Remote newer description</p>', '']) {
	test(`pending AI respects a remote description ${remoteDescription ? 'update' : 'clear'} while the editor stays focused`, async ({
		page
	}) => {
		await page.clock.install();
		let completeRequest!: () => void;
		const pending = new Promise<void>((resolve) => {
			completeRequest = resolve;
		});
		const reworkRequests: string[] = [];
		await mockWorkspace(page, {
			aiConfigured: true,
			reworkRequests,
			expand: async (route) => {
				await pending;
				await route.fulfill({ json: { description: '<p>Obsolete AI suggestion</p>' } });
			}
		});
		await page.goto('/test/issue/ENG-1');
		const description = page.locator('.rich-editor .tiptap').first();
		await description.click();
		await page.keyboard.press('Control+Home');
		await page.keyboard.press('Control+Shift+ArrowRight');
		await page.getByRole('button', { name: 'Rework with AI', exact: true }).click();
		await expect.poll(() => reworkRequests.length).toBe(1);
		const writes: unknown[] = [];
		await page.route('**/api/workspaces/test/issues/ENG-1', async (route) => {
			if (route.request().method() === 'PATCH') writes.push(route.request().postDataJSON());
			await route.fulfill({ json: { ...issue, title: 'Issue updated remotely', description: remoteDescription } });
		});
		await page.evaluate(() =>
			window.dispatchEvent(new CustomEvent('ws:issue-updated', { detail: { identifier: 'ENG-1' } }))
		);
		await expect(page.getByText('Issue updated remotely', { exact: true })).toBeVisible();
		await expect(description).toHaveText('Short description');
		completeRequest();
		const suggestion = page.getByRole('region', { name: 'AI suggestion' });
		await expect(suggestion).toContainText('Your text changed while AI was working');
		await expect(description).toHaveText('Short description');
		await suggestion.getByRole('button', { name: 'Discard suggestion' }).click();
		await expect(description).toHaveText(remoteDescription ? 'Remote newer description' : '');
		await page.clock.runFor(2100);
		expect(writes).toEqual([]);
	});
}

test('changing a new issue title during generation preserves the old-title response for review', async ({ page }) => {
	let completeRequest!: () => void;
	const pending = new Promise<void>((resolve) => {
		completeRequest = resolve;
	});
	const textRequests: ReworkRequest[] = [];
	await mockWorkspace(page, {
		aiConfigured: true,
		textRequests,
		rework: async (route) => {
			await pending;
			await route.fulfill({ json: { text: '<p>Description for the original title</p>' } });
		}
	});
	await page.goto('/test/issue/ENG-1');
	await expect(page.getByRole('button', { name: 'Improve with AI', exact: true }).first()).toBeVisible();
	await page.getByTitle('New issue', { exact: true }).click();
	await page.locator('#create-issue-title').fill('Original title');
	const dialog = page.getByRole('dialog');
	await dialog.getByRole('button', { name: 'Generate description with AI' }).click();
	await expect.poll(() => textRequests.length).toBe(1);
	await page.locator('#create-issue-title').fill('Changed title');
	completeRequest();
	await expect(dialog.getByRole('region', { name: 'AI suggestion' })).toContainText(
		'Description for the original title'
	);
	await expect(dialog.locator('.rich-editor .tiptap')).toHaveText('');
});
