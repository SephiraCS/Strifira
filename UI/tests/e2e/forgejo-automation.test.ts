import { expect, test, type Page, type Route } from '@playwright/test';

const engineeringId = '00000000-0000-0000-0000-000000000010';
const operationsId = '00000000-0000-0000-0000-000000000011';
type Rule = {
	event: string;
	target_status: string;
	target_status_id: string | null;
	target_assignee_id: string | null;
	assign_forgejo_user: boolean;
	is_active: boolean;
};

function issueStatusButton(page: Page, name: string) {
	return page.getByRole('button', { name, exact: true }).and(page.locator('button:not([data-popover-trigger])'));
}

async function mockForgejo(
	page: Page,
	options: {
		role?: string;
		statusErrorOnce?: boolean;
		rulesErrorOnce?: boolean;
		initialRules?: Partial<Rule>[];
		updateRule?: (route: Route, rule: Rule) => Promise<void>;
	} = {}
) {
	const rules: Rule[] = ['branch_created', 'pr_opened', 'pr_checks_green', 'pr_merged'].map((event) => ({
		event,
		target_status: '',
		target_status_id: null,
		target_assignee_id: null,
		assign_forgejo_user: false,
		is_active: false,
		...options.initialRules?.find((rule) => rule.event === event)
	}));
	const teams = [
		{
			id: engineeringId,
			name: 'Engineering',
			key: 'ENG',
			icon: 'layers',
			color: '#6366f1'
		},
		{
			id: operationsId,
			name: 'Operations',
			key: 'OPS',
			icon: 'layers',
			color: '#888888'
		}
	];
	const statuses = (teamId: string) => [
		{
			id: `${teamId}-todo`,
			team_id: teamId,
			name: 'Todo',
			slug: 'todo',
			category: 'unstarted',
			position: 0,
			color: '#888888',
			is_default: true
		},
		{
			id: `${teamId}-review`,
			team_id: teamId,
			name: teamId === engineeringId ? 'Code review' : 'Awaiting review',
			slug: 'review',
			category: 'started',
			position: 1,
			color: '#8b5cf6',
			is_default: false
		},
		...(teamId === engineeringId
			? [
					{
						id: `${teamId}-testing`,
						team_id: teamId,
						name: 'Integration testing',
						slug: 'integration-testing',
						category: 'started',
						position: 2,
						color: '#6366f1',
						is_default: false
					}
				]
			: [])
	];
	const currentIssue = {
		id: 'issue-one',
		team_id: engineeringId,
		identifier: 'ENG-1',
		title: 'Forgejo linked issue',
		description: '<p>Keep this description</p>',
		status: 'todo',
		status_id: `${engineeringId}-todo`,
		priority: 0,
		labels: [],
		assignees: [],
		created_at: '2026-09-30T00:00:00Z',
		updated_at: '2026-09-30T00:00:00Z'
	};
	const calls = {
		updates: [] as Rule[],
		status: 0,
		rules: 0,
		issueReads: 0,
		issueWrites: 0
	};
	let seeded = false;
	await page.route('https://raw.githubusercontent.com/**/releases.json', (route) => route.fulfill({ json: [] }));
	await page.route('**/api/**', async (route) => {
		const url = new URL(route.request().url());
		const path = url.pathname;
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
			name: 'Test Workspace',
			slug: 'test',
			current_user_role: options.role ?? 'owner',
			screen_sharing_enabled: false
		};
		if (path === '/api/workspaces') return route.fulfill({ json: [workspace] });
		if (path === '/api/workspaces/test') return route.fulfill({ json: workspace });
		if (path === '/api/notifications') return route.fulfill({ json: { notifications: [], unread_count: 0 } });
		if (path === '/api/workspaces/test/teams') return route.fulfill({ json: teams });
		if (path === '/api/workspaces/test/members')
			return route.fulfill({
				json: [
					{
						user_id: 'user-one',
						name: 'Test User',
						email: 'test@example.com',
						role: 'owner'
					}
				]
			});
		if (path.endsWith('/statuses')) {
			const teamId = path.split('/').at(-2)!;
			return route.fulfill({
				json: seeded ? statuses(teamId) : statuses(teamId).slice(0, 1)
			});
		}
		if (path === '/api/workspaces/test/forgejo/status') {
			calls.status++;
			if (options.statusErrorOnce && calls.status === 1)
				return route.fulfill({
					status: 503,
					json: {
						error: { message: 'Forgejo connection could not be checked' }
					}
				});
			seeded = true;
			return route.fulfill({
				json: {
					configured: true,
					connected: true,
					global_config: true,
					instance_url: 'https://git.example.com',
					account: { login: 'mario' },
					repos: []
				}
			});
		}
		if (path === '/api/workspaces/test/forgejo/auto-transitions') {
			if (route.request().method() === 'PATCH') {
				const rule = route.request().postDataJSON().transitions[0] as Rule;
				calls.updates.push(rule);
				if (options.updateRule) return options.updateRule(route, rule);
				Object.assign(
					rules.find((item) => item.event === rule.event)!,
					rule
				);
				return route.fulfill({ json: { status: 'updated' } });
			}
			calls.rules++;
			if (options.rulesErrorOnce && calls.rules === 1)
				return route.fulfill({
					status: 503,
					json: { error: { message: 'Automation rules could not be loaded' } }
				});
			return route.fulfill({ json: rules });
		}
		if (path === '/api/workspaces/test/forgejo/user-mappings') return route.fulfill({ json: [] });
		if (path === '/api/workspaces/test/issues/ENG-1' || path === '/api/workspaces/test/issues/LEG-1') {
			if (route.request().method() === 'PATCH') {
				calls.issueWrites++;
				Object.assign(currentIssue, route.request().postDataJSON());
			} else calls.issueReads++;
			return route.fulfill({
				json: {
					...currentIssue,
					status_info: statuses(engineeringId).find((status) => status.slug === currentIssue.status)
				}
			});
		}
		if (path === '/api/workspaces/test/issues')
			return route.fulfill({
				json: {
					data: [currentIssue],
					total_count: 1,
					page: 1,
					has_more: false
				}
			});
		if (path === '/api/workspaces/test/issues/ENG-1/forgejo')
			return route.fulfill({
				json: { pull_requests: [], branches: [], commits: [] }
			});
		return route.fulfill({ json: [] });
	});
	return { rules, currentIssue, calls };
}

