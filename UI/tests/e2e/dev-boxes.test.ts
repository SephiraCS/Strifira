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

for (const succeeds of [true, false]) {
	test(`pending delegation blocks dismissal and ${succeeds ? 'opens its only created job' : 'restores the form after failure'}`, async ({
		page
	}) => {
		const started = deferred();
		const response = deferred();
		const requests: unknown[] = [];
		await mockWorkspace(page, async (route, url) => {
			if (url.pathname === `/api/workspaces/test/dev-boxes/${boxId}/jobs`) {
				requests.push(route.request().postDataJSON());
				started.resolve();
				await response.promise;
				return succeeds ? reply(route, job, 201) : reply(route, { message: 'Temporary delegation failure' }, 503);
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
		await dialog.getByLabel('Test command').fill('["go", "test", "./..."]');
		await dialog.getByRole('button', { name: 'Delegate issue', exact: true }).click();
		await started.promise;
		await expect(dialog.getByRole('button', { name: 'Cancel', exact: true })).toBeDisabled();
		await expect(dialog.getByRole('button', { name: 'Close', exact: true })).toHaveCount(0);
		await expect(dialog.getByRole('button', { name: 'Delegating…', exact: true })).toBeDisabled();
		await page.keyboard.press('Escape');
		await expect(dialog).toBeVisible();
		await page.mouse.click(2, 2);
		await expect(dialog).toBeVisible();
		await dialog.locator('form').dispatchEvent('submit');
		expect(requests).toHaveLength(1);
		response.resolve();
		if (succeeds) {
			await expect(page).toHaveURL(`/test/dev-box-jobs/${jobId}`);
			await expect(dialog).toHaveCount(0);
		} else {
			await expect(dialog.getByRole('button', { name: 'Delegate issue', exact: true })).toBeEnabled();
			await expect(dialog.getByLabel('Test command')).toHaveValue('["go", "test", "./..."]');
			await expect(dialog.getByRole('button', { name: 'Cancel', exact: true })).toBeEnabled();
			await expect(dialog.getByRole('button', { name: 'Close', exact: true })).toBeVisible();
			await page.keyboard.press('Escape');
			await expect(dialog).toHaveCount(0);
		}
		expect(requests).toHaveLength(1);
	});
}

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

for (const { role, sysadmin, visible } of [
	{ role: 'member', sysadmin: false, visible: false },
	{ role: 'member', sysadmin: true, visible: true },
	{ role: 'guest', sysadmin: true, visible: false }
]) {
	test(`demo dev-box navigation respects the ${sysadmin ? 'sysadmin' : 'ordinary'} ${role} guard in sidebar and settings`, async ({
		page
	}) => {
		await page.route('**/_app/env.js', (route) =>
			route.fulfill({ contentType: 'application/javascript', body: 'export const env = { PUBLIC_DEMO_MODE: "true" };' })
		);
		await mockWorkspace(
			page,
			async (route, url) => {
				if (url.pathname === '/api/auth/me') {
					return reply(route, {
						id: 'u1',
						name: 'Test User',
						email: 'test@example.com',
						is_sysadmin: sysadmin
					});
				}
				return false;
			},
			role
		);
		await page.goto('/test/issue/ENG-1');
		await page.getByRole('button', { name: 'Issue actions' }).waitFor({ state: 'visible' });
		const link = page.getByRole('link', { name: 'Dev boxes', exact: true });
		await expect(link).toHaveCount(visible ? 1 : 0);
		if (visible) await expect(link).toHaveAttribute('href', '/test/dev-boxes');
		await page.goto('/test/settings/profile');
		await page.getByRole('heading', { name: 'Profile', exact: true }).waitFor({ state: 'visible' });
		await expect(link).toHaveCount(visible ? 1 : 0);
		if (visible) await expect(link).toHaveAttribute('href', '/test/dev-boxes');
	});
}

test('command validation rejects oversized UTF-8 arguments before delegation and accepts the exact boundary', async ({
	page
}) => {
	const requests: unknown[] = [];
	await mockWorkspace(page, async (route, url) => {
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
	const command = dialog.getByLabel('Test command');
	await command.fill(JSON.stringify(['runner', 'é'.repeat(2049)]));
	await dialog.getByRole('button', { name: 'Delegate issue', exact: true }).click();
	await expect(dialog.getByRole('alert')).toHaveText('Enter the test command as a nonempty JSON list of strings.');
	await expect(command).toHaveAttribute('aria-invalid', 'true');
	expect(requests).toEqual([]);
	const accepted = ['runner', 'é'.repeat(2048)];
	await command.fill(JSON.stringify(accepted));
	await dialog.getByRole('button', { name: 'Delegate issue', exact: true }).click();
	await expect(page).toHaveURL(`/test/dev-box-jobs/${jobId}`);
	expect(requests).toHaveLength(1);
	expect((requests[0] as { test_command: string[] }).test_command).toEqual(accepted);
});

for (const operation of ['register', 'rotate'] as const) {
	test(`pending ${operation} keeps its dialog open until the one-time token arrives`, async ({ page }) => {
		const started = deferred();
		const response = deferred();
		const token = `kub_${'c'.repeat(64)}`;
		await mockWorkspace(page, async (route, url) => {
			const expectedPath =
				operation === 'register'
					? '/api/workspaces/test/dev-boxes'
					: `/api/workspaces/test/dev-boxes/${boxId}/rotate-token`;
			if (url.pathname === expectedPath && route.request().method() === 'POST') {
				started.resolve();
				await response.promise;
				return reply(route, { box, token });
			}
			return false;
		});
		await page.goto('/test/dev-boxes');
		const action = operation === 'register' ? 'Register dev box' : 'Replace token';
		await page.getByRole('button', { name: action, exact: true }).click();
		const dialog = page.getByRole('dialog');
		if (operation === 'register') await dialog.getByLabel('Name', { exact: true }).fill('second');
		await dialog.getByRole('button', { name: action, exact: true }).click();
		await started.promise;
		await expect(dialog.getByRole('button', { name: 'Cancel', exact: true })).toBeDisabled();
		await expect(dialog.getByRole('button', { name: 'Close', exact: true })).toHaveCount(0);
		await page.keyboard.press('Escape');
		await expect(dialog).toBeVisible();
		await page.mouse.click(2, 2);
		await expect(dialog).toBeVisible();
		response.resolve();
		await expect(dialog.getByLabel('Worker token')).toHaveValue(token);
		await dialog.getByRole('button', { name: 'Done' }).click();
		await expect(dialog).toHaveCount(0);
		await expect(page.getByLabel('Worker token')).toHaveCount(0);
		await page.getByRole('main').getByRole('button', { name: action, exact: true }).click();
		await expect(dialog).toBeVisible();
		await expect(dialog.getByLabel('Worker token')).toHaveCount(0);
	});
}

test('criteria validation explains count, UTF-8 size and null characters before sending a job', async ({ page }) => {
	const requests: unknown[] = [];
	await mockWorkspace(page, async (route, url) => {
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
	await dialog.getByLabel('Test command').fill('["go", "test", "./..."]');
	const criteria = dialog.getByLabel('Acceptance criteria');
	await criteria.fill(Array.from({ length: 51 }, (_, index) => `Requirement ${index}`).join('\n'));
	await dialog.getByRole('button', { name: 'Delegate issue' }).click();
	await expect(dialog.getByRole('alert')).toHaveText(
		'Use at most 50 nonempty requirements. Remove or combine some lines.'
	);
	await expect(criteria).toHaveAttribute('aria-invalid', 'true');
	await criteria.fill('é'.repeat(1000) + 'a');
	await expect(criteria).toHaveAttribute('aria-invalid', 'false');
	await dialog.getByRole('button', { name: 'Delegate issue' }).click();
	await expect(dialog.getByRole('alert')).toContainText('Requirement 1 exceeds 2,000 UTF-8 bytes.');
	await criteria.fill('Must not contain\0NUL');
	await dialog.getByRole('button', { name: 'Delegate issue' }).click();
	await expect(dialog.getByRole('alert')).toContainText('Requirement 1 contains a null character.');
	expect(requests).toEqual([]);
	const accepted = Array.from({ length: 50 }, () => 'é'.repeat(1000));
	await criteria.fill(' \n' + accepted.map((line) => ` ${line} `).join('\n\n'));
	await dialog.getByRole('button', { name: 'Delegate issue' }).click();
	await expect(page).toHaveURL(`/test/dev-box-jobs/${jobId}`);
	expect(requests).toHaveLength(1);
	expect((requests[0] as { acceptance_criteria: string[] }).acceptance_criteria).toEqual(accepted);
});

test('completed jobs stop retrying a failed log page and retain manual recovery', async ({ page }) => {
	await page.clock.install();
	let logsRequests = 0;
	await mockWorkspace(page, async (route, url) => {
		if (url.pathname === `/api/workspaces/test/dev-box-jobs/${jobId}`)
			return reply(route, { ...job, status: 'succeeded' });
		if (url.pathname.endsWith('/logs')) {
			logsRequests++;
			if (logsRequests === 1) return reply(route, logPage(0));
			if (logsRequests === 2) return reply(route, { message: 'Temporary log failure' }, 503);
			return reply(route, [{ ...logPage(500)[0], content: 'Recovered final output\n' }]);
		}
		return false;
	});
	await page.goto(`/test/dev-box-jobs/${jobId}`);
	const logsAlert = page.getByRole('region', { name: 'Live logs', exact: true }).getByRole('alert');
	await expect(logsAlert).toHaveText('Could not load the remaining output. Select Refresh to try again.');
	expect(logsRequests).toBe(2);
	await page.clock.runFor(10_000);
	expect(logsRequests).toBe(2);
	await page.getByRole('button', { name: 'Refresh', exact: true }).click();
	await expect(page.getByTestId('dev-box-logs')).toContainText('Recovered final output');
	await expect(logsAlert).toHaveCount(0);
	expect(logsRequests).toBe(3);
	await page.clock.runFor(10_000);
	expect(logsRequests).toBe(3);
});

test('completed jobs drain successful log pages past one poll batch', async ({ page }) => {
	const cursors: number[] = [];
	await mockWorkspace(page, async (route, url) => {
		if (url.pathname === `/api/workspaces/test/dev-box-jobs/${jobId}`)
			return reply(route, { ...job, status: 'succeeded' });
		if (url.pathname.endsWith('/logs')) {
			const cursor = Number(url.searchParams.get('after_id'));
			cursors.push(cursor);
			return reply(route, cursor < 3000 ? logPage(cursor) : []);
		}
		return false;
	});
	await page.goto(`/test/dev-box-jobs/${jobId}`);
	await page.getByRole('region', { name: 'Live logs', exact: true }).waitFor({ state: 'visible' });
	await expect(page.getByTestId('dev-box-logs')).toContainText('Output 3000');
	await expect.poll(() => cursors.length).toBe(7);
	expect(cursors).toEqual([0, 500, 1000, 1500, 2000, 2500, 3000]);
	await expect(page.getByTestId('dev-box-logs')).not.toContainText('Output 1\n');
});

test('active jobs still retry failed progress requests', async ({ page }) => {
	await page.clock.install();
	let logsRequests = 0;
	await mockWorkspace(page, async (route, url) => {
		if (url.pathname === `/api/workspaces/test/dev-box-jobs/${jobId}`) return reply(route, job);
		if (url.pathname.endsWith('/logs')) {
			logsRequests++;
			return logsRequests === 1
				? reply(route, { message: 'Temporary log failure' }, 503)
				: reply(route, [{ ...logPage(0)[0], content: 'Progress recovered\n' }]);
		}
		return false;
	});
	await page.goto(`/test/dev-box-jobs/${jobId}`);
	const logsAlert = page.getByRole('region', { name: 'Live logs', exact: true }).getByRole('alert');
	await expect(logsAlert).toHaveText('Could not refresh progress. Retrying…');
	await page.clock.runFor(2000);
	await expect(page.getByTestId('dev-box-logs')).toContainText('Progress recovered');
	await expect(logsAlert).toHaveCount(0);
	expect(logsRequests).toBe(2);
});

test('box polling pauses while hidden after a mutation and refreshes heartbeat state on return', async ({ page }) => {
	await page.clock.install();
	const mutationStarted = deferred();
	const mutationResponse = deferred();
	let boxesRequests = 0;
	let jobsRequests = 0;
	let enabled = true;
	await mockWorkspace(page, async (route, url) => {
		if (url.pathname === `/api/workspaces/test/dev-boxes/${boxId}` && route.request().method() === 'PATCH') {
			mutationStarted.resolve();
			await mutationResponse.promise;
			enabled = false;
			return reply(route, { ...box, enabled });
		}
		if (url.pathname === '/api/workspaces/test/dev-boxes') {
			boxesRequests++;
			return reply(route, [{ ...box, enabled }]);
		}
		if (url.pathname === '/api/workspaces/test/dev-box-jobs') {
			jobsRequests++;
			return reply(route, { data: [], has_more: false, page: 1 });
		}
		return false;
	});
	await page.goto('/test/dev-boxes');
	await expect(page.getByRole('article', { name: 'ku', exact: true })).toBeVisible();
	await page.getByRole('switch', { name: 'Enable ku' }).click();
	await mutationStarted.promise;
	await setVisibility(page, 'hidden');
	mutationResponse.resolve();
	await expect(page.getByRole('switch', { name: 'Enable ku' })).toHaveAttribute('aria-checked', 'false');
	await page.clock.runFor(15_000);
	expect({ boxesRequests, jobsRequests }).toEqual({ boxesRequests: 1, jobsRequests: 1 });
	await setVisibility(page, 'visible');
	await expect.poll(() => boxesRequests).toBe(2);
	await expect.poll(() => jobsRequests).toBe(2);
	await page.clock.runFor(5000);
	await expect.poll(() => boxesRequests).toBe(3);
	await expect.poll(() => jobsRequests).toBe(3);
});

test('job polling pauses an in-flight page drain while hidden and resumes immediately', async ({ page }) => {
	await page.clock.install();
	const logsStarted = deferred();
	const logsResponse = deferred();
	const cursors: number[] = [];
	let jobRequests = 0;
	await mockWorkspace(page, async (route, url) => {
		if (url.pathname === `/api/workspaces/test/dev-box-jobs/${jobId}`) {
			jobRequests++;
			return reply(route, job);
		}
		if (url.pathname.endsWith('/logs')) {
			const cursor = Number(url.searchParams.get('after_id'));
			cursors.push(cursor);
			if (cursor === 0) {
				logsStarted.resolve();
				await logsResponse.promise;
				return reply(route, logPage(0));
			}
			return reply(route, [{ ...logPage(cursor)[0], content: 'Visible progress\n' }]);
		}
		return false;
	});
	await page.goto(`/test/dev-box-jobs/${jobId}`);
	await logsStarted.promise;
	await setVisibility(page, 'hidden');
	logsResponse.resolve();
	await expect(page.getByTestId('dev-box-logs')).toContainText('Output 500');
	await page.clock.runFor(10_000);
	expect(jobRequests).toBe(1);
	expect(cursors).toEqual([0]);
	await setVisibility(page, 'visible');
	await expect(page.getByTestId('dev-box-logs')).toContainText('Visible progress');
	expect(jobRequests).toBe(2);
	expect(cursors).toEqual([0, 500]);
});

function deferred() {
	let resolve!: () => void;
	const promise = new Promise<void>((finish) => (resolve = finish));
	return { promise, resolve };
}

function logPage(after: number) {
	return Array.from({ length: 500 }, (_, index) => ({
		id: after + index + 1,
		job_id: jobId,
		sequence: after + index + 1,
		stream: 'stdout',
		content: `Output ${after + index + 1}\n`,
		created_at: job.created_at
	}));
}

async function setVisibility(page: Page, visibility: 'hidden' | 'visible') {
	await page.evaluate((value) => {
		Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => value });
		document.dispatchEvent(new Event('visibilitychange'));
	}, visibility);
}
