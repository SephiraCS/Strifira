import { expect, test, type Page, type Route } from '@playwright/test';

const boxId = '00000000-0000-0000-0000-000000000070';
const jobId = '00000000-0000-0000-0000-000000000071';
const issue = {
	id: '00000000-0000-0000-0000-000000000072',
	identifier: 'ENG-1',
	title: 'Implement the requested feature',
	description: '<p>Use the linked repository.</p>',
	team_id: '00000000-0000-0000-0000-000000000010',
	status: 'todo',
	priority: 0,
	labels: [],
	assignees: [],
	created_at: '2026-09-30T00:00:00Z',
	updated_at: '2026-09-30T00:00:00Z'
};
const box = {
	id: boxId,
	workspace_id: 'w1',
	created_by_user_id: 'u1',
	name: 'ku',
	enabled: true,
	online: true,
	hostname: 'ku-dev',
	os: 'linux',
	arch: 'amd64',
	version: '0.3.0',
	providers: [{ id: 'codex', version: '0.159.2' }],
	token_prefix: 'kub_12345678',
	last_seen_at: '2026-09-30T00:00:00Z',
	created_at: '2026-09-30T00:00:00Z',
	updated_at: '2026-09-30T00:00:00Z'
};
const job = {
	id: jobId,
	workspace_id: 'w1',
	box_id: boxId,
	requested_by_user_id: 'u1',
	issue_id: issue.id,
	issue_identifier: issue.identifier,
	provider: 'codex',
	prompt: 'Implement the feature',
	forgejo_repo_id: 'repo1',
	repository_url: 'https://forgejo.example/owner/ku.git',
	repository_full_name: 'owner/ku',
	base_branch: 'main',
	working_branch: 'ku/ENG-1',
	test_command: ['go', 'test', './...'],
	acceptance_criteria: ['Tests pass'],
	max_runtime_seconds: 3600,
	status: 'running',
	cancel_requested: false,
	logs_bytes: 0,
	logs_truncated: false,
	created_at: '2026-09-30T00:00:00Z'
};

async function reply(route: Route, json: unknown, status = 200) {
	await route.fulfill({ status, json });
	return true;
}

async function mockWorkspace(page: Page, handler: (route: Route, url: URL) => Promise<boolean>, role = 'member') {
	await page.route('https://raw.githubusercontent.com/**/releases.json', (route) => route.fulfill({ json: [] }));
	await page.route('**/api/**', async (route) => {
		const url = new URL(route.request().url());
		if (await handler(route, url)) return;
		const path = url.pathname;
		if (path === '/api/auth/me') return reply(route, { id: 'u1', name: 'Test User', email: 'test@example.com' });
		if (path === '/api/preferences')
			return reply(route, {
				font_size: 'default',
				pointer_cursors: true,
				theme_mode: 'dark',
				light_theme: 'light',
				dark_theme: 'dark',
				workflow_sort_mode: 'default',
				workflow_sort_order: [],
				team_workflow_sort_overrides: {},
				issues_group_by: 'status'
			});
		if (path === '/api/workspaces') return reply(route, [{ id: 'w1', slug: 'test', name: 'Test Workspace' }]);
		if (path === '/api/workspaces/test')
			return reply(route, { id: 'w1', slug: 'test', name: 'Test Workspace', current_user_role: role });
		if (path === '/api/notifications') return reply(route, { notifications: [], unread_count: 0 });
		if (path === '/api/workspaces/test/teams')
			return reply(route, [{ id: issue.team_id, key: 'ENG', name: 'Engineering', color: '#6366f1' }]);
		if (path === '/api/workspaces/test/issues/ENG-1') return reply(route, issue);
		if (path === '/api/workspaces/test/issues')
			return reply(route, { data: [issue], total_count: 1, has_more: false, page: 1 });
		if (path === '/api/workspaces/test/dev-boxes') return reply(route, [box]);
		if (path === '/api/workspaces/test/dev-box-jobs')
			return reply(route, { data: [], total_count: 0, has_more: false, page: 1 });
		if (path === '/api/workspaces/test/ai-settings')
			return reply(route, { provider: 'openai_compatible', base_url: '', model: '', has_api_key: false });
		return reply(route, []);
	});
}

