import { test, expect as baseExpect, type Page } from '@playwright/test';

const expect = baseExpect.configure({ timeout: 10_000 });
test.use({ actionTimeout: 15_000 });
test.setTimeout(60_000);

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

test('Escape cancels title editing before leaving the issue', async ({ page }) => {
	await mockWorkspace(page);
	await openTeamBoard(page);
	await page.getByText(ISSUE_TITLE).click();
	await expect(page).toHaveURL(new RegExp(issueUrl));

	await page.getByRole('button', { name: ISSUE_TITLE, exact: true }).click();
	const title = page.getByRole('textbox').filter({ visible: true }).first();
	await title.fill('Unsaved title');
	await title.press('Escape');

	await expect(page.getByRole('button', { name: ISSUE_TITLE, exact: true })).toBeVisible();
	await expect(page).toHaveURL(new RegExp(issueUrl));
	await page.locator('body').click({ position: { x: 500, y: 10 } });
	await page.keyboard.press('Escape');
	await expect(page).toHaveURL(new RegExp(teamUrl));
});

for (const dialog of ['command palette', 'parent picker', 'relation picker']) {
	test(`Escape closes the ${dialog} from a button without leaving the issue`, async ({ page }) => {
		await mockWorkspace(page);
		await openTeamBoard(page);
		await page.getByText(ISSUE_TITLE).click();
		await expect(page).toHaveURL(new RegExp(issueUrl));

		if (dialog === 'command palette') {
			await page.keyboard.press('Control+k');
		} else {
			await page.getByRole('button', { name: 'Issue actions' }).click();
			await page
				.getByRole('button', { name: dialog === 'parent picker' ? 'Add parent' : 'Add relation', exact: true })
				.click();
		}
		const overlay = page.locator('div.fixed.inset-0.z-50').filter({ has: page.locator('input') });
		await expect(overlay).toBeVisible();
		// These custom dialogs also accept Escape when focus leaves the search input.
		await overlay.locator('button').first().focus();
		await page.keyboard.press('Escape');

		await expect(overlay).toBeHidden();
		await expect(page).toHaveURL(new RegExp(issueUrl));
		await page.keyboard.press('Escape');
		await expect(page).toHaveURL(new RegExp(teamUrl));
		await expect(page.locator('div[aria-label="Backlog"]')).toBeVisible();
	});
}

const INBOX_NOTIFICATION = {
	id: 'aaaaaaaa-0000-0000-0000-0000000000ea',
	issue_id: ISSUE_ID,
	issue_identifier: ISSUE_IDENTIFIER,
	type: 'assigned',
	title: 'You were assigned an Escape hatch issue',
	read_at: '2026-01-01T00:00:00Z',
	snoozed_until: null,
	archived_at: null,
	created_at: '2026-01-01T00:00:00Z'
};

async function mockInboxWorkspace(page: Page) {
	await mockWorkspace(page);
	await page.route(/\/api\/notifications(?:\?.*)?$/, (route) =>
		route.fulfill({
			json: {
				notifications: new URL(route.request().url()).searchParams.has('tab') ? [] : [INBOX_NOTIFICATION],
				unread_count: 0
			}
		})
	);
}

for (const entry of ['direct', 'navigated']) {
	test(`Escape closes the issue panel in a ${entry} Inbox without leaving the list`, async ({ page }) => {
		await mockInboxWorkspace(page);
		if (entry === 'navigated') {
			await openTeamBoard(page);
			await page.locator('a[href="/test/inbox"]').first().click();
		} else {
			await page.goto('/test/inbox');
		}
		const issueTitle = page.getByRole('button', { name: ISSUE_TITLE, exact: true });
		await expect(issueTitle).toBeVisible();
		await page.keyboard.press('Escape');

		await expect(issueTitle).toBeHidden();
		await expect(page).toHaveURL(/\/test\/inbox$/);
		await expect(page.getByText('Select a notification to view details', { exact: true })).toBeVisible();
		const notification = page.getByRole('button', { name: new RegExp(INBOX_NOTIFICATION.title) });
		await expect(notification).toBeVisible();
		// Clearing selection must allow the same notification to be opened again.
		await notification.click();
		await expect(issueTitle).toBeVisible();
		await page.keyboard.press('Escape');
		await expect(issueTitle).toBeHidden();
		if (entry === 'navigated') {
			await page.goBack();
			await expect(page).toHaveURL(new RegExp(teamUrl));
			await expect(page.locator('div[aria-label="Backlog"]')).toBeVisible();
		}
	});
}

