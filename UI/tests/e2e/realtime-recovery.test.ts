import { test, expect } from '@playwright/test';

const teamId = '00000000-0000-0000-0000-000000000010';

const createdIssue = {
	id: '00000000-0000-0000-0000-000000000100',
	identifier: 'ENG-1',
	title: 'Created while this tab was offline',
	team_id: teamId,
	status: 'backlog',
	status_id: null,
	priority: 0,
	labels: [],
	assignees: [],
	sort_order: 0,
	sub_issue_count: 0,
	created_at: '2026-01-01T00:00:00Z',
	updated_at: '2026-01-01T00:00:00Z'
};

test('an empty issue view picks up an issue created while the realtime connection was down', async ({ page }) => {
	const sockets: { close: () => Promise<void> }[] = [];
	await page.routeWebSocket('**/api/workspaces/*/ws', (ws) => {
		sockets.push(ws);
	});
	let issues: (typeof createdIssue)[] = [];
	let issueRequests = 0;

	await page.route('https://raw.githubusercontent.com/**/releases.json', (route) => route.fulfill({ json: [] }));
	await page.route('**/api/**', async (route) => {
		const requestUrl = new URL(route.request().url());
		const path = requestUrl.pathname;
		if (!path.startsWith('/api/')) return route.continue();
		if (path === '/api/auth/me') {
			return route.fulfill({
				json: { id: 'user-1', email: 'test@example.com', name: 'Test User', display_name: 'Test User', avatar_url: null }
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
		const workspace = {
			id: 'workspace-1',
			name: 'Test Workspace',
			slug: 'test',
			logo_url: null,
			created_at: '2026-01-01T00:00:00Z',
			updated_at: '2026-01-01T00:00:00Z'
		};
		if (path === '/api/workspaces') return route.fulfill({ json: [workspace] });
		if (path === '/api/workspaces/test') return route.fulfill({ json: workspace });
		if (path === '/api/workspaces/test/teams') {
			return route.fulfill({
				json: [
					{
						id: teamId,
						name: 'Engineering',
						key: 'ENG',
						description: null,
						color: '#6366f1',
						icon: 'layers',
						triage_enabled: false,
						parent_auto_close_enabled: false,
						sub_issue_auto_close_enabled: false,
						issue_copy_prompt: null,
						created_at: '2026-01-01T00:00:00Z',
						updated_at: '2026-01-01T00:00:00Z'
					}
				]
			});
		}
		if (
			path === '/api/workspaces/test/projects' ||
			path === '/api/workspaces/test/labels' ||
			path === '/api/workspaces/test/members' ||
			path === '/api/workspaces/test/views' ||
			path === `/api/workspaces/test/teams/${teamId}/statuses` ||
			path === `/api/workspaces/test/teams/${teamId}/cycles`
		) {
			return route.fulfill({ json: [] });
		}
		if (path === '/api/notifications') {
			return route.fulfill({ json: { notifications: [], unread_count: 0 } });
		}
		if (path === '/api/workspaces/test/issues') {
			issueRequests += 1;
			return route.fulfill({
				json: { data: issues, total_count: issues.length, page: 1, per_page: 100, has_more: false }
			});
		}
		return route.fulfill({ status: 404, json: { error: { message: `Unhandled ${path}` } } });
	});

	await page.goto(`/test/teams/${teamId}`);
	await expect.poll(() => sockets.length).toBe(1);
	await expect.poll(() => issueRequests).toBe(1);
	await expect(page.getByText(createdIssue.title)).toBeHidden();

	// Another user creates the first issue while this tab is disconnected, so
	// its event never arrives; the reconnect must reload the empty view.
	issues = [createdIssue];
	await sockets[0].close();
	await expect.poll(() => sockets.length, { timeout: 10_000 }).toBe(2);
	await expect(page.getByText(createdIssue.title)).toBeVisible();
	expect(issueRequests).toBe(2);
});