async function openForgejoIssue(page: Page, url = '/test/issue/ENG-1') {
	await page.goto(url);
	await page.getByRole('button', { name: 'Issue actions', exact: true }).waitFor();
}

async function openForgejoSettings(page: Page, statusError = false) {
	await page.goto('/test/settings/forgejo');
	const ready = statusError
		? page.getByText('Forgejo connection could not be checked', { exact: true })
		: page.locator('[data-forgejo-rule]').first();
	await ready.waitFor({ state: 'visible' });
}

test('configures an inactive blank rule before enabling it and resolves shared custom status slugs', async ({
	page
}) => {
	const { calls } = await mockForgejo(page);
	await openForgejoSettings(page);
	const rule = page.locator('[data-forgejo-rule="pr_opened"]');
	await expect(rule.getByRole('switch')).toBeDisabled();
	await expect(rule.getByLabel('Target status', { exact: true })).toBeEnabled();
	await rule.getByLabel('Target status', { exact: true }).selectOption('review');
	await expect(rule.getByRole('switch')).toBeEnabled();
	await expect(rule).toContainText('Status changes apply to: Engineering, Operations.');
	expect(calls.updates[0]).toMatchObject({
		event: 'pr_opened',
		target_status: 'review',
		target_status_id: null,
		is_active: false
	});
	await rule.getByRole('switch').click();
	await expect(rule.getByRole('switch')).toBeChecked();
	await expect(rule.getByLabel('Target status', { exact: true })).toBeEnabled();
	expect(calls.updates[1]).toMatchObject({
		event: 'pr_opened',
		target_status: 'review',
		target_status_id: null,
		is_active: true
	});
	await rule.getByLabel('Target status', { exact: true }).selectOption('integration-testing');
	await expect(rule).toContainText('Status changes apply to: Engineering.');
	await expect(rule).toContainText(
		'These teams do not have this status and will keep their current status: Operations.'
	);
});

