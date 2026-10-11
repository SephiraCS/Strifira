import { test, expect, type Page } from '@playwright/test';

const teamA = '00000000-0000-0000-0000-0000000000a1';
const teamB = '00000000-0000-0000-0000-0000000000b2';
const statusA1 = '00000000-0000-0000-0000-0000000001a1';
const statusA2 = '00000000-0000-0000-0000-0000000002a2';
const statusB1 = '00000000-0000-0000-0000-0000000001b1';
// Belongs to no loaded column → must land in the unmapped bucket, not vanish.
const statusOrphan = '00000000-0000-0000-0000-00000000dead';

function issue(id: string, identifier: string, title: string, teamId: string, statusId: string) {
	return {
		id,
		identifier,
		title,
		team_id: teamId,
		status_id: statusId,
		priority: 0,
		labels: [],
		assignees: [],
		sort_order: 0,
		sub_issue_count: 0,
		created_at: '2026-01-01T00:00:00Z',
		updated_at: '2026-01-01T00:00:00Z'
	};
}

async function mockMyIssuesBoard(page: Page) {
	const issueRequests: string[] = [];
	const unhandled: string[] = [];
	const patches: { status_id?: string }[] = [];
	await page.route('https://raw.githubusercontent.com/**/releases.json', (route) => route.fulfill({ json: [] }));
	await page.route('**/api/**', (route) => {
		const requestUrl = new URL(route.request().url());
		const path = requestUrl.pathname;
		if (!path.startsWith('/api/')) return route.continue();

		if (path === '/api/auth/me') {
			return route.fulfill({
				json: {
					id: 'u1',
					email: 'test@example.com',
					name: 'Test User',
					display_name: 'Test User'
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
					issues_group_by: 'status',
					default_view_layout: 'board'
				}
			});
		}
		if (path === '/api/workspaces') {
			return route.fulfill({
				json: [{ id: 'w1', name: 'Test Workspace', slug: 'test' }]
			});
		}
		if (path === '/api/workspaces/test') {
			return route.fulfill({
				json: {
					id: 'w1',
					name: 'Test Workspace',
					slug: 'test',
					current_user_role: 'owner'
				}
			});
		}
		if (path === '/api/workspaces/test/teams') {
			return route.fulfill({
				json: [
					{
						id: teamA,
						name: 'Alpha',
						key: 'ALP',
						color: '#6366f1',
						icon: 'layers'
					},
					{
						id: teamB,
						name: 'Beta',
						key: 'BET',
						color: '#22c55e',
						icon: 'layers'
					}
				]
			});
		}
		if (path === `/api/workspaces/test/teams/${teamA}/statuses`) {
			return route.fulfill({
				json: [
					{
						id: statusA1,
						team_id: teamA,
						name: 'Todo',
						slug: 'todo',
						category: 'unstarted',
						color: null,
						position: 0,
						is_default: true,
						created_at: '2026-01-01T00:00:00Z',
						updated_at: '2026-01-01T00:00:00Z'
					},
					{
						id: statusA2,
						team_id: teamA,
						name: 'Done',
						slug: 'done',
						category: 'completed',
						color: null,
						position: 1,
						is_default: false,
						created_at: '2026-01-01T00:00:00Z',
						updated_at: '2026-01-01T00:00:00Z'
					}
				]
			});
		}
		if (path === `/api/workspaces/test/teams/${teamB}/statuses`) {
			return route.fulfill({
				json: [
					{
						id: statusB1,
						team_id: teamB,
						name: 'In Progress',
						slug: 'in_progress',
						category: 'started',
						color: null,
						position: 0,
						is_default: true,
						created_at: '2026-01-01T00:00:00Z',
						updated_at: '2026-01-01T00:00:00Z'
					}
				]
			});
		}
		if (
			[
				'/api/workspaces/test/projects',
				'/api/workspaces/test/labels',
				'/api/workspaces/test/members',
				'/api/workspaces/test/views'
			].includes(path)
		) {
			return route.fulfill({ json: [] });
		}
		if (path === '/api/notifications') {
			return route.fulfill({ json: { notifications: [], unread_count: 0 } });
		}
		if (/^\/api\/workspaces\/test\/issues\/[^/]+$/.test(path) && route.request().method() === 'PATCH') {
			patches.push(JSON.parse(route.request().postData() ?? '{}'));
			return route.fulfill({
				status: 400,
				json: { error: { message: 'Unexpected issue update' } }
			});
		}
		if (path === '/api/workspaces/test/issues') {
			issueRequests.push(`${requestUrl.searchParams.get('page') ?? '1'}`);
			// Paginate: the orphaned-status issue only exists on page 2, so it
			// renders on the board only when the fetch-all paging works.
			const page = Number(requestUrl.searchParams.get('page') ?? 1);
			if (page === 1) {
				return route.fulfill({
					json: {
						data: [
							issue('i1', 'ALP-1', 'Alpha todo issue', teamA, statusA1),
							issue('i2', 'BET-1', 'Beta in-progress issue', teamB, statusB1)
						],
						total_count: 3,
						page: 1,
						has_more: true
					}
				});
			}
			return route.fulfill({
				json: {
					data: [issue('i3', 'ALP-2', 'Orphaned status issue', teamA, statusOrphan)],
					total_count: 3,
					page,
					has_more: false
				}
			});
		}

		unhandled.push(path);
		return route.fulfill({
			status: 404,
			json: { error: { message: `Unhandled ${path}` } }
		});
	});
	return { issueRequests, unhandled, patches };
}

