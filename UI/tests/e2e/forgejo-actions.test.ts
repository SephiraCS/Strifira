import { expect, test, type Page } from '@playwright/test';

const repositoryId = '00000000-0000-0000-0000-000000000040';
const actionsPath = `/api/workspaces/test/forgejo/repos/${repositoryId}/actions`;

const issue = {
	id: 'issue-one',
	team_id: '',
	identifier: 'ENG-1',
	title: 'Forgejo linked issue',
	description: '<p>Issue description</p>',
	status: 'todo',
	status_info: {
		id: 'todo',
		name: 'Todo',
		category: 'unstarted',
		color: '#888888'
	},
	priority: 0,
	labels: [],
	assignees: [],
	created_at: '2026-09-30T00:00:00Z',
	updated_at: '2026-09-30T00:00:00Z'
};

const pullRequest = {
	id: 'pr-one',
	number: 7,
	title: 'ENG-1 fix the build',
	state: 'open',
	checks_green: false,
	author_login: 'dev',
	author_avatar_url: '',
	html_url: 'https://git.example.com/TerraScale/ku/pulls/7',
	head_branch: 'ENG-1-fix',
	head_sha: 'abcdef0123456789',
	base_branch: 'main',
	additions: 3,
	deletions: 1,
	repo_full_name: 'TerraScale/ku',
	repo_id: repositoryId,
	merged_at: null,
	created_at: '2026-09-30T00:00:00Z',
	updated_at: '2026-09-30T00:00:00Z'
};

async function mockActions(
	page: Page,
	options: {
		role?: string;
		pullRequests?: unknown[];
		finished?: boolean;
	} = {}
) {
	const calls = {
		lists: [] as URL[],
		runPages: [] as URL[],
		jobs: [] as string[],
		actions: [] as string[],
		logs: [] as URL[]
	};
	const completedRun = {
		id: 101,
		name: 'build.yaml',
		title: 'Build application',
		run_number: 42,
		event: 'pull_request',
		status: 'success',
		conclusion: 'success',
		workflow_id: 'build.yaml',
		head_branch: 'ENG-1-fix',
		head_sha: 'abcdef0123456789',
		html_url: 'https://git.example.com/TerraScale/ku/actions/runs/42',
		created_at: '2026-09-30T12:00:00Z',
		updated_at: '2026-09-30T12:01:00Z',
		started_at: '2026-09-30T12:00:00Z',
		completed_at: '2026-09-30T12:01:00Z' as string | null
	};
	const runningRun = {
		...completedRun,
		id: 102,
		title: 'Deploy preview',
		run_number: 43,
		status: 'running',
		conclusion: '',
		html_url: 'https://evil.example.com/run',
		completed_at: null as string | null
	};
	if (options.finished)
		Object.assign(runningRun, { status: 'success', conclusion: 'success', completed_at: '2026-09-30T12:03:00Z' });
	const failNextLogs = { count: 0 };
	const olderRun = { ...completedRun, id: 100, title: 'Lint sources', run_number: 41, html_url: '' };
	const job = {
		id: 201,
		run_id: 101,
		name: 'Linux checks',
		status: 'success',
		conclusion: 'success',
		attempt: 2
	};
	const deployJob = {
		...job,
		id: 301,
		run_id: 102,
		name: 'Deploy preview job',
		status: 'running',
		conclusion: '',
		attempt: 1
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
				json: {
					id: 'user',
					email: 'test@example.com',
					name: 'Test User',
					display_name: 'Test User',
					avatar_url: null
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
						}
					]
				}
			});
		if (path === '/api/workspaces/test/issues/ENG-1/forgejo')
			return route.fulfill({
				json: { pull_requests: options.pullRequests ?? [pullRequest], branches: [], commits: [] }
			});
		if (path === '/api/workspaces/test/issues/ENG-1') return route.fulfill({ json: issue });
		if (path === `${actionsPath}/runs`) {
			// The newest page holds both current runs; older history is on page 2.
			const second = url.searchParams.get('page') === '2';
			if (second) calls.runPages.push(url);
			else calls.lists.push(url);
			const pullRuns = url.searchParams.get('ref') === 'refs/pull/7/head';
			const data = pullRuns ? (second ? [olderRun] : [completedRun, runningRun]) : [];
			return route.fulfill({
				json: { data, total_count: pullRuns ? 3 : 0, page: second ? 2 : 1, per_page: 20, has_more: pullRuns && !second }
			});
		}
		if (path === `${actionsPath}/runs/102/jobs`) {
			// The running workflow spans two pages of jobs.
			calls.jobs.push(path);
			const second = url.searchParams.get('page') === '2';
			return route.fulfill({
				json: {
					data: [second ? deployJob : job],
					total_count: 2,
					page: second ? 2 : 1,
					per_page: 100,
					has_more: !second
				}
			});
		}
		if (path === `${actionsPath}/runs/102/jobs/301/logs`) {
			calls.logs.push(url);
			if (failNextLogs.count > 0) {
				failNextLogs.count--;
				return route.fulfill({ status: 502, json: { error: 'Forgejo is unavailable' } });
			}
			const done = runningRun.status !== 'running';
			return route.fulfill({
				json: { content: done ? 'Deploy finished\n' : 'Deploying\n', truncated: false, max_bytes: 2097152 }
			});
		}
		if (path === `${actionsPath}/runs/101/jobs`) {
			calls.jobs.push(path);
			return route.fulfill({
				json: { data: [job], total_count: 1, page: 1, per_page: 100, has_more: false }
			});
		}
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
			runningRun.status = 'cancelled';
			runningRun.conclusion = 'cancelled';
			runningRun.completed_at = '2026-09-30T12:02:00Z';
			return route.fulfill({ status: 202, json: { status: 'accepted' } });
		}
		return route.fulfill({ json: [] });
	});
	return { calls, runningRun, failNextLogs };
}