test('serializes edits to one rule and keeps rejected changes visible and retryable', async ({ page }) => {
	let release!: () => void;
	const released = new Promise<void>((resolve) => {
		release = resolve;
	});
	let request = 0;
	const { calls } = await mockForgejo(page, {
		updateRule: async (route) => {
			request++;
			if (request === 1) {
				await released;
				return route.fulfill({
					status: 400,
					json: {
						error: { message: 'The selected workflow status is unavailable' }
					}
				});
			}
			return route.fulfill({ json: { status: 'updated' } });
		}
	});
	await openForgejoSettings(page);
	const rule = page.locator('[data-forgejo-rule="branch_created"]');
	await rule.getByLabel('Target status', { exact: true }).selectOption('review');
	await expect(rule.getByLabel('Target status', { exact: true })).toBeDisabled();
	await expect(rule.getByLabel('Assign to', { exact: true })).toBeDisabled();
	await expect(rule.getByRole('switch')).toBeDisabled();
	await expect(rule).toContainText('Saving rule…');
	await expect.poll(() => calls.updates).toHaveLength(1);
	release();
	await expect(rule.getByRole('alert')).toHaveText('The selected workflow status is unavailable');
	await expect(rule.getByLabel('Target status', { exact: true })).toHaveValue('');
	await expect(rule.getByLabel('Target status', { exact: true })).toBeEnabled();
	await rule.getByLabel('Target status', { exact: true }).selectOption('review');
	await expect(rule.getByRole('alert')).toHaveCount(0);
	await expect(rule.getByRole('switch')).toBeEnabled();
	expect(calls.updates).toHaveLength(2);
});

test('shows invalid saved targets and lets the owner disable the rule', async ({ page }) => {
	const { calls } = await mockForgejo(page, {
		initialRules: [{ event: 'pr_merged', target_status: 'removed-status', is_active: true }]
	});
	await openForgejoSettings(page);
	const rule = page.locator('[data-forgejo-rule="pr_merged"]');
	await expect(rule).toContainText('This status is no longer available. Choose a team workflow status.');
	await rule.getByRole('switch').click();
	await expect.poll(() => calls.updates).toHaveLength(1);
	await expect(rule.getByRole('switch')).not.toBeChecked();
	expect(calls.updates[0]).toMatchObject({
		event: 'pr_merged',
		target_status: 'removed-status',
		is_active: false
	});
});

for (const role of ['admin', 'member', 'guest']) {
	test(`shows automation settings to ${role} without exposing unavailable write controls`, async ({ page }) => {
		const { calls } = await mockForgejo(page, {
			role,
			initialRules: [{ event: 'pr_opened', target_status: 'review', is_active: true }]
		});
		await openForgejoSettings(page);
		const rule = page.locator('[data-forgejo-rule="pr_opened"]');
		await expect(rule.getByLabel('Target status', { exact: true })).toBeDisabled();
		await expect(rule.getByRole('switch')).toBeDisabled();
		await expect(
			page.getByText('You can view this integration, but only the workspace owner can change it.', { exact: true })
		).toBeVisible();
		await expect(page.getByRole('button', { name: 'Manage', exact: true })).toHaveCount(0);
		expect(calls.updates).toEqual([]);
	});
}

test('retries status and automation load errors without displaying a false disconnected form', async ({ page }) => {
	await mockForgejo(page, { statusErrorOnce: true, rulesErrorOnce: true });
	await openForgejoSettings(page, true);
	await expect(page.getByText('Forgejo connection could not be checked', { exact: true })).toBeVisible();
	await expect(page.getByRole('button', { name: 'Connect', exact: true })).toHaveCount(0);
	await page.getByRole('button', { name: 'Try again', exact: true }).click();
	await expect(page.getByText('Automation rules could not be loaded', { exact: true })).toBeVisible();
	await page.getByRole('button', { name: 'Try again', exact: true }).click();
	await expect(page.locator('[data-forgejo-rule="pr_opened"]')).toBeVisible();
});

test('refreshes an automation status update received immediately after a local title save', async ({ page }) => {
	const { currentIssue, calls } = await mockForgejo(page);
	await openForgejoIssue(page);
	await expect(issueStatusButton(page, 'Todo')).toBeVisible();
	await page.clock.install();
	await page.getByRole('button', { name: 'Forgejo linked issue', exact: true }).click();
	const title = page.locator('input[autofocus]');
	await title.fill('Locally updated title');
	await title.press('Tab');
	await expect(page.getByRole('button', { name: 'Locally updated title', exact: true })).toBeVisible();
	expect(calls.issueWrites).toBe(1);
	const initialReads = calls.issueReads;
	currentIssue.status = 'review';
	currentIssue.status_id = `${engineeringId}-review`;
	await page.evaluate(() =>
		window.dispatchEvent(
			new CustomEvent('ws:issue-updated', {
				detail: { id: 'issue-one', identifier: 'ENG-1', status: 'review' }
			})
		)
	);
	await page.clock.fastForward(500);
	expect(calls.issueReads).toBe(initialReads);
	await page.clock.fastForward(2200);
	await expect(issueStatusButton(page, 'Code review')).toBeVisible();
	await expect(page.getByRole('button', { name: 'Locally updated title', exact: true })).toBeVisible();
	await expect(page.locator('.rich-editor .tiptap').first()).toHaveText('Keep this description');
	expect(calls.issueReads).toBe(initialReads + 1);
});

