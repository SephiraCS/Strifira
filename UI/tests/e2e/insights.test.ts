import { expect, test, type Locator, type Page, type Route } from '@playwright/test';

async function plottedPixels(canvas: Locator) {
	return canvas.evaluate((element: HTMLCanvasElement) => {
		const context = element.getContext('2d');
		if (!context) throw new Error('Chart has no canvas context');
		const pixels = context.getImageData(0, 0, element.width, element.height).data;
		const scale = element.width / element.clientWidth;
		const plotted = new Uint8Array(element.width * element.height);
		let count = 0;
		let firstPixel = -1;
		// Ignore axis labels and legends: an empty grid must fail even when its
		// legend renders. Plotted bars, lines, and points have saturated colors.
		for (let y = Math.ceil(40 * scale); y < element.height - 40 * scale; y++) {
			for (let x = 0; x < element.width; x++) {
				const offset = (y * element.width + x) * 4;
				const [red, green, blue, alpha] = pixels.subarray(offset, offset + 4);
				if (alpha > 180 && Math.max(red, green, blue) - Math.min(red, green, blue) > 40) {
					count++;
					plotted[y * element.width + x] = 1;
					if (firstPixel < 0) firstPixel = y * element.width + x;
				}
			}
		}
		// Antialiased edge pixels can lie outside the symbol's hit geometry.
		// Hover/click its colored region's center instead of its first edge pixel.
		const pending = firstPixel < 0 ? [] : [firstPixel];
		let regionSize = 0;
		let sumX = 0;
		let sumY = 0;
		while (pending.length) {
			const index = pending.pop()!;
			if (!plotted[index]) continue;
			plotted[index] = 0;
			const x = index % element.width;
			const y = Math.floor(index / element.width);
			regionSize++;
			sumX += x + 0.5;
			sumY += y + 0.5;
			if (x > 0) pending.push(index - 1);
			if (x < element.width - 1) pending.push(index + 1);
			if (y > 0) pending.push(index - element.width);
			if (y < element.height - 1) pending.push(index + element.width);
		}
		const point = regionSize ? { x: sumX / regionSize / scale, y: sumY / regionSize / scale } : null;
		return { count, point };
	});
}

async function expectPlottedChart(page: Page, name: string, minimumPixels = 100) {
	const canvas = page.getByTestId(name).locator('canvas').first();
	await expect(canvas).toBeVisible();
	await expect.poll(async () => (await plottedPixels(canvas)).count).toBeGreaterThan(minimumPixels);
	return canvas;
}

type Team = Record<string, unknown> & { id: string; name: string };