async function openIssue(page: Page) {
	await page.goto('/test/issue/ENG-1');
	await page.getByRole('button', { name: 'Issue actions', exact: true }).waitFor({ state: 'visible' });
	const actions = page.getByRole('region', { name: 'Forgejo Actions', exact: true });
	await actions.waitFor({ state: 'visible' });
	return actions;
}

test('shows pull request workflow runs, jobs, and plain logs on the issue and cancels as an owner', async ({
	page
}) => {
	const { calls } = await mockActions(page);
	const pageErrors: Error[] = [];
	page.on('pageerror', (error) => pageErrors.push(error));
	const actions = await openIssue(page);
	await expect(actions.getByRole('button', { name: /Build application/ })).toContainText('#42');
	await expect(actions.getByRole('button', { name: /Build application/ })).toContainText('1m 0s');
	expect(calls.lists[0].searchParams.get('ref')).toBe('refs/pull/7/head');
	// Older history loads only on request.
	expect(calls.lists[0].searchParams.get('per_page')).toBe('20');
	expect(calls.runPages).toEqual([]);
	await actions.getByRole('button', { name: 'Load older runs', exact: true }).click();
	await expect(actions.getByRole('button', { name: /Lint sources/ })).toBeVisible();
	await expect(actions.getByRole('button', { name: 'Load older runs', exact: true })).toHaveCount(0);
	// Only the run with a trusted Forgejo URL links out.
	const links = actions.getByRole('link', { name: 'Open in Forgejo' });
	await expect(links).toHaveCount(1);
	await expect(links).toHaveAttribute('href', 'https://git.example.com/TerraScale/ku/actions/runs/42');
	await expect(links).toHaveAttribute('rel', 'noopener noreferrer');

	await actions.getByRole('button', { name: /Build application/ }).click();
	await expect(actions.getByRole('list', { name: 'Jobs' })).toBeVisible();
	await expect(actions.getByRole('button', { name: 'Cancel run', exact: true })).toHaveCount(0);
	await actions.getByRole('button', { name: /Linux checks/ }).click();
	await expect(actions.locator('pre')).toHaveText('All tests passed\n<script>plain text</script>\n');
	await expect(actions.getByText('Logs are limited to the first 2 MB.', { exact: true })).toBeVisible();
	expect(calls.logs[0].searchParams.get('attempt')).toBe('2');

	await actions.getByRole('button', { name: /Deploy preview/ }).click();
	await expect(actions.locator('pre')).toHaveCount(0);
	await actions.getByRole('button', { name: 'Cancel run', exact: true }).click();
	await page.getByRole('alertdialog').getByRole('button', { name: 'Cancel run', exact: true }).click();
	await expect.poll(() => calls.actions).toEqual([`${actionsPath}/runs/102/cancel`]);
	await expect(actions.getByRole('button', { name: 'Cancel run', exact: true })).toHaveCount(0);
	expect(pageErrors).toEqual([]);
});

for (const role of ['admin', 'member', 'guest']) {
	test(`lets ${role} inspect issue workflow runs without cancel controls`, async ({ page }) => {
		const { calls } = await mockActions(page, { role });
		const actions = await openIssue(page);
		await actions.getByRole('button', { name: /Deploy preview/ }).click();
		await expect(actions.getByRole('list', { name: 'Jobs' })).toBeVisible();
		await expect(actions.getByRole('button', { name: 'Cancel run', exact: true })).toHaveCount(0);
		expect(calls.actions).toEqual([]);
	});
}