test('refreshes the current issue after reconnecting and explains unmatched code activity', async ({ page }) => {
	const { currentIssue, calls } = await mockForgejo(page);
	await openForgejoIssue(page);
	await expect(issueStatusButton(page, 'Todo')).toBeVisible();
	await expect(
		page.getByText(
			'No linked code activity yet. Include ENG-1 in a branch name, pull request title or description, or commit message in a linked repository.',
			{ exact: true }
		)
	).toBeVisible();
	await expect(page.getByRole('button', { name: 'Refresh code activity', exact: true })).toBeVisible();
	await page.clock.install();
	const initialReads = calls.issueReads;
	currentIssue.status = 'review';
	currentIssue.status_id = `${engineeringId}-review`;
	await page.evaluate(() =>
		window.dispatchEvent(
			new CustomEvent('ws:reconnected', {
				detail: { workspaceSlug: 'other-workspace' }
			})
		)
	);
	await page.clock.fastForward(250);
	expect(calls.issueReads).toBe(initialReads);
	await page.evaluate(() =>
		window.dispatchEvent(new CustomEvent('ws:reconnected', { detail: { workspaceSlug: 'test' } }))
	);
	await page.clock.fastForward(250);
	await expect(issueStatusButton(page, 'Code review')).toBeVisible();
	expect(calls.issueReads).toBe(initialReads + 1);
});

test('uses stable issue identity when an old URL returns its canonical identifier', async ({ page }) => {
	const { currentIssue, calls } = await mockForgejo(page);
	await page.goto('/test/issue/LEG-1');
	await expect(page.getByRole('button', { name: 'Forgejo linked issue', exact: true })).toBeVisible();
	await expect(issueStatusButton(page, 'Todo')).toBeVisible();
	await expect(
		page.getByText(
			'No linked code activity yet. Include ENG-1 in a branch name, pull request title or description, or commit message in a linked repository.',
			{ exact: true }
		)
	).toBeVisible();
	await page.clock.install();
	const initialReads = calls.issueReads;
	await page.clock.fastForward(30000);
	expect(calls.issueReads).toBe(initialReads);
	currentIssue.status = 'review';
	currentIssue.status_id = `${engineeringId}-review`;
	await page.evaluate(() =>
		window.dispatchEvent(
			new CustomEvent('ws:issue-updated', {
				detail: { id: 'issue-one', identifier: 'ENG-1', status: 'review' }
			})
		)
	);
	await page.clock.fastForward(250);
	await expect(issueStatusButton(page, 'Code review')).toBeVisible();
	expect(calls.issueReads).toBe(initialReads + 1);
	await expect(page).toHaveURL(/\/test\/issue\/LEG-1$/);
});

test('offers the built-in PR status for every team and explains its automatic creation', async ({ page }) => {
	const { calls } = await mockForgejo(page);
	await openForgejoSettings(page);
	const rule = page.locator('[data-forgejo-rule="pr_opened"]');
	await rule.getByLabel('Target status', { exact: true }).selectOption('pr');
	await expect(rule.getByRole('switch')).toBeEnabled();
	await expect(rule).toContainText('Status changes apply to: Engineering, Operations.');
	await expect(rule).toContainText('The PR status is created automatically in teams that need it.');
	await expect(rule).not.toContainText('These teams do not have this status');
	await expect(rule).not.toContainText('This status is no longer available');
	await rule.getByRole('switch').click();
	await expect(rule.getByRole('switch')).toBeChecked();
	expect(calls.updates[1]).toMatchObject({
		event: 'pr_opened',
		target_status: 'pr',
		target_status_id: null,
		is_active: true
	});
});