test('Escape closes the Inbox snooze calendar from a control and preserves the issue panel', async ({ page }) => {
	await mockInboxWorkspace(page);
	await page.goto('/test/inbox');
	const issueTitle = page.getByRole('button', { name: ISSUE_TITLE, exact: true });
	await expect(issueTitle).toBeVisible();
	const notification = page.getByRole('button', { name: new RegExp(INBOX_NOTIFICATION.title) });
	await notification.click({ button: 'right' });
	await page.getByRole('menuitem', { name: 'Snooze', exact: true }).hover();
	await page.getByRole('menuitem', { name: 'Pick date...', exact: true }).click();

	const dialog = page.getByRole('dialog').filter({ hasText: 'Snooze notification' });
	await expect(dialog).toBeVisible();
	// Opening the calendar must give it Escape immediately, without manual focus.
	await page.keyboard.press('Escape');
	await expect(dialog).toBeHidden();
	await expect(issueTitle).toBeVisible();
	await notification.click({ button: 'right' });
	await page.getByRole('menuitem', { name: 'Snooze', exact: true }).hover();
	await page.getByRole('menuitem', { name: 'Pick date...', exact: true }).click();
	await expect(dialog).toBeVisible();
	await page.keyboard.press('Shift+?');
	const help = page.getByRole('dialog').filter({ hasText: 'Keyboard shortcuts' });
	await expect(help).toBeVisible();
	await page.keyboard.press('Escape');
	await expect(help).toBeHidden();
	await expect(dialog).toBeVisible();
	await dialog.getByRole('button', { name: 'Close', exact: true }).focus();
	await page.keyboard.press('Escape');

	await expect(dialog).toBeHidden();
	await expect(page).toHaveURL(/\/test\/inbox$/);
	await expect(issueTitle).toBeVisible();
	await expect(notification).toBeVisible();
	await page.keyboard.press('Escape');
	await expect(issueTitle).toBeHidden();
	await expect(page).toHaveURL(/\/test\/inbox$/);
});

test('a pending Inbox issue response does not reopen the panel after changing tabs', async ({ page }) => {
	await mockInboxWorkspace(page);
	let releaseIssue!: () => void;
	const pendingIssue = new Promise<void>((resolve) => {
		releaseIssue = resolve;
	});
	await page.route(`**/api/workspaces/test/issues/${ISSUE_IDENTIFIER}`, async (route) => {
		await pendingIssue;
		await route.fulfill({ json: ISSUE });
	});
	const issueRequest = page.waitForRequest(`**/api/workspaces/test/issues/${ISSUE_IDENTIFIER}`);
	await page.goto('/test/inbox');
	await issueRequest;
	await page.getByRole('tab', { name: 'Snoozed', exact: true }).click();
	await expect(page.getByText('Select a notification to view details', { exact: true })).toBeVisible();

	const issueResponse = page.waitForResponse(`**/api/workspaces/test/issues/${ISSUE_IDENTIFIER}`);
	releaseIssue();
	await (await issueResponse).finished();
	await page.evaluate(() => new Promise<void>((resolve) => requestAnimationFrame(() => resolve())));
	await expect(page.getByRole('button', { name: ISSUE_TITLE, exact: true })).toBeHidden();
	await expect(page.getByText('Select a notification to view details', { exact: true })).toBeVisible();
	await expect(page).toHaveURL(/\/test\/inbox$/);
});

const NEXT_ISSUE = {
	...ISSUE,
	id: 'aaaaaaaa-0000-0000-0000-0000000000eb',
	identifier: 'ESC-43',
	title: 'Next Escape issue',
	sort_order: 1
};

test('notification refresh does not reopen an Inbox panel dismissed with Escape', async ({ page }) => {
	await mockInboxWorkspace(page);
	await page.goto('/test/inbox');
	const title = page.getByRole('button', { name: ISSUE_TITLE, exact: true });
	await expect(title).toBeVisible();
	await page.keyboard.press('Escape');
	await expect(title).toBeHidden();
	const refreshed = page.waitForResponse(/\/api\/notifications(?:\?.*)?$/);
	await page.evaluate(() => window.dispatchEvent(new Event('ws:notification')));
	await (await refreshed).finished();
	await expect(page.getByRole('button', { name: new RegExp(INBOX_NOTIFICATION.title) })).toBeVisible();
	await expect(title).toBeHidden();
	await expect(page).toHaveURL(/\/test\/inbox$/);
});