test('skips pull requests without a linked repository id', async ({ page }) => {
	const { calls } = await mockActions(page, { pullRequests: [{ ...pullRequest, repo_id: '' }] });
	await page.goto('/test/issue/ENG-1');
	await page.getByText('ENG-1 fix the build', { exact: true }).waitFor();
	await expect(page.getByRole('region', { name: 'Forgejo Actions', exact: true })).toHaveCount(0);
	expect(calls.lists).toEqual([]);
});

test('polls active runs fast, then keeps a slow discovery poll while the PR is open', async ({ page }) => {
	const { calls, runningRun } = await mockActions(page);
	await page.clock.install();
	const actions = await openIssue(page);
	await expect(actions.getByRole('button', { name: /Deploy preview/ })).toBeVisible();
	await expect.poll(() => calls.lists.length).toBe(1);
	await page.clock.fastForward(10001);
	await expect.poll(() => calls.lists.length).toBe(2);
	runningRun.status = 'success';
	runningRun.conclusion = 'success';
	runningRun.completed_at = '2026-09-30T12:03:00Z';
	await page.clock.fastForward(10001);
	await expect.poll(() => calls.lists.length).toBe(3);
	await page.clock.fastForward(20000);
	expect(calls.lists).toHaveLength(3);
	await page.clock.fastForward(10001);
	await expect.poll(() => calls.lists.length).toBe(4);
});

test('stops polling a merged pull request once its runs finish', async ({ page }) => {
	const { calls } = await mockActions(page, { pullRequests: [{ ...pullRequest, state: 'merged' }], finished: true });
	await page.clock.install();
	const actions = await openIssue(page);
	await expect(actions.getByRole('button', { name: /Deploy preview/ })).toBeVisible();
	await page.clock.fastForward(60001);
	expect(calls.lists).toHaveLength(1);
});

test('loads every job page and refreshes open logs on the tick that sees a run finish', async ({ page }) => {
	const { calls, runningRun } = await mockActions(page);
	await page.clock.install();
	const actions = await openIssue(page);
	await actions.getByRole('button', { name: /Deploy preview/ }).click();
	await expect(actions.getByRole('button', { name: /Linux checks/ })).toBeVisible();
	await actions.getByRole('button', { name: /Deploy preview job/ }).click();
	await expect(actions.locator('pre')).toHaveText('Deploying\n');
	runningRun.status = 'success';
	runningRun.conclusion = 'success';
	runningRun.completed_at = '2026-09-30T12:03:00Z';
	await page.clock.fastForward(10001);
	await expect(actions.locator('pre')).toHaveText('Deploy finished\n');
	const lists = calls.lists.length;
	await page.clock.fastForward(20000);
	expect(calls.lists).toHaveLength(lists);
	// The discovery poll refreshes runs but not the finished run's jobs or logs.
	const jobs = calls.jobs.length;
	const logs = calls.logs.length;
	await page.clock.fastForward(10001);
	await expect.poll(() => calls.lists.length).toBe(lists + 1);
	expect(calls.jobs).toHaveLength(jobs);
	expect(calls.logs).toHaveLength(logs);
});

test('retries the final logs refresh after a transient failure when a run finishes', async ({ page }) => {
	const { calls, runningRun, failNextLogs } = await mockActions(page, {
		pullRequests: [{ ...pullRequest, state: 'merged' }]
	});
	await page.clock.install();
	const actions = await openIssue(page);
	await actions.getByRole('button', { name: /Deploy preview/ }).click();
	await actions.getByRole('button', { name: /Deploy preview job/ }).click();
	await expect(actions.locator('pre')).toHaveText('Deploying\n');
	runningRun.status = 'success';
	runningRun.conclusion = 'success';
	runningRun.completed_at = '2026-09-30T12:03:00Z';
	failNextLogs.count = 1;
	const logs = calls.logs.length;
	await page.clock.fastForward(10001);
	await expect.poll(() => calls.logs.length).toBe(logs + 1);
	await expect(actions.locator('pre')).toHaveText('Deploying\n');
	// The merged PR has no active runs, but the failed refresh is retried.
	await page.clock.fastForward(30001);
	await expect(actions.locator('pre')).toHaveText('Deploy finished\n');
	const settled = calls.lists.length;
	await page.clock.fastForward(60001);
	expect(calls.lists).toHaveLength(settled);
});

test('removes Actions from navigation and redirects the old workspace page to issues', async ({ page }) => {
	await mockActions(page);
	await page.goto('/test/actions');
	await expect(page).toHaveURL(/\/test\/my-issues$/);
	await expect(page.locator('a[href="/test/actions"]')).toHaveCount(0);
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
		status_info: {
			id: 'todo',
			name: 'Todo',
			category: 'unstarted',
			color: '#888888'
		},
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
	await page.getByRole('button', { name: 'Issue actions', exact: true }).waitFor({ state: 'visible' });
	await page.getByText('ENG-1-fix', { exact: true }).waitFor();
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
