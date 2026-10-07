import { test, expect, type Page } from '@playwright/test';

// Escape must leave a full-page issue and return to the view it was opened
// from — the Kanban board or the list, whichever the user selected — and it
// must close a create-issue dialog without moving the underlying view. The
// full-page view has no overlays of its own while Escape is aimed at leaving,
// so every case here keeps focus on the page body.
const teamId = 'aaaaaaaa-0000-0000-0000-0000000000e1';
const statusBacklog = 'aaaaaaaa-0000-0000-0000-00000000e100';
const statusTodo = 'aaaaaaaa-0000-0000-0000-00000000e200';

const ISSUE_ID = 'aaaaaaaa-0000-0000-0000-0000000000e9';
const ISSUE_IDENTIFIER = 'ESC-42';
const ISSUE_TITLE = 'Escape hatch issue';

const TEAM_STATUSES = [
	{ id: statusBacklog, name: 'Backlog', slug: 'backlog', category: 'backlog', color: null, position: 0 },
	{ id: statusTodo, name: 'Todo', slug: 'todo', category: 'unstarted', color: null, position: 1 }
];

const ISSUE = {
	id: ISSUE_ID,
	identifier: ISSUE_IDENTIFIER,
	title: ISSUE_TITLE,
	team_id: teamId,
	status_id: statusBacklog,
	status_info: TEAM_STATUSES[0],
	priority: 0,
	description: null,
	labels: [],
	assignees: [],
	label_ids: [],
	project_id: null,
	cycle_id: null,
	parent: null,
	sort_order: 0,
	sub_issue_count: 0,
	is_subscribed: false,
	created_at: '2026-01-01T00:00:00Z',
	updated_at: '2026-01-01T00:00:00Z'
};

const teamUrl = `/test/teams/${teamId}`;
const issueUrl = `/test/issue/${ISSUE_IDENTIFIER}`;

async function mockWorkspace(page: Page) {
	await page.route('https://raw.githubusercontent.com/**/releases.json', (route) => route.fulfill({ json: [] }));
	await page.route('**/api/**', async (route) => {
		const path = new URL(route.request().url()).pathname;
		if (!path.startsWith('/api/')) return route.continue();

		if (path === '/api/auth/me') {
			return route.fulfill({
				json: {
					id: '00000000-0000-0000-0000-000000000001',
					email: 'test@example.com',
					name: 'Test User',
					display_name: 'Test User',
					avatar_url: null
				}
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
					issues_group_by: 'none'
				}
			});
		}

		if (path === '/api/workspaces/test') {
			return route.fulfill({
				json: {
					id: '00000000-0000-0000-0000-000000000002',
					name: 'Test Workspace',
					slug: 'test',
					logo_url: null,
					created_at: '2026-01-01T00:00:00Z',
					updated_at: '2026-01-01T00:00:00Z'
				}
			});
		}

		if (path === '/api/workspaces') {
			return route.fulfill({
				json: [
					{
						id: '00000000-0000-0000-0000-000000000002',
						name: 'Test Workspace',
						slug: 'test',
						logo_url: null,
						created_at: '2026-01-01T00:00:00Z',
						updated_at: '2026-01-01T00:00:00Z'
					}
				]
			});
		}

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

		if (path === `/api/workspaces/test/teams/${teamId}/statuses`) {
			return route.fulfill({ json: TEAM_STATUSES });
		}

		if (
			path === `/api/workspaces/test/teams/${teamId}/cycles` ||
			path === '/api/workspaces/test/projects' ||
			path === '/api/workspaces/test/labels' ||
			path === '/api/workspaces/test/members' ||
			path === '/api/workspaces/test/views'
		) {
			return route.fulfill({ json: [] });
		}

		if (path === '/api/notifications') {
			return route.fulfill({ json: { notifications: [], unread_count: 0 } });
		}

		if (path === '/api/workspaces/test/ai-settings') {
			return route.fulfill({ json: { base_url: '', model: '', has_api_key: false } });
		}

		if (path === '/api/workspaces/test/issues' && route.request().method() === 'GET') {
			return route.fulfill({ json: { data: [ISSUE], total_count: 1, page: 1, has_more: false } });
		}

		if (path === `/api/workspaces/test/issues/${ISSUE_IDENTIFIER}`) {
			return route.fulfill({ json: ISSUE });
		}

		if (
			path === `/api/workspaces/test/issues/${ISSUE_IDENTIFIER}/comments` ||
			path === `/api/workspaces/test/issues/${ISSUE_IDENTIFIER}/history`
		) {
			return route.fulfill({ json: [] });
		}

		return route.fulfill({ status: 404, json: { error: { message: `Unhandled ${path}` } } });
	});
}