for (const resource of ['user-mappings', 'members']) {
	test(`retries a failed ${resource} load while keeping mapping edits disabled until recovery`, async ({ page }) => {
		await mockForgejo(page);
		// Finish the layout's member lookup before faulting the settings lookup of the same endpoint.
		const layoutMembers = page.waitForResponse(
			(response) => new URL(response.url()).pathname === '/api/workspaces/test/members' && response.status() === 200
		);
		await page.goto('/test/settings/profile');
		await layoutMembers;
		await expect(page.getByRole('heading', { name: 'Profile', exact: true })).toBeVisible();
		let requests = 0;
		let release!: () => void;
		const released = new Promise<void>((resolve) => {
			release = resolve;
		});
		await page.route(
			`**/api/workspaces/test/${resource === 'members' ? resource : `forgejo/${resource}`}`,
			async (route) => {
				requests++;
				if (requests === 1)
					return route.fulfill({
						status: 503,
						json: {
							error: { message: 'Mapping data is temporarily unavailable' }
						}
					});
				await released;
				return route.fallback();
			}
		);
		await page.getByRole('link', { name: 'Forgejo', exact: true }).click();
		const login = page.getByLabel('Forgejo login', { exact: true });
		const member = page.getByLabel('Ku member', { exact: true });
		await expect(
			page.getByText('Mapping data is temporarily unavailable', {
				exact: true
			})
		).toBeVisible();
		await expect(login).toBeDisabled();
		await expect(member).toBeDisabled();
		await page.getByRole('button', { name: 'Try again', exact: true }).click();
		await expect.poll(() => requests).toBe(2);
		await expect(login).toBeDisabled();
		await expect(member).toBeDisabled();
		await page.getByRole('button', { name: 'Try again', exact: true }).click();
		expect(requests).toBe(2);
		release();
		await expect(login).toBeEnabled();
		await expect(member).toBeEnabled();
		await expect(
			page.getByText('Mapping data is temporarily unavailable', {
				exact: true
			})
		).toHaveCount(0);
		await login.fill('mario');
		await member.selectOption('user-one');
		await expect(page.getByRole('button', { name: 'Add mapping', exact: true })).toBeEnabled();
	});
}

test('clears old repository selections before rediscovery and blocks saves after discovery fails', async ({ page }) => {
	await mockForgejo(page);
	const oldRepo = {
		forgejo_repo_id: 41,
		full_name: 'team/legacy',
		private: false,
		linked: false
	};
	const freshRepo = {
		forgejo_repo_id: 73,
		full_name: 'team/current',
		private: false,
		linked: false
	};
	let discoveries = 0;
	const additions: number[][] = [];
	await page.route('**/api/workspaces/test/forgejo/repos', (route) => {
		if (route.request().method() === 'POST') {
			additions.push(route.request().postDataJSON().forgejo_repo_ids);
			return route.fulfill({ json: { status: 'linked' } });
		}
		discoveries++;
		if (discoveries === 2)
			return route.fulfill({
				status: 503,
				json: { error: { message: 'Could not load repositories' } }
			});
		return route.fulfill({ json: [discoveries === 1 ? oldRepo : freshRepo] });
	});
	await openForgejoSettings(page);
	await page.getByRole('button', { name: 'Manage', exact: true }).click();
	await page.getByRole('button', { name: oldRepo.full_name, exact: true }).click();
	await expect(page.getByText('1 selected', { exact: true })).toBeVisible();
	await page.getByPlaceholder('Search repositories…').fill('legacy');
	await page.getByRole('button', { name: 'Cancel', exact: true }).click();
	await page.getByRole('button', { name: 'Manage', exact: true }).click();
	await expect(page.getByText('Could not load repositories', { exact: true })).toBeVisible();
	await expect(page.getByRole('button', { name: oldRepo.full_name, exact: true })).toHaveCount(0);
	await expect(page.getByRole('button', { name: 'Save', exact: true })).toHaveCount(0);
	expect(additions).toEqual([]);
	await page.getByRole('button', { name: 'Try again', exact: true }).click();
	await expect(page.getByRole('button', { name: freshRepo.full_name, exact: true })).toBeVisible();
	await expect(page.getByText('0 selected', { exact: true })).toBeVisible();
	await expect(page.getByPlaceholder('Search repositories…')).toHaveValue('');
	await page.getByRole('button', { name: freshRepo.full_name, exact: true }).click();
	await page.getByRole('button', { name: 'Save', exact: true }).click();
	await expect(page.getByRole('button', { name: 'Manage', exact: true })).toBeVisible();
	expect(discoveries).toBe(3);
	expect(additions).toEqual([[73]]);
});

