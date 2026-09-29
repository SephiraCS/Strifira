import { test, expect, type Page } from '@playwright/test';

// Mirrors the production Dashboard team: one team, five workflow states, and
// more issues than one page (252 > per_page 100) so a move that re-sorts an
// issue past the first page hides it unless the board follows every page.
const teamId = 'aaaaaaaa-0000-0000-0000-0000000000a1';
const statusBacklog = 'aaaaaaaa-0000-0000-0000-00000000b100';
const statusTodo = 'aaaaaaaa-0000-0000-0000-00000000b200';
const statusProgress = 'aaaaaaaa-0000-0000-0000-00000000b300';
const statusValidation = 'aaaaaaaa-0000-0000-0000-00000000b400';
const statusDone = 'aaaaaaaa-0000-0000-0000-00000000b500';

const VICTIM_ID = 'DSH-9001';
const VICTIM_TITLE = 'Vanish victim issue';
const DEEP_TAIL_TITLE = 'Deep tail issue';

type StoredIssue = {
	id: string;
	identifier: string;
	title: string;
	team_id: string;
	status_id: string;
	status_info?: { id: string; name: string; category: string; color: string | null; position: number };
	priority: number;
	labels: unknown[];
	assignees: unknown[];
	sort_order: number;
	sub_issue_count: number;
	created_at: string;
	updated_at: string;
};

// status_info as the real list API returns it; the archive affordances key
// off its category.
const STATUS_INFO: Record<string, StoredIssue['status_info']> = {
	[statusBacklog]: { id: statusBacklog, name: 'Backlog', category: 'backlog', color: null, position: 0 },
	[statusTodo]: { id: statusTodo, name: 'Todo', category: 'unstarted', color: null, position: 1 },
	[statusProgress]: { id: statusProgress, name: 'In Progress', category: 'started', color: null, position: 2 },
	[statusValidation]: { id: statusValidation, name: 'In Validation', category: 'started', color: null, position: 3 },
	[statusDone]: { id: statusDone, name: 'Done', category: 'completed', color: null, position: 4 }
};

function issue(
	id: string,
	identifier: string,
	title: string,
	statusId: string,
	sortOrder: number
): StoredIssue {
	return {
		id,
		identifier,
		title,
		team_id: teamId,
		status_id: statusId,
		status_info: STATUS_INFO[statusId],
		priority: 0,
		labels: [],
		assignees: [],
		sort_order: sortOrder,
		sub_issue_count: 0,
		created_at: '2026-01-01T00:00:00Z',
		updated_at: '2026-01-01T00:00:00Z'
	};
}

// Mirrors the production Dashboard team's real sort_order distribution
// (verified 2026-09-29): the overwhelming majority of issues carry negative
// sort_order (backlog/validation/done), the In Progress column's few items
// sit at 0..5000 (rank 213+, page 3 of a 252-issue team), and only a handful
// sort above 6000. A drag into In Progress therefore re-sorts the moved card
// deep past page 1 — unless the board follows every page, the refresh after
// the move drops it.
const NEGATIVE_FILLERS = 212;
const POSITIVE_DONE = 34;

function seedIssues(): StoredIssue[] {
	const store: StoredIssue[] = [
		issue('i-victim', VICTIM_ID, VICTIM_TITLE, statusBacklog, -254500),
		issue('i-tail', 'DSH-9252', DEEP_TAIL_TITLE, statusDone, 15500)
	];
	// Negative block: Backlog/In Validation/In PR Review/Todo/Cancelled/Done.
	const cycle = [statusBacklog, statusValidation, statusDone, statusValidation, statusDone, statusValidation];
	for (let i = 0; i < NEGATIVE_FILLERS; i++) {
		store.push(
			issue(`i-n${i}`, `DSH-${1000 + i}`, `Filler issue ${i}`, cycle[i % cycle.length], -264000 + i * 1000)
		);
	}
	// In Progress: a handful of items at the top of the positive range.
	const progressSorts = [0, 2000, 3500, 5000];
	progressSorts.forEach((sortOrder, i) => {
		store.push(issue(`i-p${i}`, `DSH-${2000 + i}`, `Progress issue ${i}`, statusProgress, sortOrder));
	});
	// Positive Done tail interleaving the In Progress range, like production.
	for (let i = 0; i < POSITIVE_DONE; i++) {
		store.push(issue(`i-d${i}`, `DSH-${3000 + i}`, `Done issue ${i}`, statusDone, 1000 + i * 450));
	}
	store.sort((a, b) => a.sort_order - b.sort_order);
	return store;
}