test('registers a box with a token shown once and replaces it without browser persistence', async ({ page }) => {
	const token = `kub_${'a'.repeat(64)}`;
	const replacement = `kub_${'b'.repeat(64)}`;
	const requests: unknown[] = [];
	let registered = false;
	await mockWorkspace(page, async (route, url) => {
		if (url.pathname === '/api/workspaces/test/dev-boxes' && route.request().method() === 'POST') {
			requests.push(route.request().postDataJSON());
			registered = true;
			return reply(route, { box, token });
		}
		if (url.pathname === '/api/workspaces/test/dev-boxes') return reply(route, registered ? [box] : []);
		if (url.pathname.endsWith('/rotate-token')) return reply(route, { box, token: replacement });
		return false;
	});
	await page.goto('/test/dev-boxes');
	await page.getByRole('button', { name: 'Register dev box', exact: true }).click();
	let dialog = page.getByRole('dialog');
	await dialog.getByLabel('Name', { exact: true }).fill('ku');
	await dialog.getByRole('button', { name: 'Register dev box', exact: true }).click();
	await expect(dialog.getByLabel('Worker token')).toHaveValue(token);
	const config = JSON.parse(await dialog.getByLabel('Worker configuration').inputValue());
	expect(config.server_url).toBe(new URL(page.url()).origin);
	expect(config.token_file).toContain('/worker.token');
	expect(JSON.stringify(config)).not.toContain(token);
	expect(requests).toEqual([{ name: 'ku' }]);
	await page.evaluate(() =>
		Object.defineProperty(navigator, 'clipboard', {
			configurable: true,
			value: {
				writeText: async () => {
					throw new Error('denied');
				}
			}
		})
	);
	await dialog.getByRole('button', { name: 'Copy token' }).click();
	await expect(page.getByText('Could not copy. Select the text and copy it manually.')).toBeVisible();
	await dialog.getByRole('button', { name: 'Done' }).click();
	await expect(page.getByLabel('Worker token')).toHaveCount(0);
	await page.getByRole('button', { name: 'Replace token', exact: true }).click();
	dialog = page.getByRole('dialog');
	await dialog.getByRole('button', { name: 'Replace token', exact: true }).click();
	await expect(dialog.getByLabel('Worker token')).toHaveValue(replacement);
	await dialog.getByRole('button', { name: 'Done' }).click();
	const storage = await page.evaluate(() =>
		JSON.stringify({ local: { ...localStorage }, session: { ...sessionStorage } })
	);
	expect(storage).not.toContain(token);
	expect(storage).not.toContain(replacement);
});

test('delegates to an online installed provider with independent test arguments and review-only output', async ({
	page
}) => {
	const requests: unknown[] = [];
	await mockWorkspace(page, async (route, url) => {
		if (url.pathname === '/api/workspaces/test/dev-boxes')
			return reply(route, [
				{ ...box, id: 'offline', name: 'Offline box', online: false },
				{ ...box, id: 'empty', name: 'No provider box', providers: [] },
				box
			]);
		if (url.pathname === `/api/workspaces/test/dev-boxes/${boxId}/jobs`) {
			requests.push(route.request().postDataJSON());
			return reply(route, job, 201);
		}
		if (url.pathname === `/api/workspaces/test/dev-box-jobs/${jobId}`) return reply(route, job);
		if (url.pathname.endsWith('/logs')) return reply(route, []);
		return false;
	});
	await page.goto('/test/issue/ENG-1');
	await page.getByRole('button', { name: 'Issue actions' }).click();
	await page.getByRole('button', { name: 'Delegate to dev box' }).click();
	const dialog = page.getByRole('dialog');
	await expect(dialog.getByLabel('Dev box', { exact: true })).toHaveText('ku');
	await expect(dialog.getByLabel('Coding provider', { exact: true })).toHaveText('Codex');
	await dialog.getByLabel('Dev box', { exact: true }).click();
	await expect(page.getByRole('option', { name: 'Offline box' })).toHaveCount(0);
	await expect(page.getByRole('option', { name: 'No provider box' })).toHaveCount(0);
	await page.getByRole('option', { name: 'ku', exact: true }).click();
	await dialog.getByLabel('Test command').fill('go test ./...');
	await dialog.getByRole('button', { name: 'Delegate issue' }).click();
	await expect(dialog.getByRole('alert')).toHaveText('Enter the test command as a nonempty JSON list of strings.');
	expect(requests).toEqual([]);
	await dialog.getByLabel('Test command').fill('["go", "test", "./..."]');
	await dialog.getByLabel('Additional instructions').fill('Keep the API clear');
	await dialog.getByLabel('Acceptance criteria').fill('Meaningful tests pass\nChanges are reviewable');
	await dialog.getByLabel('Maximum runtime (minutes)').fill('15');
	await dialog.getByRole('button', { name: 'Delegate issue' }).click();
	await expect(page).toHaveURL(`/test/dev-box-jobs/${jobId}`);
	expect(requests).toEqual([
		{
			issue_id: issue.id,
			provider: 'codex',
			prompt: 'Keep the API clear',
			acceptance_criteria: ['Meaningful tests pass', 'Changes are reviewable'],
			test_command: ['go', 'test', './...'],
			max_runtime_seconds: 900
		}
	]);
});