test('requires fresh discovery after client navigation to another workspace and Forgejo instance', async ({ page }) => {
	await mockForgejo(page);
	const oldRepo = {
		forgejo_repo_id: 41,
		full_name: 'team/legacy',
		private: false,
		linked: false
	};
	const freshRepo = {
		forgejo_repo_id: 73,
		full_name: 'other/current',
		private: false,
		linked: false
	};
	const otherWorkspace = {
		id: 'workspace-other',
		name: 'Other Workspace',
		slug: 'other',
		current_user_role: 'owner',
		screen_sharing_enabled: false
	};
	let discoveries = 0;
	const additions: { workspace: string; ids: number[] }[] = [];
	await page.route('**/api/workspaces/test/forgejo/repos', (route) => route.fulfill({ json: [oldRepo] }));
	await page.route('**/api/workspaces/other**', (route) => {
		const path = new URL(route.request().url()).pathname;
		if (path === '/api/workspaces/other') return route.fulfill({ json: otherWorkspace });
		if (path === '/api/workspaces/other/forgejo/status')
			return route.fulfill({
				json: {
					configured: true,
					connected: true,
					instance_url: 'https://git.other.example.com',
					repos: []
				}
			});
		if (path === '/api/workspaces/other/forgejo/repos') {
			if (route.request().method() === 'POST') {
				additions.push({
					workspace: 'other',
					ids: route.request().postDataJSON().forgejo_repo_ids
				});
				return route.fulfill({ json: { status: 'linked' } });
			}
			discoveries++;
			if (discoveries === 1)
				return route.fulfill({
					status: 503,
					json: { error: { message: 'Could not load repositories' } }
				});
			return route.fulfill({ json: [freshRepo] });
		}
		return route.fulfill({ json: [] });
	});
	await openForgejoSettings(page);
	await page.getByRole('button', { name: 'Manage', exact: true }).click();
	await page.getByRole('button', { name: oldRepo.full_name, exact: true }).click();
	await page.getByPlaceholder('Search repositories…').fill('legacy');
	await page.evaluate(() => {
		Object.defineProperty(window, 'kuDiscoveryNavigationMarker', {
			configurable: true,
			value: 'retained'
		});
		const link = document.createElement('a');
		link.href = '/other/settings/forgejo';
		link.textContent = 'Other Forgejo settings';
		document.body.append(link);
	});
	await page.getByRole('link', { name: 'Other Forgejo settings', exact: true }).click();
	await expect(page).toHaveURL(/\/other\/settings\/forgejo$/);
	expect(await page.evaluate(() => Reflect.get(window, 'kuDiscoveryNavigationMarker'))).toBe('retained');
	await expect(page.getByText('https://git.other.example.com', { exact: true })).toBeVisible();
	await page.getByRole('button', { name: 'Manage', exact: true }).click();
	await expect(page.getByText('Could not load repositories', { exact: true })).toBeVisible();
	await expect(page.getByRole('button', { name: oldRepo.full_name, exact: true })).toHaveCount(0);
	await expect(page.getByRole('button', { name: 'Save', exact: true })).toHaveCount(0);
	expect(additions).toEqual([]);
	await page.getByRole('button', { name: 'Try again', exact: true }).click();
	await expect(page.getByRole('button', { name: freshRepo.full_name, exact: true })).toBeVisible();
	await expect(page.getByPlaceholder('Search repositories…')).toHaveValue('');
	await expect(page.getByText('0 selected', { exact: true })).toBeVisible();
	await page.getByRole('button', { name: freshRepo.full_name, exact: true }).click();
	await page.getByRole('button', { name: 'Save', exact: true }).click();
	await expect(page.getByRole('button', { name: 'Manage', exact: true })).toBeVisible();
	expect(additions).toEqual([{ workspace: 'other', ids: [73] }]);
});