async function openMyIssuesBoard(page: Page) {
	await page.goto('/test/my-issues');
	await page.locator('[data-issue-drop-zone]').first().waitFor();
}

test('my issues board renders one section per team and never hides orphaned statuses', async ({ page }) => {
	const { unhandled, issueRequests } = await mockMyIssuesBoard(page);

	await openMyIssuesBoard(page);

	// One board section per team, each issue under its own team's section.
	const alphaSection = page.locator('section').filter({ hasText: 'Alpha todo issue' });
	const betaSection = page.locator('section').filter({ hasText: 'Beta in-progress issue' });
	await expect(alphaSection).toBeVisible();
	await expect(betaSection).toBeVisible();
	await expect(alphaSection).not.toContainText('Beta in-progress issue');
	await expect(page.getByText('Alpha todo issue')).toBeVisible();
	await expect(page.getByText('Beta in-progress issue')).toBeVisible();

	// An issue whose status key matches no column stays visible in the
	// trailing bucket instead of disappearing from the board — and it lives on
	// page 2, proving boards follow every page (a move beyond the first page
	// used to make issues vanish outright).
	await expect(page.getByText('Orphaned status issue')).toBeVisible();
	await expect(page.getByLabel('No status').first()).toBeVisible();
	expect(issueRequests).toContain('2');
	expect(unhandled).toEqual([]);
});

test('app.refresh bursts coalesce into one background reload and never blank the board', async ({ page }) => {
	const { issueRequests } = await mockMyIssuesBoard(page);

	await openMyIssuesBoard(page);
	await expect(page.getByText('Alpha todo issue')).toBeVisible();

	const requestsBefore = issueRequests.length;

	// Burst of refresh events — what webhook processing and the five-minute
	// GitHub reconciler deliver (each used to trigger its own immediate
	// reload with the loading flag up, blanking the whole board).
	for (let i = 0; i < 3; i++) {
		await page.evaluate(() => {
			window.dispatchEvent(new CustomEvent('app:refresh', { detail: { resources: ['issues'] } }));
		});
	}

	// The board card must stay attached and visible across the entire
	// debounce + refetch window (the old code unmounted it while loading).
	const stayedVisible = await page.evaluate(async () => {
		const card = [...document.querySelectorAll<HTMLElement>('p')].find((el) => el.textContent === 'Alpha todo issue');
		if (!card) return false;
		for (let i = 0; i < 16; i++) {
			await new Promise((resolve) => setTimeout(resolve, 50));
			if (!card.isConnected || card.getBoundingClientRect().height === 0) return false;
		}
		return true;
	});
	expect(stayedVisible).toBe(true);

	// The whole burst produced exactly one trailing refetch (two page
	// requests, since the board follows every page).
	await expect.poll(() => issueRequests.length - requestsBefore, { timeout: 3_000 }).toBe(2);
	await expect(page.getByText('Alpha todo issue')).toBeVisible();
});

for (const foreign of [false, true]) {
	test(`issue release on ${foreign ? 'another team board' : 'the unmapped bucket'} cancels without an illegal status save`, async ({
		page
	}) => {
		await page.setViewportSize({ width: 1920, height: 1600 });
		const { patches, unhandled } = await mockMyIssuesBoard(page);
		await openMyIssuesBoard(page);
		const source = page.locator('button', { hasText: 'Alpha todo issue' }).first();
		const target = page
			.locator('button', {
				hasText: foreign ? 'Beta in-progress issue' : 'Orphaned status issue'
			})
			.first();
		await expect(target).toBeVisible();
		const from = await source.boundingBox();
		const to = await target.boundingBox();
		if (!from || !to) throw new Error('Missing source or destination board card');
		const start = { x: from.x + from.width / 2, y: from.y + from.height / 2 };
		await page.mouse.move(start.x, start.y);
		await page.mouse.down();
		await page.mouse.move(start.x + 8, start.y);
		await page.waitForTimeout(130);
		await page.mouse.move(to.x + to.width / 2, to.y + to.height / 2);
		await page.waitForTimeout(35);
		await page.mouse.up();
		await expect(page.locator('#dnd-action-dragged-el')).toHaveCount(0);
		await expect(page.locator('[data-issue-card="i1"]')).toHaveCount(1);
		await expect(page.locator(`div[data-issue-drop-zone="${statusA1}"]`)).toContainText('Alpha todo issue');
		await expect(page.locator('[data-issue-shadow="true"]')).toHaveCount(0);
		expect(patches).toEqual([]);
		expect(unhandled).toEqual([]);
	});
}