test('a pending title save does not reopen an Inbox panel dismissed with Escape', async ({ page }) => {
	await mockInboxWorkspace(page);
	let releaseSave!: () => void;
	const pendingSave = new Promise<void>((resolve) => {
		releaseSave = resolve;
	});
	await page.route(`**/api/workspaces/test/issues/${ISSUE_IDENTIFIER}`, async (route) => {
		if (route.request().method() !== 'PATCH') return route.fallback();
		await pendingSave;
		await route.fulfill({ json: { ...ISSUE, title: 'Saved title' } });
	});
	await page.goto('/test/inbox');
	await page.getByRole('button', { name: ISSUE_TITLE, exact: true }).click();
	const input = page.getByRole('textbox').filter({ visible: true }).first();
	await input.fill('Saved title');
	const saveRequest = page.waitForRequest((request) => request.method() === 'PATCH');
	await input.press('Enter');
	await saveRequest;
	await page.keyboard.press('Escape');
	await expect(page.getByText('Select a notification to view details', { exact: true })).toBeVisible();
	const saveResponse = page.waitForResponse((response) => response.request().method() === 'PATCH');
	releaseSave();
	await (await saveResponse).finished();
	await page.evaluate(() => new Promise<void>((resolve) => requestAnimationFrame(() => resolve())));
	await expect(page.getByRole('button', { name: 'Saved title', exact: true })).toBeHidden();
	await expect(page.getByText('Select a notification to view details', { exact: true })).toBeVisible();
	await expect(page).toHaveURL(/\/test\/inbox$/);
});

async function mockAdjacentIssues(page: Page) {
	await mockWorkspace(page);
	await page.route(/\/api\/workspaces\/test\/issues(?:\?.*)?$/, (route) =>
		route.fulfill({
			json: { data: [ISSUE, NEXT_ISSUE], total_count: 2, page: 1, has_more: false }
		})
	);
	await page.route('**/api/workspaces/test/issues/ESC-43', (route) => route.fulfill({ json: NEXT_ISSUE }));
}

for (const entry of ['direct', 'board']) {
	test(`Escape after switching issues from a ${entry} entry returns safely to the team`, async ({ page }) => {
		await mockAdjacentIssues(page);
		if (entry === 'board') {
			await openTeamBoard(page);
			await page.getByText(ISSUE_TITLE, { exact: true }).click();
		} else {
			await page.goto(issueUrl);
		}
		await expect(page.getByRole('button', { name: ISSUE_TITLE, exact: true })).toBeVisible();
		await page.getByRole('button', { name: 'Next issue (J)', exact: true }).click();
		await expect(page.getByRole('button', { name: NEXT_ISSUE.title, exact: true })).toBeVisible();
		await page.keyboard.press('Escape');
		await expect(page).toHaveURL(new RegExp(teamUrl));
		await expect(page.getByText(ISSUE_TITLE, { exact: true })).toBeVisible();
		if (entry === 'board') {
			await expect(page.locator('div[aria-label="Backlog"]')).toBeVisible();
			await page.goForward();
			await expect(page).toHaveURL(/\/test\/issue\/ESC-43$/);
		}
	});
}

test('Escape on the initial issue after browser Back uses the team fallback', async ({ page }) => {
	await mockWorkspace(page);
	await page.goto(issueUrl);
	await expect(page.getByRole('button', { name: ISSUE_TITLE, exact: true })).toBeVisible();
	await page.locator('a[href="/test/inbox"]').first().click();
	await expect(page).toHaveURL(/\/test\/inbox$/);
	await page.goBack();
	await expect(page.getByRole('button', { name: ISSUE_TITLE, exact: true })).toBeVisible();
	await page.keyboard.press('Escape');
	await expect(page).toHaveURL(new RegExp(teamUrl));
});

test('Escape closes the collapsed sidebar drawer from an inner control', async ({ page }) => {
	await page.addInitScript(() => localStorage.setItem('sidebar_collapsed_panel', 'true'));
	await mockWorkspace(page);
	await page.goto(issueUrl);
	await expect(page.getByRole('button', { name: ISSUE_TITLE, exact: true })).toBeVisible();
	await page.locator('div.fixed.left-0.top-0.z-20').hover({ force: true });
	const drawer = page.getByRole('button', { name: 'Close sidebar', exact: true });
	await expect(drawer).toHaveCSS('pointer-events', 'auto');
	await page.keyboard.press('Control+k');
	const palette = page.locator('div.fixed.inset-0.z-50').filter({ has: page.locator('input') });
	await expect(palette).toBeVisible();
	await palette.locator('button').first().focus();
	await page.keyboard.press('Escape');
	await expect(palette).toBeHidden();
	await expect(drawer).toHaveCSS('pointer-events', 'auto');
	// Hover leaves keyboard focus on the issue, but Escape still belongs to the drawer.
	await page.keyboard.press('Escape');
	await expect(drawer).toHaveCSS('pointer-events', 'none');
	await expect(page).toHaveURL(new RegExp(issueUrl));
	await page.locator('div.fixed.left-0.top-0.z-20').hover({ force: true });
	await expect(drawer).toHaveCSS('pointer-events', 'auto');
	await drawer.locator('a[href="/test/inbox"]').focus();
	await page.keyboard.press('Escape');
	await expect(drawer).toHaveCSS('pointer-events', 'none');
	await expect(page).toHaveURL(new RegExp(issueUrl));
	await page.keyboard.press('Escape');
	await expect(page).toHaveURL(new RegExp(teamUrl));
});
