import { expect, test, type Page } from '@playwright/test';
import { readFile } from 'node:fs/promises';

const repositoryId = '00000000-0000-0000-0000-000000000040';
const secondRepositoryId = '00000000-0000-0000-0000-000000000041';
const actionsPath = `/api/workspaces/test/forgejo/repos/${repositoryId}/actions`;

async function mockActions(
	page: Page,
	options: { role?: string; jobUnavailable?: boolean; actionError?: boolean } = {}
) {
	const calls = { lists: [] as URL[], actions: [] as string[], logs: [] as URL[] };
	const completedRun = {
		id: 101,
		name: 'build.yaml',
		title: 'Build application',
		run_number: 42,
		event: 'push',
		status: 'success',
		conclusion: 'success',
		workflow_id: 'build.yaml',
		head_branch: 'main',
		head_sha: 'abcdef0123456789',
		html_url: 'https://git.example.com/TerraScale/ku/actions/42',
		created_at: '2026-09-30T12:00:00Z',
		updated_at: '2026-09-30T12:01:00Z',
		started_at: '2026-09-30T12:00:00Z',
		completed_at: '2026-09-30T12:01:00Z'
	};
	const runningRun = {
		...completedRun,
		id: 102,
		title: 'Deploy preview',
		run_number: 43,
		status: 'running',
		conclusion: '',
		html_url: 'https://evil.example.com/run',
		completed_at: null
	};
	const job = {
		id: 201,
		run_id: 101,
		name: 'Linux checks',
		status: 'success',
		conclusion: 'success',
		attempt: 2
	};
	const workspace = {
		id: '00000000-0000-0000-0000-000000000002',
		name: 'Test Workspace',
		slug: 'test',
		current_user_role: options.role ?? 'owner',
		screen_sharing_enabled: false
	};
	await page.route('https://raw.githubusercontent.com/carbogninalberto/kuayle/main/UI/static/releases.json', (route) =>
		route.fulfill({ json: [] })
	);
	await page.route('**/api/**', async (route) => {
		const url = new URL(route.request().url());
		const path = url.pathname;
		if (path === '/api/auth/me')
			return route.fulfill({
				json: { id: 'user', email: 'test@example.com', name: 'Test User', display_name: 'Test User', avatar_url: null }
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
		if (path === '/api/workspaces/test') return route.fulfill({ json: workspace });
		if (path === '/api/workspaces') return route.fulfill({ json: [workspace] });
		if (path === '/api/notifications') return route.fulfill({ json: { notifications: [], unread_count: 0 } });
		if (path === '/api/workspaces/test/forgejo/status')
			return route.fulfill({
				json: {
					configured: true,
					connected: true,
					global_config: true,
					instance_url: 'https://git.example.com',
					repos: [
						{
							id: repositoryId,
							forgejo_repo_id: 1,
							full_name: 'TerraScale/ku',
							instance_url: 'https://git.example.com',
							default_branch: 'main',
							is_active: true
						},
						{
							id: secondRepositoryId,
							forgejo_repo_id: 2,
							full_name: 'TerraScale/sdk',
							instance_url: 'https://git.example.com',
							default_branch: 'main',
							is_active: true
						}
					]
				}
			});
		if (path === `${actionsPath}/runs`) {
			calls.lists.push(url);
			let data = [completedRun, runningRun];
			if (url.searchParams.get('status')) data = data.filter((run) => run.status === url.searchParams.get('status'));
			if (url.searchParams.get('ref'))
				data = data.filter((run) => `refs/heads/${run.head_branch}` === url.searchParams.get('ref'));
			return route.fulfill({
				json: {
					data,
					total_count: data.length,
					page: Number(url.searchParams.get('page')),
					per_page: 20,
					has_more: false
				}
			});
		}
		if (path === `/api/workspaces/test/forgejo/repos/${secondRepositoryId}/actions/runs`)
			return route.fulfill({ json: { data: [], total_count: 0, page: 1, per_page: 20, has_more: false } });
		if (path === `${actionsPath}/runs/101`) return route.fulfill({ json: completedRun });
		if (path === `${actionsPath}/runs/102`) return route.fulfill({ json: runningRun });
		if (path === `${actionsPath}/runs/101/jobs` || path === `${actionsPath}/runs/102/jobs`)
			return route.fulfill({ json: { data: [job], total_count: 1, page: 1, per_page: 100, has_more: false } });
		if (path === `${actionsPath}/runs/101/jobs/201`)
			return options.jobUnavailable
				? route.fulfill({ status: 404, json: { error: { message: 'Job details are unavailable' } } })
				: route.fulfill({ json: job });
		if (path === `${actionsPath}/runs/101/jobs/201/logs`) {
			calls.logs.push(url);
			return route.fulfill({
				json: {
					content: '\u001b[32mAll tests passed\u001b[0m\n<script>plain text</script>\n',
					truncated: true,
					max_bytes: 2097152
				}
			});
		}
		if (route.request().method() === 'POST' && path.startsWith(actionsPath)) {
			calls.actions.push(path);
			if (!path.endsWith('/cancel'))
				return route.fulfill({ status: 404, json: { error: { message: 'Unsupported action' } } });
			if (options.actionError)
				return route.fulfill({ status: 502, json: { error: { message: 'Forgejo is temporarily unavailable' } } });
			const run = path.includes('/runs/101/') ? completedRun : runningRun;
			run.status = 'cancelled';
			run.conclusion = 'cancelled';
			run.updated_at = new Date().toISOString();
			return route.fulfill({ status: 202, json: { status: 'accepted' } });
		}
		return route.fulfill({ json: [] });
	});
	return { calls, completedRun, job };
}

test('reads native job logs, hands reruns to Forgejo, and cancels active runs as an owner', async ({ page }) => {
	const { calls } = await mockActions(page);
	const pageErrors: Error[] = [];
	page.on('pageerror', (error) => pageErrors.push(error));
	await page.goto('/test/actions');
	await page.getByRole('button', { name: /Build application/ }).click();
	await expect(page.getByRole('link', { name: 'Open in Forgejo' })).toHaveAttribute(
		'href',
		'https://git.example.com/TerraScale/ku/actions/42'
	);
	await page.getByRole('button', { name: /Linux checks/ }).click();
	await expect(page.getByRole('link', { name: 'View workflow steps in Forgejo', exact: true })).toHaveAttribute(
		'href',
		'https://git.example.com/TerraScale/ku/actions/42'
	);
	await expect(page.locator('pre')).toHaveText('All tests passed\n<script>plain text</script>\n');
	await expect(page.getByText('Logs are limited to the first 2 MB.', { exact: true })).toBeVisible();
	expect(calls.logs[0].searchParams.get('attempt')).toBe('2');
	const downloaded = page.waitForEvent('download');
	await page.getByRole('button', { name: 'Download logs' }).click();
	expect((await downloaded).suggestedFilename()).toBe('forgejo-run-101-job-201-attempt-2.log');
	const handoff = page.getByRole('link', { name: 'Rerun in Forgejo', exact: true });
	await expect(handoff).toHaveAttribute('href', 'https://git.example.com/TerraScale/ku/actions/42');
	await expect(handoff).toHaveAttribute('target', '_blank');
	await expect(handoff).toHaveAttribute('rel', 'noopener noreferrer');
	await expect(
		page.getByText(
			'Sign in to Forgejo on the run page, then use its rerun controls. Your Forgejo repository permissions apply.',
			{ exact: true }
		)
	).toBeVisible();
	await page
		.context()
		.route('https://git.example.com/TerraScale/ku/actions/42', (route) =>
			route.fulfill({ contentType: 'text/html', body: '<h1>Forgejo workflow run</h1>' })
		);
	const opened = page.waitForEvent('popup');
	await handoff.click();
	const forgejoPage = await opened;
	await expect(forgejoPage).toHaveURL('https://git.example.com/TerraScale/ku/actions/42');
	await expect(forgejoPage.getByRole('heading', { name: 'Forgejo workflow run' })).toBeVisible();
	await forgejoPage.close();
	expect(calls.actions).toEqual([]);
	await page.getByRole('button', { name: /Deploy preview/ }).click();
	await expect(page.getByRole('link', { name: 'Open in Forgejo' })).toHaveCount(0);
	await page.getByRole('button', { name: 'Cancel run', exact: true }).click();
	await page.getByRole('alertdialog').getByRole('button', { name: 'Cancel run', exact: true }).click();
	await expect(page.getByRole('button', { name: 'Rerun in Forgejo', exact: true })).toBeDisabled();
	await expect(page.getByRole('link', { name: 'Rerun in Forgejo', exact: true })).toHaveCount(0);
	expect(calls.actions).toEqual([`${actionsPath}/runs/102/cancel`]);
	await expect(page.getByRole('link', { name: 'Open in Forgejo' })).toHaveCount(0);
	expect(pageErrors).toEqual([]);
});

for (const role of ['admin', 'member', 'guest']) {
	test(`allows ${role} to inspect workflows without mutation controls`, async ({ page }) => {
		const { calls } = await mockActions(page, { role });
		await page.goto('/test/actions');
		await page.getByRole('button', { name: /Build application/ }).click();
		await expect(page.getByRole('link', { name: 'Rerun in Forgejo', exact: true })).toHaveCount(0);
		await page.getByRole('button', { name: /Deploy preview/ }).click();
		await expect(page.getByRole('button', { name: 'Cancel run', exact: true })).toHaveCount(0);
		expect(calls.actions).toEqual([]);
	});
}

test('preserves readable logs when job refresh fails and reports cancellation errors', async ({ page }) => {
	const { calls } = await mockActions(page, { jobUnavailable: true, actionError: true });
	await page.goto('/test/actions');
	await page.getByRole('button', { name: /Build application/ }).click();
	await page.getByRole('button', { name: /Linux checks/ }).click();
	await expect(page.locator('pre')).toContainText('All tests passed');
	await expect(page.getByText('Could not refresh this job.', { exact: true })).toBeVisible();
	await page.getByRole('button', { name: /Deploy preview/ }).click();
	await page.getByRole('button', { name: 'Cancel run', exact: true }).click();
	await page.getByRole('alertdialog').getByRole('button', { name: 'Cancel run', exact: true }).click();
	await expect(page.getByText('Forgejo is temporarily unavailable', { exact: true })).toBeVisible();
	await expect(page.getByRole('alertdialog')).not.toBeVisible();
	await expect(
		page.getByRole('region', { name: 'Jobs', exact: true }).getByRole('button', { name: 'Cancel run', exact: true })
	).toBeEnabled();
	expect(calls.actions).toHaveLength(1);
});

test('sends status and branch filters and discards delayed details when switching repository', async ({ page }) => {
	const { calls } = await mockActions(page);
	await page.goto('/test/actions');
	await page.getByLabel('Status', { exact: true }).selectOption('success');
	await expect(page.getByRole('button', { name: /Deploy preview/ })).toHaveCount(0);
	await page.getByLabel('Branch', { exact: true }).fill('feature/empty');
	await page.getByLabel('Branch', { exact: true }).press('Enter');
	await expect(page.getByText('No workflow runs', { exact: true })).toBeVisible();
	expect(calls.lists.at(-1)?.searchParams.get('status')).toBe('success');
	expect(calls.lists.at(-1)?.searchParams.get('ref')).toBe('refs/heads/feature/empty');
	await page.getByLabel('Branch', { exact: true }).fill('');
	await page.getByLabel('Branch', { exact: true }).press('Enter');
	await expect(page.getByRole('button', { name: /Build application/ })).toBeVisible();
	let release!: () => void;
	let markStarted!: () => void;
	const released = new Promise<void>((resolve) => {
		release = resolve;
	});
	const started = new Promise<void>((resolve) => {
		markStarted = resolve;
	});
	await page.route(`**${actionsPath}/runs/101`, async (route) => {
		markStarted();
		await released;
		await route.fulfill({ json: { id: 101, title: 'Stale repository run', status: 'success' } }).catch(() => undefined);
	});
	await page.getByRole('button', { name: /Build application/ }).click();
	await started;
	await page.getByLabel('Repository', { exact: true }).selectOption(secondRepositoryId);
	release();
	await expect(page.getByText('No workflow runs', { exact: true })).toBeVisible();
	await expect(page.getByText('Select a run to inspect its jobs and logs.', { exact: true })).toBeVisible();
	await expect(page.getByText('Stale repository run', { exact: true })).toHaveCount(0);
	await expect(page.getByRole('link', { name: 'Rerun in Forgejo', exact: true })).toHaveCount(0);
});

test('polls active runs only while visible and leaves completed filters idle', async ({ page }) => {
	const { calls } = await mockActions(page);
	await page.clock.install();
	await page.goto('/test/actions');
	await expect(page.getByRole('button', { name: /Build application/ })).toBeVisible();
	await page.clock.fastForward(15001);
	await expect.poll(() => calls.lists.length).toBe(2);
	await page.evaluate(() => {
		Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'hidden' });
		document.dispatchEvent(new Event('visibilitychange'));
	});
	await page.clock.fastForward(30001);
	expect(calls.lists).toHaveLength(2);
	await page.evaluate(() => {
		Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' });
		document.dispatchEvent(new Event('visibilitychange'));
	});
	await expect.poll(() => calls.lists.length).toBe(3);
	await page.getByLabel('Status', { exact: true }).selectOption('success');
	await expect(page.getByRole('button', { name: /Deploy preview/ })).toHaveCount(0);
	const count = calls.lists.length;
	await page.clock.fastForward(45001);
	expect(calls.lists).toHaveLength(count);
});