test('requires repository rediscovery after a save cannot reconcile connection status', async ({ page }) => {
	await mockForgejo(page);
	const repo = {
		id: 'linked-one',
		forgejo_repo_id: 41,
		full_name: 'team/current',
		private: false,
		linked: true
	};
	let linked = true;
	let statusReads = 0;
	let discoveries = 0;
	const removals: string[] = [];
	await page.route('**/api/workspaces/test/forgejo/status', (route) => {
		statusReads++;
		if (statusReads === 2)
			return route.fulfill({
				status: 503,
				json: {
					error: { message: 'Connection status is temporarily unavailable' }
				}
			});
		return route.fulfill({
			json: {
				configured: true,
				connected: true,
				instance_url: 'https://git.example.com',
				repos: linked ? [repo] : []
			}
		});
	});
	await page.route('**/api/workspaces/test/forgejo/repos', (route) => {
		discoveries++;
		return route.fulfill({ json: [{ ...repo, linked }] });
	});
	await page.route('**/api/workspaces/test/forgejo/repos/linked-one', (route) => {
		removals.push(repo.id);
		linked = false;
		return route.fulfill({ json: { status: 'deleted' } });
	});
	await openForgejoSettings(page);
	await page.getByRole('button', { name: 'Manage', exact: true }).click();
	await page.getByRole('button', { name: repo.full_name, exact: true }).click();
	await page.getByRole('button', { name: 'Save', exact: true }).click();
	await expect(
		page.getByText('Connection status is temporarily unavailable', {
			exact: true
		})
	).toBeVisible();
	await expect(page.getByRole('button', { name: 'Save', exact: true })).toHaveCount(0);
	await expect(page.getByRole('button', { name: repo.full_name, exact: true })).toHaveCount(0);
	expect(removals).toEqual([repo.id]);
	await page.getByRole('button', { name: 'Try again', exact: true }).click();
	await expect(page.getByRole('button', { name: repo.full_name, exact: true })).toBeVisible();
	await expect(page.getByText('0 selected', { exact: true })).toBeVisible();
	await page.getByRole('button', { name: 'Save', exact: true }).click();
	await expect(page.getByRole('button', { name: 'Manage', exact: true })).toBeVisible();
	expect(removals).toEqual([repo.id]);
	expect(discoveries).toBe(2);
	expect(statusReads).toBe(3);
});

test('attempts every selected unlink, reports failed repositories, and retries only remaining links', async ({
	page
}) => {
	await mockForgejo(page);
	const repos = ['first', 'second', 'third'].map((name, index) => ({
		id: `linked-${name}`,
		forgejo_repo_id: index + 1,
		full_name: `team/${name}`,
		default_branch: 'main',
		is_active: true,
		private: false,
		linked: true
	}));
	const linked = new Set(repos.map((repo) => repo.id));
	const removals: string[] = [];
	let statusReads = 0;
	let fail = true;
	let release!: () => void;
	const released = new Promise<void>((resolve) => {
		release = resolve;
	});
	await page.route('**/api/workspaces/test/forgejo/status', (route) => {
		statusReads++;
		return route.fulfill({
			json: {
				configured: true,
				connected: true,
				instance_url: 'https://git.example.com',
				repos: repos.filter((repo) => linked.has(repo.id))
			}
		});
	});
	await page.route('**/api/workspaces/test/forgejo/repos', (route) => route.fulfill({ json: repos }));
	await page.route('**/api/workspaces/test/forgejo/repos/linked-*', async (route) => {
		const id = new URL(route.request().url()).pathname.split('/').at(-1)!;
		removals.push(id);
		if (id === 'linked-first' && fail) await released;
		if (id !== 'linked-second' && fail)
			return route.fulfill({
				status: 503,
				json: { error: { message: 'Temporary unlink failure' } }
			});
		linked.delete(id);
		return route.fulfill({ json: { status: 'deleted' } });
	});
	await openForgejoSettings(page);
	await page.getByRole('button', { name: 'Manage', exact: true }).click();
	await page.getByRole('button', { name: 'Select all (3)', exact: true }).click();
	const save = page.getByRole('button', { name: 'Save', exact: true });
	await save.click();
	await expect.poll(() => removals).toEqual(['linked-first']);
	await expect(save).toBeDisabled();
	await expect(page.getByRole('button', { name: 'team/second', exact: true })).toBeDisabled();
	release();
	await expect(page.getByRole('alert')).toHaveText('Could not save repository links. team/first, team/third');
	expect(removals).toEqual(['linked-first', 'linked-second', 'linked-third']);
	await expect(save).toBeEnabled();
	expect(statusReads).toBe(2);
	fail = false;
	await save.click();
	await expect(page.getByRole('button', { name: 'Manage', exact: true })).toBeVisible();
	expect(removals).toEqual(['linked-first', 'linked-second', 'linked-third', 'linked-first', 'linked-third']);
	expect(statusReads).toBe(3);
	expect([...linked]).toEqual([]);
});