async function mockInsights(page: Page, teamsHandler?: (route: Route, defaults: Team[]) => Promise<void>) {
	const teamId = '00000000-0000-0000-0000-000000000010';
	let scopedOverviewRequests = 0;
	const issueListQueries: URLSearchParams[] = [];
	const pageErrors: Error[] = [];
	const chartErrors: string[] = [];
	page.on('pageerror', (error) => {
		pageErrors.push(error);
	});
	page.on('console', (message) => {
		if (message.type() === 'error' && /echarts|chart|series|component.*import|renderer/i.test(message.text())) {
			chartErrors.push(message.text());
		}
	});
	await page.route('https://raw.githubusercontent.com/carbogninalberto/kuayle/main/UI/static/releases.json', (route) =>
		route.fulfill({ json: [] })
	);
	await page.route('**/api/**', async (route) => {
		const requestUrl = new URL(route.request().url());
		const path = requestUrl.pathname;
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
					team_workflow_sort_overrides: {}
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
			const defaults: Team[] = [
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
			];
			if (teamsHandler) return teamsHandler(route, defaults);
			return route.fulfill({ json: defaults });
		}
		if (path === `/api/workspaces/test/teams/${teamId}/statuses`) {
			return route.fulfill({
				json: [
					{
						id: '00000000-0000-0000-0000-000000000003',
						team_id: teamId,
						name: 'In progress',
						slug: 'in-progress',
						category: 'started',
						color: '#6366f1',
						position: 2,
						is_default: false,
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
			path === '/api/workspaces/test/views'
		) {
			return route.fulfill({ json: [] });
		}
		if (path === '/api/notifications') {
			return route.fulfill({ json: { notifications: [], unread_count: 0 } });
		}
		if (path === '/api/workspaces/test/analytics/overview') {
			if (requestUrl.searchParams.get('team_id') === teamId) scopedOverviewRequests += 1;
			return route.fulfill({
				json: {
					total_issues: 42,
					open_issues: 18,
					completed_issues: 24,
					overdue_issues: 3,
					total_projects: 4,
					total_members: 8,
					started_issues: 6,
					unassigned_issues: 2,
					completion_rate: 57.14,
					avg_lead_time_hours: 48,
					avg_cycle_time_hours: 24
				}
			});
		}
		if (path === '/api/workspaces/test/analytics/distribution') {
			return route.fulfill({
				json: {
					by_status: [
						{
							status_id: '00000000-0000-0000-0000-000000000004',
							name: 'Backlog',
							color: null,
							category: 'backlog',
							count: 5
						},
						{
							status_id: '00000000-0000-0000-0000-000000000003',
							name: 'In progress',
							color: '#6366f1',
							category: 'started',
							count: 18
						}
					],
					by_priority: [{ priority: 2, count: 12 }]
				}
			});
		}
		if (path === '/api/workspaces/test/analytics/burnup') {
			return route.fulfill({
				json: {
					interval: 'week',
					from: '2026-04-14',
					to: '2026-07-12',
					points: [
						{
							date: '2026-06-22',
							total_created: 24,
							total_completed: 12,
							scope: 12
						},
						{
							date: '2026-06-29',
							total_created: 32,
							total_completed: 18,
							scope: 14
						},
						{
							date: '2026-07-06',
							total_created: 42,
							total_completed: 24,
							scope: 18
						}
					]
				}
			});
		}
		if (path === '/api/workspaces/test/analytics/insights') {
			const requestedSlice = requestUrl.searchParams.get('slice') ?? 'none';
			const requestedMeasure = requestUrl.searchParams.get('measure') ?? 'issue_count';
			const requestedSegment = requestUrl.searchParams.get('segment') ?? 'none';
			const duration = requestedMeasure !== 'issue_count';
			const group =
				requestedSlice === 'cycle'
					? { key: '__null__', label: 'No cycle' }
					: requestedSlice === 'status_type'
						? { key: 'started', label: 'Started' }
						: requestedSlice === 'team'
							? { key: teamId, label: 'Engineering' }
							: {
									key: '00000000-0000-0000-0000-000000000003',
									label: 'In progress',
									color: '#6366f1'
								};
			return route.fulfill({
				json: {
					measure: requestedMeasure,
					slice: requestedSlice,
					segment: requestedSegment,
					unit: duration ? 'hours' : 'issues',
					total_count: 42,
					aggregate: 42,
					groups: [
						{
							...group,
							count: 18,
							value: 18,
							p50: duration ? 24 : undefined,
							segments:
								requestedSegment === 'none'
									? []
									: [
											{ key: '2', label: 'High priority', count: 10, p50: 24 },
											{ key: '3', label: 'Low priority', count: 8, p50: 48 }
										]
						}
					],
					points: duration
						? [
								{
									issue_id: 'issue-1',
									identifier: 'ENG-1',
									title: 'First issue',
									slice_key: group.key,
									value: 24
								},
								{
									issue_id: 'issue-2',
									identifier: 'ENG-2',
									title: 'Second issue',
									slice_key: group.key,
									value: 48
								}
							]
						: []
				}
			});
		}
		if (path === '/api/workspaces/test/issues') {
			issueListQueries.push(requestUrl.searchParams);
			return route.fulfill({
				json: { data: [], total_count: 0, page: 1, has_more: false }
			});
		}
		return route.fulfill({
			status: 404,
			json: { error: { message: `Unhandled ${path}` } }
		});
	});

	return {
		teamId,
		issueListQueries,
		pageErrors,
		chartErrors,
		scopedOverviewRequests: () => scopedOverviewRequests
	};
}

test('shows workspace analytics and switches team scope', async ({ page }) => {
	const { teamId, scopedOverviewRequests, pageErrors, chartErrors } = await mockInsights(page);
	await page.goto('/test/insights');
	await page.getByRole('heading', { name: 'Insights' }).waitFor();
	await expect(page.getByRole('heading', { name: 'Insights' })).toBeVisible();
	await expect(page.getByText('Total issues')).toBeVisible();
	await expect(page.getByText('42', { exact: true })).toBeVisible();
	await expect(page.getByText('Completion rate')).toBeVisible();
	await expect(page.getByText('57%')).toBeVisible();
	await expectPlottedChart(page, 'distribution-chart');
	await expectPlottedChart(page, 'burnup-chart');
	await page.getByLabel('Team scope').click();
	await page.getByRole('option', { name: 'Engineering' }).click();
	await expect(page).toHaveURL(new RegExp(`team=${teamId}`));
	await expect.poll(scopedOverviewRequests).toBeGreaterThan(0);
	await expect(page.getByText('Using Engineering custom statuses')).toBeVisible();

	await page.getByRole('tab', { name: 'Explore' }).click();
	await expect(page).toHaveURL(/tab=explore/);
	await expect(page.getByLabel('Measure')).toContainText('Issue count');
	await page.getByLabel('Group by').click();
	await page.getByRole('option', { name: 'Status', exact: true }).click();
	await expect(page.getByLabel('Group by')).toContainText('Status');
	await page.getByLabel('Date range').click();
	await expect(page.getByRole('button', { name: '90 days' })).toBeVisible();
	await page.keyboard.press('Escape');
	await expect(page.getByText('In progress', { exact: true })).toBeVisible();
	await expectPlottedChart(page, 'insights-chart');

	await expect.poll(() => pageErrors.map((error) => error.message)).toEqual([]);
	expect(chartErrors).toEqual([]);
});

test('renders duration and segmented explorer charts', async ({ page }) => {
	const { pageErrors, chartErrors } = await mockInsights(page);
	await page.goto('/test/insights?tab=explore&measure=lead_time&slice=status_type');
	const scatter = await expectPlottedChart(page, 'insights-chart', 10);
	const scatterPoint = (await plottedPixels(scatter)).point!;
	await scatter.hover({ position: scatterPoint });
	await expect(page.getByText(/hours: 1d 0h/)).toBeVisible();

	await page.goto('/test/insights?tab=explore&slice=status_type&segment=priority');
	const segmentedBars = await expectPlottedChart(page, 'insights-chart');
	await segmentedBars.hover({
		position: (await plottedPixels(segmentedBars)).point!
	});
	await expect(page.getByText('High priority', { exact: false })).toBeVisible();

	await expect.poll(() => pageErrors.map((error) => error.message)).toEqual([]);
	expect(chartErrors).toEqual([]);
});

test('drills down from cycle, status and team groups', async ({ page }) => {
	const { teamId, issueListQueries, pageErrors, chartErrors } = await mockInsights(page);
	await page.goto('/test/insights?tab=explore&slice=cycle');
	await expect(page.getByText('No cycle', { exact: true })).toBeVisible();
	await page.getByText('No cycle', { exact: true }).click();
	await expect(page).toHaveURL('/test/my-issues?cycle=none');
	await expect.poll(() => issueListQueries.length).toBeGreaterThan(0);
	const drillDownQuery = issueListQueries.at(-1)!;
	expect(drillDownQuery.get('cycle')).toBe('none');
	expect(drillDownQuery.has('assignee')).toBe(false);
	expect(drillDownQuery.has('creator')).toBe(false);

	await page.goto('/test/insights?tab=explore&slice=status_type');
	const statusTypeQueryCount = issueListQueries.length;
	const statusBars = await expectPlottedChart(page, 'insights-chart');
	await statusBars.click({
		position: (await plottedPixels(statusBars)).point!
	});
	await expect(page).toHaveURL('/test/my-issues?status_type=started');
	await expect.poll(() => issueListQueries.length).toBeGreaterThan(statusTypeQueryCount);
	const statusTypeQuery = issueListQueries.at(-1)!;
	expect(statusTypeQuery.get('status_type')).toBe('started');
	expect(statusTypeQuery.has('assignee')).toBe(false);
	expect(statusTypeQuery.has('creator')).toBe(false);

	await page.goto('/test/insights?tab=explore&slice=team');
	const teamQueryCount = issueListQueries.length;
	await page.getByRole('row').filter({ hasText: 'Engineering' }).click();
	await expect(page).toHaveURL(`/test/my-issues?team=${teamId}`);
	await expect.poll(() => issueListQueries.length).toBeGreaterThan(teamQueryCount);
	expect(issueListQueries.at(-1)!.get('team')).toBe(teamId);

	await expect.poll(() => pageErrors.map((error) => error.message)).toEqual([]);
	expect(chartErrors).toEqual([]);
});

test('renders the explorer on mobile', async ({ page }) => {
	const { pageErrors, chartErrors } = await mockInsights(page);
	await page.setViewportSize({ width: 390, height: 844 });
	await page.goto('/test/insights?tab=explore');
	await expect(page.getByRole('tab', { name: 'Explore' })).toBeVisible();
	await expect(page.getByLabel('Measure')).toBeVisible();
	await expectPlottedChart(page, 'insights-chart');
	await expect.poll(() => pageErrors.map((error) => error.message)).toEqual([]);
	expect(chartErrors).toEqual([]);
});

test('a teams response invalidated while in flight never overwrites the refreshed list', async ({ page }) => {
	// Teams requests are answered by the test: each waits until it is released.
	const pending: { route: Route; release: (teams: Team[]) => Promise<void> }[] = [];
	let defaultTeams: Team[] = [];
	let latestTeams: Team[] | null = null;
	let holdTeams = false;
	// Keep the realtime connection up so finished requests are shareable.
	await page.routeWebSocket('**/api/workspaces/*/ws', () => {});
	await mockInsights(page, async (route, defaults) => {
		defaultTeams = defaults;
		if (!holdTeams) return route.fulfill({ json: latestTeams ?? defaults });
		pending.push({ route, release: (teams) => route.fulfill({ json: teams }) });
	});
	await page.goto('/test/projects');
	await expect(page.getByRole('heading', { name: 'Projects' })).toBeVisible();

	// The layout reloads teams; that request is still pending when insights
	// mounts and shares it.
	holdTeams = true;
	const refreshTeams = () =>
		page.evaluate(() =>
			window.dispatchEvent(new CustomEvent('app:refresh', { detail: { slug: 'test', resources: ['teams'] } }))
		);
	await refreshTeams();
	await expect.poll(() => pending.length).toBe(1);
	await page.locator('body').click({ position: { x: 1, y: 1 } });
	await page.keyboard.press('g');
	await page.keyboard.press('a');
	await expect(page.getByRole('heading', { name: 'Insights' })).toBeVisible();

	// A second refresh replaces it; the replacement answers with the renamed
	// team before the original answers with the old name.
	await refreshTeams();
	await expect.poll(() => pending.length).toBe(2);
	const renamed = defaultTeams.map((team) => ({ ...team, name: 'Platform' }));
	latestTeams = renamed;
	holdTeams = false;
	await pending[1].release(renamed);
	await pending[0].release(defaultTeams);

	await page.getByLabel('Team scope').click();
	await expect(page.getByRole('option', { name: 'Platform' })).toBeVisible();
	await expect(page.getByRole('option', { name: 'Engineering' })).toHaveCount(0);
});