test('coalesces Forgejo issue updates, ignores other issues, and waits for clipboard success', async ({ page }) => {
	await mockActions(page);
	let statusRequests = 0;
	let activityRequests = 0;
	const issue = {
		id: 'issue-one',
		team_id: '',
		identifier: 'ENG-1',
		title: 'Forgejo linked issue',
		description: '<p>Issue description</p>',
		status: 'todo',
		status_info: { id: 'todo', name: 'Todo', category: 'unstarted', color: '#888888' },
		priority: 0,
		labels: [],
		assignees: [],
		created_at: '2026-09-30T00:00:00Z',
		updated_at: '2026-09-30T00:00:00Z'
	};
	await page.route('**/api/workspaces/test/forgejo/status', (route) => {
		statusRequests++;
		return route.fulfill({
			json: {
				configured: true,
				connected: true,
				global_config: true,
				instance_url: 'https://git.example.com',
				repos: []
			}
		});
	});
	await page.route('**/api/workspaces/test/issues/ENG-1**', (route) => {
		const path = new URL(route.request().url()).pathname;
		if (path.endsWith('/forgejo')) {
			activityRequests++;
			return route.fulfill({
				json: {
					pull_requests: [],
					commits: [],
					branches: [
						{
							id: 'branch-one',
							name: 'ENG-1-fix',
							html_url: 'https://git.example.com/TerraScale/ku/src/branch/ENG-1-fix',
							repo_full_name: 'TerraScale/ku',
							created_at: '2026-09-30T00:00:00Z'
						}
					]
				}
			});
		}
		return route.fulfill({ json: path.endsWith('/ENG-1') ? issue : [] });
	});
	await page.goto('/test/issue/ENG-1');
	await expect(page.getByText('ENG-1-fix', { exact: true })).toBeVisible();
	expect(statusRequests).toBe(1);
	expect(activityRequests).toBe(1);
	await page.clock.install();
	await page.evaluate(() => {
		for (const type of ['ws:forgejo:pr_updated', 'ws:forgejo:branch_created', 'ws:forgejo:commit_pushed']) {
			window.dispatchEvent(new CustomEvent(type, { detail: { issue_id: 'another-issue' } }));
		}
	});
	await page.clock.fastForward(250);
	expect(activityRequests).toBe(1);
	await page.evaluate(() => {
		for (const type of ['ws:forgejo:pr_updated', 'ws:forgejo:branch_created', 'ws:forgejo:commit_pushed']) {
			window.dispatchEvent(new CustomEvent(type, { detail: { issue_id: 'issue-one' } }));
		}
	});
	await page.clock.fastForward(250);
	await expect.poll(() => activityRequests).toBe(2);
	expect(statusRequests).toBe(1);
	await page.evaluate(() => {
		Object.defineProperty(navigator, 'clipboard', {
			configurable: true,
			value: { writeText: () => Promise.reject(new Error('Denied')) }
		});
	});
	await page.getByRole('button', { name: 'Copy branch name' }).click();
	await expect(page.getByText('Could not copy the branch name.', { exact: true })).toBeVisible();
	await expect(page.getByText('Branch name copied', { exact: true })).toHaveCount(0);
	await page.evaluate(() => {
		Object.defineProperty(navigator, 'clipboard', {
			configurable: true,
			value: { writeText: () => Promise.resolve() }
		});
	});
	await page.getByRole('button', { name: 'Copy branch name' }).click();
	await expect(page.getByText('Branch name copied', { exact: true })).toBeVisible();
});

