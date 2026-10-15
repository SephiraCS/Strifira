import { expect, test, type Page } from '@playwright/test';

const teamId = '00000000-0000-0000-0000-000000000010';
const projectId = '00000000-0000-0000-0000-000000000020';
type LoadFailure = 401 | 403 | 404 | 503 | 'network' | null;

async function mockWorkspace(
	page: Page,
	initialFailure: LoadFailure = null,
	options: { shellFailure?: LoadFailure; holdMembers?: Promise<void>; holdNotifications?: Promise<void> } = {}
) {
	const baseURL = test.info().project.use.baseURL;
	if (!baseURL) throw new Error('Workspace recovery tests require a configured baseURL');
	const state = {
		failure: initialFailure,
		shellFailure: options.shellFailure ?? null,
		workspaceName: 'Test Workspace',
		teamName: 'Engineering',
		projectName: 'Current project'
	};
	const calls = {
		auth: 0,
		members: 0,
		refresh: 0,
		authenticatedMembers: 0,
		shell: 0,
		notifications: 0,
		projects: 0,
		completedMembers: 0,
		completedNotifications: 0
	};
	let holdMembers = options.holdMembers;
	let holdNotifications = options.holdNotifications;
	await page
		.context()
		.addCookies([
			{ name: 'strifira-recovery-session', value: 'preserved-session', url: new URL('/', baseURL).href, httpOnly: true }
		]);
	await page.route('https://raw.githubusercontent.com/**/releases.json', (route) => route.fulfill({ json: [] }));
	await page.route('**/api/**', async (route) => {
		const path = new URL(route.request().url()).pathname;
		if (path === '/api/auth/me') {
			calls.auth++;
			return route.fulfill({
				json: { id: 'user-one', name: 'Test User', display_name: 'Test User', email: 'test@example.com' }
			});
		}
		if (path === '/api/auth/refresh') {
			calls.refresh++;
			return route.fulfill({ status: 401, json: { error: { message: 'Session expired' } } });
		}
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
			name: state.workspaceName,
			slug: 'test',
			current_user_role: 'owner',
			screen_sharing_enabled: false
		};
		if (path === '/api/workspaces') return route.fulfill({ json: [workspace] });
		if (path === '/api/workspaces/test') {
			calls.shell++;
			const failure = state.shellFailure;
			state.shellFailure = null;
			if (failure === 'network') return route.abort('failed');
			if (failure) return route.fulfill({ status: failure, json: { error: { message: 'Workspace unavailable' } } });
			return route.fulfill({ json: workspace });
		}
		if (path === '/api/workspaces/test/teams')
			return route.fulfill({
				json: [{ id: teamId, name: state.teamName, key: 'ENG', icon: 'layers', color: '#6366f1' }]
			});
		if (path === '/api/workspaces/test/projects') {
			calls.projects++;
			return route.fulfill({
				json: [{ id: projectId, team_id: teamId, name: state.projectName, status: 'planned', sort_order: 0 }]
			});
		}
		if (path === '/api/notifications') {
			calls.notifications++;
			const held = holdNotifications;
			holdNotifications = undefined;
			if (held) await held;
			calls.completedNotifications++;
			return route.fulfill({ json: { notifications: [], unread_count: 0 } });
		}
		if (path === '/api/workspaces/test/members') {
			calls.members++;
			// headers() omits security headers; inspect the complete Cookie header.
			const cookieHeader = await route.request().headerValue('cookie');
			if (cookieHeader?.split(';').some((cookie) => cookie.trim() === 'strifira-recovery-session=preserved-session'))
				calls.authenticatedMembers++;
			const held = holdMembers;
			holdMembers = undefined;
			if (held) await held;
			calls.completedMembers++;
			const failure = state.failure;
			state.failure = null;
			if (failure === 'network') return route.abort('failed');
			if (failure) return route.fulfill({ status: failure, json: { error: { message: 'Members unavailable' } } });
			return route.fulfill({ json: [{ user_id: 'user-one', name: 'Test User', role: 'owner' }] });
		}
		return route.fulfill({ json: [] });
	});
	return { state, calls };
}