async function openTeamBoard(page: Page) {
	await page.goto(teamUrl);
	// Explicitly select the board so the test asserts the user's choice is
	// the layout Escape returns to, not the default list.
	await page.locator('[aria-label="Board view"]').click();
	await expect(page.locator('div[aria-label="Backlog"]')).toBeVisible();
}

test('Escape from an issue returns to the board when the user selected the board', async ({ page }) => {
	await mockWorkspace(page);
	await openTeamBoard(page);

	await page.getByText(ISSUE_TITLE).click();
	await expect(page).toHaveURL(new RegExp(issueUrl));

	await page.keyboard.press('Escape');

	await expect(page).toHaveURL(new RegExp(teamUrl));
	// The board, not the list, must be what comes back.
	await expect(page.locator('div[aria-label="Backlog"]')).toBeVisible();
	await expect(page.getByText(ISSUE_TITLE)).toBeVisible();

	// A real history entry was used: forward returns to the issue.
	await page.goForward();
	await expect(page).toHaveURL(new RegExp(issueUrl));
});

test('Escape from an issue returns to the list when the user stayed on the list', async ({ page }) => {
	await mockWorkspace(page);
	await page.goto(teamUrl);
	// Default layout is the list; the issue title only renders in a row there.
	await expect(page.getByText(ISSUE_TITLE)).toBeVisible();

	await page.getByText(ISSUE_TITLE).click();
	await expect(page).toHaveURL(new RegExp(issueUrl));

	await page.keyboard.press('Escape');

	await expect(page).toHaveURL(new RegExp(teamUrl));
	await expect(page.getByText(ISSUE_TITLE)).toBeVisible();
	await expect(page.locator('div[aria-label="Backlog"]')).toHaveCount(0);
});

test('Escape on a directly opened issue falls back to the team view', async ({ page }) => {
	await mockWorkspace(page);
	// No in-app history (fresh tab / shared deep link): the team issues view
	// in the user's selected layout is the only sensible destination.
	await page.goto(issueUrl);
	await expect(page.getByText(ISSUE_TITLE)).toBeVisible();

	await page.keyboard.press('Escape');

	await expect(page).toHaveURL(new RegExp(teamUrl));
	await expect(page.getByText(ISSUE_TITLE)).toBeVisible();
});

test('Escape closes the create-issue dialog and keeps the board underneath', async ({ page }) => {
	await mockWorkspace(page);
	await openTeamBoard(page);

	await page.keyboard.press('c');
	await expect(page.locator('#create-issue-title')).toBeVisible();

	await page.keyboard.press('Escape');

	await expect(page.locator('#create-issue-title')).toBeHidden();
	await expect(page).toHaveURL(new RegExp(teamUrl));
	await expect(page.locator('div[aria-label="Backlog"]')).toBeVisible();
});

test('Escape closes an open properties popover before leaving the issue', async ({ page }) => {
	await mockWorkspace(page);
	await openTeamBoard(page);

	await page.getByText(ISSUE_TITLE).click();
	await expect(page).toHaveURL(new RegExp(issueUrl));

	// "s" opens the status popover; Escape belongs to it first.
	await page.keyboard.press('s');
	await expect(page.locator('[data-slot="popover-content"][data-state="open"]')).toBeVisible();

	await page.keyboard.press('Escape');

	await expect(page.locator('[data-slot="popover-content"][data-state="open"]')).toBeHidden();
	await expect(page).toHaveURL(new RegExp(issueUrl));

	// With the popover gone the next Escape leaves the issue.
	await page.keyboard.press('Escape');
	await expect(page).toHaveURL(new RegExp(teamUrl));
	await expect(page.locator('div[aria-label="Backlog"]')).toBeVisible();
});

test('Escape still leaves the issue when a popover lingers open but invisible', async ({ page }) => {
	await mockWorkspace(page);
	await openTeamBoard(page);

	await page.getByText(ISSUE_TITLE).click();
	await expect(page).toHaveURL(new RegExp(issueUrl));

	// Reproduces a long-lived-tab artifact: a bits-ui layer left
	// data-state="open" with transparent content (observed with the sidebar
	// workspace switcher after dev-server hot swaps). It must not own the
	// Escape key forever.
	await page.evaluate(() => {
		const trigger = document.createElement('button');
		trigger.setAttribute('data-state', 'open');
		trigger.setAttribute('data-slot', 'popover-trigger');
		document.body.appendChild(trigger);
		const content = document.createElement('div');
		content.setAttribute('data-state', 'open');
		content.setAttribute('data-slot', 'popover-content');
		content.style.opacity = '0';
		content.textContent = 'stuck but invisible';
		document.body.appendChild(content);
	});

	await page.keyboard.press('Escape');

	await expect(page).toHaveURL(new RegExp(teamUrl));
	await expect(page.locator('div[aria-label="Backlog"]')).toBeVisible();
});