async function mockActivityConnection(page: Page) {
	await mockForgejo(page);
	const connection = {
		configured: true,
		connected: true,
		instance_url: 'https://git.example.com',
		repos: []
	};
	const activity = {
		pull_requests: [],
		branches: [
			{
				name: 'feature/ENG-1-initial',
				repo_full_name: 'team/repo',
				html_url: 'https://git.example.com/team/repo/src/branch/feature%2FENG-1-initial'
			}
		],
		commits: []
	};
	const calls = { status: 0, activity: 0 };
	const faults = { activity: false };
	await page.route('**/api/workspaces/test/forgejo/status', (route) => {
		calls.status++;
		return route.fulfill({ json: connection });
	});
	await page.route('**/api/workspaces/test/issues/ENG-1/forgejo', (route) => {
		calls.activity++;
		return faults.activity
			? route.fulfill({
					status: 503,
					json: { error: { message: 'Activity is temporarily unavailable' } }
				})
			: route.fulfill({ json: activity });
	});
	return { connection, activity, calls, faults };
}

test('manually refreshes connection status and clears stale activity after instance changes or disconnects', async ({
	page
}) => {
	const { connection, activity, calls, faults } = await mockActivityConnection(page);
	await openForgejoIssue(page);
	await expect(page.getByText('feature/ENG-1-initial', { exact: true })).toBeVisible();
	connection.instance_url = 'https://git.changed.example.com';
	faults.activity = true;
	await page.getByRole('button', { name: 'Refresh code activity', exact: true }).click();
	await expect(page.getByText('Could not load linked code activity.', { exact: true })).toBeVisible();
	await expect(page.getByText('feature/ENG-1-initial', { exact: true })).toHaveCount(0);
	expect(calls.status).toBe(2);
	faults.activity = false;
	activity.branches[0].html_url = 'https://git.changed.example.com/team/repo/src/branch/feature%2FENG-1-initial';
	await page.getByRole('button', { name: 'Refresh code activity', exact: true }).click();
	await expect(
		page.locator('a[href="https://git.changed.example.com/team/repo/src/branch/feature%2FENG-1-initial"]')
	).toBeVisible();
	connection.connected = false;
	await page.getByRole('button', { name: 'Refresh code activity', exact: true }).click();
	await expect(page.getByText('feature/ENG-1-initial', { exact: true })).toHaveCount(0);
	await expect(page.getByRole('button', { name: 'Refresh code activity', exact: true })).toHaveCount(0);
	expect(calls.status).toBe(4);
	expect(calls.activity).toBe(3);
});

test('revalidates cached connection on activity events and refreshes missed activity for the reconnected workspace', async ({
	page
}) => {
	const { connection, activity, calls } = await mockActivityConnection(page);
	await openForgejoIssue(page);
	await expect(page.getByText('feature/ENG-1-initial', { exact: true })).toBeVisible();
	await page.clock.install();
	connection.connected = false;
	await page.clock.fastForward(31000);
	await page.evaluate(() =>
		window.dispatchEvent(
			new CustomEvent('ws:forgejo:pr_updated', {
				detail: { issue_id: 'issue-one' }
			})
		)
	);
	await page.clock.fastForward(250);
	await expect(page.getByText('feature/ENG-1-initial', { exact: true })).toHaveCount(0);
	expect(calls.status).toBe(2);
	connection.connected = true;
	activity.branches[0].name = 'feature/ENG-1-missed';
	activity.branches[0].html_url = 'https://git.example.com/team/repo/src/branch/feature%2FENG-1-missed';
	await page.evaluate(() =>
		window.dispatchEvent(
			new CustomEvent('ws:reconnected', {
				detail: { workspaceSlug: 'other-workspace' }
			})
		)
	);
	await page.clock.fastForward(250);
	expect(calls.status).toBe(2);
	expect(calls.activity).toBe(1);
	await page.evaluate(() =>
		window.dispatchEvent(new CustomEvent('ws:reconnected', { detail: { workspaceSlug: 'test' } }))
	);
	await page.clock.fastForward(250);
	await expect(page.getByText('feature/ENG-1-missed', { exact: true })).toBeVisible();
	expect(calls.status).toBe(3);
	expect(calls.activity).toBe(2);
});