async function mockTeamBoard(page: Page) {
	const store = seedIssues();
	const issueRequests: string[] = [];
	const patches: string[] = [];
	const bulkPatches: string[] = [];

	await page.route('https://raw.githubusercontent.com/**/releases.json', (route) => route.fulfill({ json: [] }));
	await page.route('**/api/**', (route) => {
		const requestUrl = new URL(route.request().url());
		const path = requestUrl.pathname;
		if (!path.startsWith('/api/')) return route.continue();

		if (path === '/api/auth/me') {
			return route.fulfill({ json: { id: 'u1', email: 'test@example.com', name: 'Test User', display_name: 'Test User' } });
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
					issues_group_by: 'status',
					default_view_layout: 'board'
				}
			});
		}
		if (path === '/api/workspaces') {
			return route.fulfill({ json: [{ id: 'w1', name: 'Test Workspace', slug: 'test' }] });
		}
		if (path === '/api/workspaces/test') {
			return route.fulfill({ json: { id: 'w1', name: 'Test Workspace', slug: 'test', current_user_role: 'owner' } });
		}
		if (path === '/api/workspaces/test/teams') {
			return route.fulfill({
				json: [{ id: teamId, name: 'Dashboard', key: 'DSH', color: '#6366f1', icon: 'layers' }]
			});
		}
		if (path === `/api/workspaces/test/teams/${teamId}/statuses`) {
			return route.fulfill({
				json: [
					{ id: statusBacklog, team_id: teamId, name: 'Backlog', slug: 'backlog', category: 'backlog', color: null, position: 0, is_default: true, created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-01T00:00:00Z' },
					{ id: statusTodo, team_id: teamId, name: 'Todo', slug: 'todo', category: 'unstarted', color: null, position: 1, is_default: false, created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-01T00:00:00Z' },
					{ id: statusProgress, team_id: teamId, name: 'In Progress', slug: 'in_progress', category: 'started', color: null, position: 2, is_default: false, created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-01T00:00:00Z' },
					{ id: statusValidation, team_id: teamId, name: 'In Validation', slug: 'in_validation', category: 'started', color: null, position: 3, is_default: false, created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-01T00:00:00Z' },
					{ id: statusDone, team_id: teamId, name: 'Done', slug: 'done', category: 'completed', color: null, position: 4, is_default: false, created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-01T00:00:00Z' }
				]
			});
		}
		if (path === `/api/workspaces/test/teams/${teamId}/cycles`) {
			return route.fulfill({ json: [] });
		}
		if (['/api/workspaces/test/projects', '/api/workspaces/test/labels', '/api/workspaces/test/members', '/api/workspaces/test/views'].includes(path)) {
			return route.fulfill({ json: [] });
		}
		if (path === '/api/notifications') {
			return route.fulfill({ json: { notifications: [], unread_count: 0 } });
		}
		if (path === '/api/workspaces/test/issues' && route.request().method() === 'GET') {
			const page_ = Number(requestUrl.searchParams.get('page') ?? 1);
			issueRequests.push(String(page_));
			const perPage = Number(requestUrl.searchParams.get('per_page') ?? 50);
			const start = (page_ - 1) * perPage;
			const slice = store.slice(start, start + perPage);
			return route.fulfill({
				json: {
					data: slice,
					total_count: store.length,
					page: page_,
					per_page: perPage,
					has_more: start + perPage < store.length
				}
			});
		}
		if (path === `/api/workspaces/test/issues/${VICTIM_ID}` && route.request().method() === 'PATCH') {
			patches.push(route.request().postData() ?? '');
			const body = JSON.parse(route.request().postData() ?? '{}');
			const victim = store.find((it) => it.identifier === VICTIM_ID)!;
			if (body.status_id) victim.status_id = body.status_id;
			// Server-side truth: the re-averaged sort lands deep past page 1
			// (production verified the moved issue at rank 216 of 252).
			victim.sort_order = typeof body.sort_order === 'number' ? body.sort_order : 600000;
			victim.updated_at = '2026-09-29T15:45:35Z';
			store.sort((a, b) => a.sort_order - b.sort_order);
			return route.fulfill({ json: victim });
		}
		if (new RegExp(`^/api/workspaces/test/issues/[^/]+$`).test(path) && route.request().method() === 'GET') {
			const identifier = path.split('/').pop()!;
			const found = store.find((it) => it.identifier === identifier);
			if (found) return route.fulfill({ json: found });
		}
		if (path === '/api/workspaces/test/issues/bulk' && route.request().method() === 'PATCH') {
			bulkPatches.push(route.request().postData() ?? '');
			const body = JSON.parse(route.request().postData() ?? '{}');
			let updated = 0;
			if (body.archived === true) {
				const ids = new Set<string>(body.issue_ids ?? []);
				// The real backend archives only completed-category issues.
				for (let i = store.length - 1; i >= 0; i--) {
					if (ids.has(store[i].id) && store[i].status_info?.category === 'completed') {
						store.splice(i, 1);
						updated++;
					}
				}
			}
			return route.fulfill({ json: { updated } });
		}

		return route.fulfill({ status: 404, json: { error: { message: `Unhandled ${path}` } } });
	});
	return { issueRequests, patches, bulkPatches, store };
}

test.use({ viewport: { width: 1920, height: 1080 } });

test('team board initial load follows every page of the team issues', async ({ page }) => {
	const { issueRequests } = await mockTeamBoard(page);

	await page.goto(`/test/teams/${teamId}`);

	// The victim (rank 1) renders immediately…
	await expect(page.getByText(VICTIM_TITLE)).toBeVisible();
	// …and the deep tail (rank 252, page 3) renders too: boards have no
	// load-more, so the initial load must follow every page.
	await expect(page.getByText(DEEP_TAIL_TITLE)).toBeVisible();
	expect(issueRequests).toContain('2');
	expect(issueRequests).toContain('3');
});

test('moving a card to In Progress keeps it visible through the background refresh', async ({ page }) => {
	const { issueRequests, patches } = await mockTeamBoard(page);

	await page.goto(`/test/teams/${teamId}`);
	await expect(page.getByText(VICTIM_TITLE)).toBeVisible();

	// Drag the victim card from Backlog into In Progress — the exact
	// production flow (drag re-averages sort_order, pushing the issue far
	// past the first page in the team-wide sort_order ordering).
	const card = page.locator('button', { hasText: VICTIM_TITLE }).first();
	const targetColumn = page.locator('div[aria-label="In Progress"]').first();
	await expect(targetColumn).toBeVisible();

	const from = await card.boundingBox();
	const to = await targetColumn.boundingBox();
	if (!from || !to) throw new Error('missing card or column box');
	await page.mouse.move(from.x + from.width / 2, from.y + from.height / 2);
	await page.mouse.down();
	const steps = 12;
	for (let i = 1; i <= steps; i++) {
		await page.mouse.move(
			from.x + ((to.x + to.width / 2) - (from.x + from.width / 2)) * (i / steps),
			from.y + ((to.y + Math.min(to.height / 2, 200)) - (from.y + from.height / 2)) * (i / steps)
		);
	}
	await page.waitForTimeout(150);
	await page.mouse.up();

	await expect.poll(() => patches.length, { timeout: 5_000 }).toBe(1);
	expect(JSON.parse(patches[0]).status_id).toBe(statusProgress);
	// The optimistic update + PATCH response put the card in the column.
	await expect(page.locator('div[aria-label="In Progress"]')).toContainText(VICTIM_TITLE);

	// The move broadcasts issue.updated over the workspace WebSocket; the
	// debounced background refresh must not drop the card (its new sort_rank
	// is past page 1 — page-1-only refreshes made it vanish outright).
	const requestsBefore = issueRequests.length;
	await page.evaluate(() => {
		window.dispatchEvent(new CustomEvent('app:refresh', { detail: { resources: ['issues'] } }));
	});
	await expect
		.poll(() => issueRequests.length - requestsBefore, { timeout: 5_000 })
		.toBeGreaterThan(0);
	// Let the debounced refetch finish assigning its result before asserting.
	await page.waitForTimeout(500);
	await expect(page.locator('div[aria-label="In Progress"]')).toContainText(VICTIM_TITLE);
	await expect(page.getByText(VICTIM_TITLE)).toBeVisible();
});
test('team header button archives every done issue in one bulk request', async ({ page }) => {
	const { bulkPatches, store } = await mockTeamBoard(page);

	await page.goto(`/test/teams/${teamId}`);
	await expect(page.getByText(VICTIM_TITLE)).toBeVisible();

	const doneIds = store.filter((it) => it.status_info?.category === 'completed').map((it) => it.id);
	expect(doneIds.length).toBeGreaterThan(30);

	await page.locator('button[title^="Archive all done issues"]').click();

	await expect.poll(() => bulkPatches.length, { timeout: 5_000 }).toBe(1);
	const body = JSON.parse(bulkPatches[0]);
	expect(body.archived).toBe(true);
	expect([...body.issue_ids].sort()).toEqual([...doneIds].sort());

	await expect(page.getByText(/Archived \d+ issues?/)).toBeVisible();
	// Every done card disappears from the board; other columns keep theirs.
	await expect(page.locator('div[aria-label="Done"]')).not.toContainText(DEEP_TAIL_TITLE);
	await expect(page.locator('div[aria-label="Backlog"]')).toContainText(VICTIM_TITLE);
});

test('done column ... menu archives the whole column', async ({ page }) => {
	const { bulkPatches, store } = await mockTeamBoard(page);

	await page.goto(`/test/teams/${teamId}`);
	await expect(page.getByText(DEEP_TAIL_TITLE)).toBeVisible();

	const doneIds = store.filter((it) => it.status_info?.category === 'completed').map((it) => it.id);

	const doneColumn = page.locator('div[aria-label="Done"]');
	await doneColumn.locator('button[aria-label="Archive all"]').click();
	await page.getByRole('menuitem', { name: 'Archive all' }).click();

	await expect.poll(() => bulkPatches.length, { timeout: 5_000 }).toBe(1);
	const body = JSON.parse(bulkPatches[0]);
	expect(body.archived).toBe(true);
	expect([...body.issue_ids].sort()).toEqual([...doneIds].sort());

	await expect(doneColumn).not.toContainText(DEEP_TAIL_TITLE);
	await expect(page.locator('div[aria-label="Backlog"]')).toContainText(VICTIM_TITLE);
});

test('column sort button reorders one column display-only', async ({ page }) => {
	const { store, patches } = await mockTeamBoard(page);

	// Give the Backlog column a non-monotonic priority spread.
	const priorityByIdentifier: Record<string, number> = {};
	for (const it of store) {
		if (it.status_id === statusBacklog) {
			it.priority = Number(it.id.replace('i-n', '').replace('i-', '')) % 5;
			if (it.identifier === VICTIM_ID) it.priority = 0;
			priorityByIdentifier[it.identifier] = it.priority;
		}
	}

	await page.goto(`/test/teams/${teamId}`);
	await expect(page.getByText(VICTIM_TITLE)).toBeVisible();

	const backlog = page.locator('div[aria-label="Backlog"]');
	async function backlogIdentifiers(): Promise<string[]> {
		const texts = await backlog.locator('span.text-xs').allInnerTexts();
		return texts.filter((t) => /^DSH-/.test(t));
	}

	const manualOrder = await backlogIdentifiers();
	const manualPriorities = manualOrder.map((id) => priorityByIdentifier[id]);
	const isDescending = (values: number[]) => values.every((value, i) => i === 0 || values[i - 1] >= value);
	// The seeded manual order is not priority-sorted…
	expect(isDescending(manualPriorities)).toBe(false);

	// …sorting by Priority reorders the column client-side…
	await backlog.locator('button[aria-label="Sort column"]').click();
	await page.getByRole('menuitem', { name: 'Priority' }).click();
	await expect
		.poll(async () => isDescending((await backlogIdentifiers()).map((id) => priorityByIdentifier[id])), {
			timeout: 5_000
		})
		.toBe(true);

	// …without a single server-side reorder request…
	expect(patches.length).toBe(0);

	// …and Manual puts the board order back.
	await backlog.locator('button[aria-label="Sort column"]').click();
	await page.getByRole('menuitem', { name: 'Manual order' }).click();
	await expect.poll(async () => await backlogIdentifiers(), { timeout: 5_000 }).toEqual(manualOrder);
});