for (const failure of [503, 403, 404, 'network'] as const) {
	test(`keeps an authenticated workspace load retryable after members ${failure}`, async ({ page }) => {
		const { calls } = await mockWorkspace(page, failure);
		await page.goto('/test/projects');
		await expect(page.getByRole('main').getByRole('alert')).toContainText('Something went wrong');
		await expect(page.getByRole('heading', { name: 'Projects', exact: true })).toBeVisible();
		await expect(page.locator('aside')).toBeVisible();
		await expect(page).toHaveURL(/\/test\/projects$/);
		expect(calls).toMatchObject({ auth: 1, members: 1, refresh: 0, authenticatedMembers: 1 });

		await page.getByRole('button', { name: 'Try again', exact: true }).click();
		await expect(page.getByRole('heading', { name: 'Projects', exact: true })).toBeVisible();
		await expect(page.getByRole('main').getByRole('link', { name: 'Current project' })).toBeVisible();
		await expect(page.getByText('Something went wrong', { exact: true })).toHaveCount(0);
		await expect(page).toHaveURL(/\/test\/projects$/);
		expect(calls).toMatchObject({ auth: 1, members: 2, refresh: 0, authenticatedMembers: 2 });
		expect((await page.context().cookies()).find((cookie) => cookie.name === 'strifira-recovery-session')?.value).toBe(
			'preserved-session'
		);
	});
}

test('keeps the loaded view and independently refreshes healthy collections after members fails, then retries', async ({
	page
}) => {
	const { state, calls } = await mockWorkspace(page);
	await page.goto('/test/projects');
	const heading = page.getByRole('heading', { name: 'Projects', exact: true });
	await expect(heading).toBeVisible();
	const currentHeading = await heading.elementHandle();
	const sidebar = page.locator('aside');
	await expect(sidebar.getByRole('button', { name: 'Engineering', exact: true })).toBeVisible();
	const sidebarProject = sidebar.locator(`a[href="/test/projects/${projectId}"]`);
	await expect(sidebarProject).toHaveText('Current project');
	state.failure = 503;
	state.workspaceName = 'Updated Workspace';
	state.teamName = 'Updated Engineering';
	state.projectName = 'Updated project';

	await page.evaluate(() => window.dispatchEvent(new CustomEvent('app:refresh', { detail: { slug: 'test' } })));
	const failure = page.getByRole('main').getByRole('alert');
	await expect(failure).toContainText('Something went wrong');
	await expect(page).toHaveURL(/\/test\/projects$/);
	await expect(sidebar).toContainText('Updated Workspace');
	await expect(sidebar.getByRole('button', { name: 'Updated Engineering', exact: true })).toBeVisible();
	await expect(sidebarProject).toHaveText('Updated project');
	await expect(page.getByRole('main').getByRole('link', { name: 'Current project' })).toBeVisible();
	expect(await currentHeading!.evaluate((node) => node.isConnected)).toBe(true);
	expect(calls).toMatchObject({ auth: 1, members: 2, refresh: 0, authenticatedMembers: 2 });
	const healthyReads = { shell: calls.shell, projects: calls.projects, notifications: calls.notifications };

	await failure.getByRole('button', { name: 'Try again', exact: true }).click();
	await expect(failure).toHaveCount(0);
	await expect(sidebar).toContainText('Updated Workspace');
	await expect(sidebar.getByRole('button', { name: 'Updated Engineering', exact: true })).toBeVisible();
	await expect(sidebarProject).toHaveText('Updated project');
	await expect(page.getByRole('main').getByRole('link', { name: 'Current project' })).toBeVisible();
	expect(await currentHeading!.evaluate((node) => node.isConnected)).toBe(true);
	expect(calls).toMatchObject({ auth: 1, members: 3, refresh: 0, authenticatedMembers: 3 });
	expect({ shell: calls.shell, projects: calls.projects, notifications: calls.notifications }).toEqual(healthyReads);
	await expect(page).toHaveURL(/\/test\/projects$/);
});

