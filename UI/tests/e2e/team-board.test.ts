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

// The list API's ORDER BY for the board: sort_order ASC, then created_at
// DESC, then id DESC (BE/internal/repository/issue.go).
function serverOrder(a: StoredIssue, b: StoredIssue): number {
	if (a.sort_order !== b.sort_order) return a.sort_order - b.sort_order;
	if (a.created_at !== b.created_at) return a.created_at < b.created_at ? 1 : -1;
	return a.id < b.id ? 1 : a.id > b.id ? -1 : 0;
}

function seedIssues(progressSorts = [0, 2000, 3500, 5000]): StoredIssue[] {
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
	progressSorts.forEach((sortOrder, i) => {
		store.push(issue(`i-p${i}`, `DSH-${2000 + i}`, `Progress issue ${i}`, statusProgress, sortOrder));
	});
	// Positive Done tail interleaving the In Progress range, like production.
	for (let i = 0; i < POSITIVE_DONE; i++) {
		store.push(issue(`i-d${i}`, `DSH-${3000 + i}`, `Done issue ${i}`, statusDone, 1000 + i * 450));
	}
	store.sort(serverOrder);
	return store;
}

async function mockTeamBoard(page: Page, options: { progressSorts?: number[] } = {}) {
	const store = seedIssues(options.progressSorts);
	const issueRequests: string[] = [];
	const patches: string[] = [];
	const allPatches: { identifier: string; body: { status_id?: string; sort_order?: number } }[] = [];
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
		const patched = /^\/api\/workspaces\/test\/issues\/([^/]+)$/.exec(path);
		if (patched && patched[1] !== 'bulk' && route.request().method() === 'PATCH') {
			const identifier = patched[1];
			const body = JSON.parse(route.request().postData() ?? '{}');
			allPatches.push({ identifier, body });
			if (identifier === VICTIM_ID) patches.push(route.request().postData() ?? '');
			const target = store.find((it) => it.identifier === identifier);
			if (!target) return route.fulfill({ status: 404, json: { error: { message: 'not found' } } });
			if (body.status_id) {
				target.status_id = body.status_id;
				target.status_info = STATUS_INFO[body.status_id];
			}
			if (typeof body.sort_order === 'number') target.sort_order = body.sort_order;
			target.updated_at = new Date(Date.parse(target.updated_at) + 1000).toISOString();
			store.sort(serverOrder);
			return route.fulfill({ json: target });
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
	return { issueRequests, patches, allPatches, bulkPatches, store };
}

// Navigation finishes before the client has hydrated and fetched the board.
// Wait for its final fixture page before measuring rendering and interactions.
async function openTeamBoard(page: Page) {
	const loaded = page.waitForResponse((response) => {
		const url = new URL(response.url());
		return response.request().method() === 'GET' &&
			url.pathname === '/api/workspaces/test/issues' &&
			url.searchParams.get('page') === '3';
	});
	await page.goto(`/test/teams/${teamId}`);
	const response = await loaded;
	expect(response.ok()).toBe(true);
	expect((await response.json()).has_more).toBe(false);
}

test.use({ viewport: { width: 1920, height: 1080 } });

test('team board initial load follows every page of the team issues', async ({ page }) => {
	const { issueRequests } = await mockTeamBoard(page);

	await openTeamBoard(page);

	// The victim (rank 1) renders immediately…
	await expect(page.getByText(VICTIM_TITLE)).toBeVisible();
	// …and the deep tail (rank 252, page 3) renders too: boards have no
	// load-more, so the initial load must follow every page.
	await expect(page.getByText(DEEP_TAIL_TITLE)).toBeVisible();
	expect(issueRequests).toContain('2');
	expect(issueRequests).toContain('3');
});

test('identical background refreshes cause no DOM churn on the board', async ({ page }) => {
	const { issueRequests } = await mockTeamBoard(page);

	await openTeamBoard(page);
	await expect(page.getByText(VICTIM_TITLE)).toBeVisible();

	// Every WS event, webhook burst, and reconcile tick ends in an
	// app:refresh here. Identical data must not re-render the board:
	// svelte-dnd-action re-applies per-item state whenever a column's items
	// array reference changes, and that per-card churn (771 DOM mutations
	// per refresh on a 252-issue board) is what made the board flicker.
	const perRefresh = await page.evaluate(async () => {
		const board = document.querySelector('main') ?? document.body;
		const counts: number[] = [];
		const observer = new MutationObserver((mutations) => {
			counts[counts.length - 1] += mutations.length;
		});
		observer.observe(board, {
			childList: true,
			subtree: true,
			characterData: true,
			attributes: true
		});
		for (let i = 0; i < 3; i++) {
			counts.push(0);
			window.dispatchEvent(new CustomEvent('app:refresh', { detail: { resources: ['issues'] } }));
			await new Promise((resolve) => setTimeout(resolve, 700));
		}
		observer.disconnect();
		return counts;
	});

	// The refreshes really ran (board fetchAll → one request per page)…
	const requestsAfter = issueRequests.length;
	expect(requestsAfter).toBeGreaterThan(3);
	// …but each identical refresh touched zero DOM nodes.
	expect(perRefresh).toEqual([0, 0, 0]);
});

test('moving a card to In Progress keeps it visible through the background refresh', async ({ page }) => {
	const { issueRequests, patches } = await mockTeamBoard(page);

	await openTeamBoard(page);
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
/** Drags a card by its title and drops it where `target`'s center is:
 * svelte-dnd-action inserts the dragged card at the index of the card under
 * the dragged element's center, so dropping onto a card places it before
 * that card. */
async function dragCard(page: Page, title: string, target: { x: number; y: number }) {
	const card = page.locator('button', { hasText: title }).first();
	const from = await card.boundingBox();
	if (!from) throw new Error(`missing card ${title}`);
	const start = { x: from.x + from.width / 2, y: from.y + from.height / 2 };
	await page.mouse.move(start.x, start.y);
	await page.mouse.down();
	const steps = 15;
	for (let i = 1; i <= steps; i++) {
		await page.mouse.move(start.x + (target.x - start.x) * (i / steps), start.y + (target.y - start.y) * (i / steps));
	}
	await page.waitForTimeout(150);
	await page.mouse.up();
}

async function cardCenter(page: Page, title: string) {
	const box = await page.locator('button', { hasText: title }).first().boundingBox();
	if (!box) throw new Error(`missing card ${title}`);
	return { x: box.x + box.width / 2, y: box.y + box.height / 2 };
}

function columnTitles(page: Page, column: string) {
	return page.locator(`div[aria-label="${column}"] p.line-clamp-2`).allTextContents();
}

/** DOM mutations on the board caused by one background refresh. */
async function refreshMutations(page: Page): Promise<number> {
	return page.evaluate(async () => {
		let count = 0;
		const observer = new MutationObserver((mutations) => (count += mutations.length));
		observer.observe(document.querySelector('main') ?? document.body, {
			childList: true,
			subtree: true,
			characterData: true,
			attributes: true
		});
		window.dispatchEvent(new CustomEvent('app:refresh', { detail: { resources: ['issues'] } }));
		await new Promise((resolve) => setTimeout(resolve, 700));
		observer.disconnect();
		return count;
	});
}

/** Refetches the board through the same path as a WebSocket issue event
 * and waits for it to land. */
async function refreshBoard(page: Page, issueRequests: string[]) {
	const before = issueRequests.length;
	await page.evaluate(() => {
		window.dispatchEvent(new CustomEvent('app:refresh', { detail: { resources: ['issues'] } }));
	});
	await expect.poll(() => issueRequests.length - before, { timeout: 5_000 }).toBeGreaterThan(0);
	await page.waitForTimeout(500);
}

test('a cross-column drop saves only the dropped card and keeps its place after a refresh', async ({ page }) => {
	// A Done card sorting after every In Progress card: the old finalize
	// loop re-saved each card below the drop point, averaging the first one
	// with the dropped card's stale sort_order and sending it to the bottom.
	const MOVED = 'Done issue 20';
	const { issueRequests, allPatches, store } = await mockTeamBoard(page);
	await openTeamBoard(page);
	await expect(page.getByText('Progress issue 1')).toBeVisible();
	await page.locator('button', { hasText: MOVED }).first().scrollIntoViewIfNeeded();

	await dragCard(page, MOVED, await cardCenter(page, 'Progress issue 1'));
	await expect(page.locator('div[aria-label="In Progress"]')).toContainText(MOVED);
	const dropped = await columnTitles(page, 'In Progress');

	// Only the dragged card changed; every other card keeps its sort_order.
	await page.waitForTimeout(1_000);
	expect(allPatches.map((patch) => patch.identifier)).toEqual(['DSH-3020']);
	expect(allPatches[0].body.status_id).toBe(statusProgress);

	// Its new sort_order sits strictly between its neighbours on the board.
	const sortOf = (title: string) => store.find((it) => it.title === title)!.sort_order;
	const index = dropped.indexOf(MOVED);
	if (index > 0) expect(sortOf(MOVED)).toBeGreaterThan(sortOf(dropped[index - 1]));
	if (index < dropped.length - 1) expect(sortOf(MOVED)).toBeLessThan(sortOf(dropped[index + 1]));

	// The drop settled in one render: the refresh its save triggers brings
	// identical data and must not touch the board.
	expect(await refreshMutations(page)).toBe(0);

	await refreshBoard(page, issueRequests);
	expect(await columnTitles(page, 'In Progress')).toEqual(dropped);
});

test('reordering inside a column saves only the moved card', async ({ page }) => {
	const { issueRequests, allPatches } = await mockTeamBoard(page);
	await openTeamBoard(page);
	await expect(page.getByText('Progress issue 3')).toBeVisible();

	await dragCard(page, 'Progress issue 3', await cardCenter(page, 'Progress issue 0'));
	await expect.poll(() => columnTitles(page, 'In Progress')).not.toEqual([
		'Progress issue 0',
		'Progress issue 1',
		'Progress issue 2',
		'Progress issue 3'
	]);
	const dropped = await columnTitles(page, 'In Progress');

	await page.waitForTimeout(1_000);
	expect(allPatches.map((patch) => patch.identifier)).toEqual(['DSH-2003']);

	await refreshBoard(page, issueRequests);
	expect(await columnTitles(page, 'In Progress')).toEqual(dropped);
});

test('a drop between cards that share a sort_order keeps its place after a refresh', async ({ page }) => {
	// Legacy boards carry runs of equal sort_order; averaging two equal
	// neighbours gives the dropped card the same value, and the server's
	// tie-break (created_at, id) then moves it after the next refresh.
	const { issueRequests } = await mockTeamBoard(page, { progressSorts: [1000, 1000, 1000, 1000] });
	await openTeamBoard(page);
	await expect(page.getByText('Progress issue 2')).toBeVisible();

	await dragCard(page, VICTIM_TITLE, await cardCenter(page, 'Progress issue 2'));
	await expect(page.locator('div[aria-label="In Progress"]')).toContainText(VICTIM_TITLE);
	const dropped = await columnTitles(page, 'In Progress');
	expect(dropped.indexOf(VICTIM_TITLE)).toBeGreaterThan(0);

	await page.waitForTimeout(1_000);
	await refreshBoard(page, issueRequests);
	expect(await columnTitles(page, 'In Progress')).toEqual(dropped);
});

test('team header button archives every done issue in one bulk request', async ({ page }) => {
	const { bulkPatches, store } = await mockTeamBoard(page);

	await openTeamBoard(page);
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

	await openTeamBoard(page);
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

	await openTeamBoard(page);
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