test('polls incremental logs, cancels work and renders result patches as plain text', async ({ page }) => {
	let cancelled = false;
	let cancelRequests = 0;
	const cursors: number[] = [];
	const patch = '<script>window.patchExecuted = true</script>\n+implemented';
	await mockWorkspace(page, async (route, url) => {
		if (url.pathname === `/api/workspaces/test/dev-box-jobs/${jobId}/cancel`) {
			cancelRequests++;
			cancelled = true;
			return reply(route, {
				...job,
				status: 'cancelled',
				cancel_requested: true,
				result: {
					status: 'cancelled',
					exit_code: 130,
					summary: 'Stopped by request',
					changed_files: ['feature.go'],
					branch: job.working_branch,
					patch,
					test_status: 'not_run',
					tests_run: [],
					risk_notes: ['Review before applying']
				}
			});
		}
		if (url.pathname === `/api/workspaces/test/dev-box-jobs/${jobId}`)
			return reply(
				route,
				cancelled
					? {
							...job,
							status: 'cancelled',
							cancel_requested: true,
							result: {
								status: 'cancelled',
								summary: 'Stopped by request',
								changed_files: ['feature.go'],
								patch,
								test_status: 'not_run',
								tests_run: [],
								risk_notes: []
							}
						}
					: job
			);
		if (url.pathname.endsWith('/logs')) {
			const cursor = Number(url.searchParams.get('after_id'));
			cursors.push(cursor);
			return reply(
				route,
				cursor === 0
					? [
							{
								id: 1,
								job_id: jobId,
								sequence: 1,
								stream: 'stdout',
								content: 'Coding started\n',
								created_at: job.created_at
							}
						]
					: cursor === 1
						? [
								{
									id: 2,
									job_id: jobId,
									sequence: 2,
									stream: 'system',
									content: 'Checking results\n',
									created_at: job.created_at
								}
							]
						: []
			);
		}
		return false;
	});
	await page.goto(`/test/dev-box-jobs/${jobId}`);
	await expect(page.getByTestId('dev-box-logs')).toContainText('Coding started');
	await expect(page.getByTestId('dev-box-logs')).toContainText('Checking results');
	expect(cursors).toContain(1);
	await page.getByRole('button', { name: 'Cancel work' }).click();
	await expect(page.getByText('Cancelled', { exact: true })).toBeVisible();
	await expect(page.getByTestId('dev-box-patch')).toHaveText(patch);
	await expect(page.getByRole('button', { name: 'Cancel work' })).toHaveCount(0);
	expect(cancelRequests).toBe(1);
	expect(await page.evaluate(() => (window as unknown as { patchExecuted?: boolean }).patchExecuted)).toBeUndefined();
	const downloadPromise = page.waitForEvent('download');
	await page.getByRole('button', { name: 'Download patch' }).click();
	const download = await downloadPromise;
	expect(download.suggestedFilename()).toBe(`ENG-1-${jobId}.patch`);
});

test('guest navigation omits registration and issue delegation controls', async ({ page }) => {
	await mockWorkspace(page, async () => false, 'guest');
	await page.goto('/test/issue/ENG-1');
	await expect(page.getByRole('link', { name: 'Dev boxes', exact: true })).toHaveCount(0);
	await page.getByRole('button', { name: 'Issue actions' }).click();
	await expect(page.getByRole('button', { name: 'Delegate to dev box' })).toHaveCount(0);
});

test('discarded registration responses cannot reopen a token after the dialog closes', async ({ page }) => {
	let start!: () => void;
	let release!: () => void;
	let finish!: () => void;
	const started = new Promise<void>((resolve) => {
		start = resolve;
	});
	const released = new Promise<void>((resolve) => {
		release = resolve;
	});
	const finished = new Promise<void>((resolve) => {
		finish = resolve;
	});
	const token = `kub_${'c'.repeat(64)}`;
	await mockWorkspace(page, async (route, url) => {
		if (url.pathname === '/api/workspaces/test/dev-boxes' && route.request().method() === 'POST') {
			start();
			await released;
			await reply(route, { box, token });
			finish();
			return true;
		}
		return false;
	});
	await page.goto('/test/dev-boxes');
	await page.getByRole('button', { name: 'Register dev box', exact: true }).click();
	const dialog = page.getByRole('dialog');
	await dialog.getByLabel('Name', { exact: true }).fill('second');
	await dialog.getByRole('button', { name: 'Register dev box', exact: true }).click();
	await started;
	await dialog.getByRole('button', { name: 'Close', exact: true }).click();
	await expect(page.getByRole('dialog')).toHaveCount(0);
	release();
	await finished;
	await page.getByRole('button', { name: 'Register dev box', exact: true }).click();
	await expect(page.getByLabel('Worker token')).toHaveCount(0);
	await expect(page.getByRole('dialog').getByLabel('Name', { exact: true })).toHaveValue('');
});