test('keeps an initial shell failure retryable without refreshing successful ancillary data', async ({ page }) => {
	const { calls } = await mockWorkspace(page, null, { shellFailure: 503 });
	await page.goto('/test/projects');
	await expect(page.getByRole('main')).toContainText('Something went wrong');
	await expect(page.getByRole('heading', { name: 'Projects', exact: true })).toHaveCount(0);
	await expect(page.locator('aside')).toHaveCount(0);
	await expect(page).toHaveURL(/\/test\/projects$/);
	await expect.poll(() => calls.completedMembers).toBe(1);
	await page.getByRole('button', { name: 'Try again', exact: true }).click();
	await expect(page.getByRole('heading', { name: 'Projects', exact: true })).toBeVisible();
	await expect(page.getByRole('main').getByRole('link', { name: 'Current project' })).toBeVisible();
	expect(calls).toMatchObject({ auth: 1, shell: 2, members: 1, refresh: 0, authenticatedMembers: 1 });
});

for (const resource of ['members', 'notifications'] as const) {
	test(`renders the usable shell while ${resource} remains deliberately stalled`, async ({ page }) => {
		let release!: () => void;
		const held = new Promise<void>((resolve) => (release = resolve));
		const { calls } = await mockWorkspace(
			page,
			null,
			resource === 'members' ? { holdMembers: held } : { holdNotifications: held }
		);
		try {
			await page.goto('/test/projects', { waitUntil: 'domcontentloaded' });
			await expect(page.getByRole('heading', { name: 'Projects', exact: true })).toBeVisible();
			await expect(page.getByRole('main').getByRole('link', { name: 'Current project' })).toBeVisible();
			await expect(page.locator('aside').getByRole('button', { name: 'Engineering', exact: true })).toBeVisible();
			await expect(page.locator('aside').locator(`a[href="/test/projects/${projectId}"]`)).toHaveText(
				'Current project'
			);
			expect(resource === 'members' ? calls.completedMembers : calls.completedNotifications).toBe(0);
			expect(resource === 'members' ? calls.completedNotifications : calls.completedMembers).toBe(1);
			await expect(page).toHaveURL(/\/test\/projects$/);
		} finally {
			release();
		}
		await expect.poll(() => (resource === 'members' ? calls.completedMembers : calls.completedNotifications)).toBe(1);
		expect(calls).toMatchObject({ auth: 1, members: 1, refresh: 0, authenticatedMembers: 1 });
	});
}

test('retries failed members independently while notifications remains stalled', async ({ page }) => {
	let release!: () => void;
	const held = new Promise<void>((resolve) => (release = resolve));
	const { calls } = await mockWorkspace(page, 503, { holdNotifications: held });
	try {
		await page.goto('/test/projects', { waitUntil: 'domcontentloaded' });
		await expect(page.getByRole('heading', { name: 'Projects', exact: true })).toBeVisible();
		const failure = page.getByRole('main').getByRole('alert');
		await expect(failure).toContainText('Something went wrong');
		await failure.getByRole('button', { name: 'Try again', exact: true }).click();
		await expect(failure).toHaveCount(0);
		expect(calls).toMatchObject({
			auth: 1,
			shell: 1,
			members: 2,
			refresh: 0,
			authenticatedMembers: 2,
			notifications: 1,
			completedNotifications: 0
		});
		await expect(page.getByRole('main').getByRole('link', { name: 'Current project' })).toBeVisible();
	} finally {
		release();
	}
	await expect.poll(() => calls.completedNotifications).toBe(1);
});

test('still redirects a real expired session to login when refresh returns 401', async ({ page }) => {
	const { calls } = await mockWorkspace(page, 401);
	await page.goto('/test/projects');
	await expect(page).toHaveURL(/\/login$/);
	await expect(page.getByRole('textbox', { name: 'Email' })).toBeVisible();
	expect(calls).toMatchObject({ auth: 1, members: 1, refresh: 1 });
});