test('reads the current job attempt when a rerun advances after the jobs list response', async ({ page }) => {
	const { calls, job } = await mockActions(page);
	await page.route(`**${actionsPath}/runs/101/jobs/201`, (route) => route.fulfill({ json: { ...job, attempt: 3 } }));
	await page.goto('/test/actions');
	await page.getByRole('button', { name: /Build application/ }).click();
	await page.getByRole('button', { name: /Linux checks/ }).click();
	await expect(page.locator('pre')).toContainText('All tests passed');
	expect(calls.logs.map((url) => url.searchParams.get('attempt'))).toEqual(['2', '3']);
	const downloaded = page.waitForEvent('download');
	await page.getByRole('button', { name: 'Download logs' }).click();
	expect((await downloaded).suggestedFilename()).toBe('forgejo-run-101-job-201-attempt-3.log');
});

test('keeps an earlier attempt unavailable for download while newer job logs are loading', async ({ page }) => {
	const { job } = await mockActions(page);
	await page.goto('/test/actions');
	await page.getByRole('button', { name: /Build application/ }).click();
	await page.getByRole('button', { name: /Linux checks/ }).click();
	await expect(page.locator('pre')).toContainText('All tests passed');
	await expect(page.getByRole('button', { name: 'Download logs' })).toBeEnabled();
	let release!: () => void;
	let markStarted!: () => void;
	const released = new Promise<void>((resolve) => {
		release = resolve;
	});
	const started = new Promise<void>((resolve) => {
		markStarted = resolve;
	});
	await page.route(`**${actionsPath}/runs/101/jobs/201/logs?attempt=3`, async (route) => {
		markStarted();
		await released;
		await route.fulfill({ json: { content: 'attempt 3 logs\n', truncated: false, max_bytes: 2097152 } });
	});
	job.attempt = 3;
	await page.getByRole('button', { name: 'Refresh', exact: true }).click();
	await started;
	try {
		await expect(page.getByRole('button', { name: 'Download logs' })).toBeDisabled();
		await expect(page.locator('pre')).toHaveCount(0);
	} finally {
		release();
	}
	await expect(page.locator('pre')).toHaveText('attempt 3 logs\n');
	const downloaded = page.waitForEvent('download');
	await page.getByRole('button', { name: 'Download logs' }).click();
	const download = await downloaded;
	expect(download.suggestedFilename()).toBe('forgejo-run-101-job-201-attempt-3.log');
	const path = await download.path();
	expect(path).not.toBeNull();
	expect(await readFile(path!, 'utf8')).toBe('attempt 3 logs\n');
});
