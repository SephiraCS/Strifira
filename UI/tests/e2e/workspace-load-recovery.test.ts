import { expect, test, type Page } from '@playwright/test';

const teamId = '00000000-0000-0000-0000-000000000010';
const projectId = '00000000-0000-0000-0000-000000000020';
type LoadFailure = 401 | 403 | 404 | 503 | 'network' | null;

async function mockWorkspace(page: Page, initialFailure: LoadFailure = null) {
	const baseURL = test.info().project.use.baseURL;
	if (!baseURL) throw new Error('Workspace recovery tests require a configured baseURL');
	const state = {
		failure: initialFailure,
		workspaceName: 'Test Workspace',
		teamName: 'Engineering',
		projectName: 'Current project'
	};
	const calls = { auth: 0, members: 0, refresh: 0, authenticatedMembers: 0 };
	await page
		.context()
		.addCookies([
			{ name: 'ku-recovery-session', value: 'preserved-session', url: new URL('/', baseURL).href, httpOnly: true }
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
		if (path === '/api/workspaces/test') return route.fulfill({ json: workspace });
		if (path === '/api/workspaces/test/teams')
			return route.fulfill({
				json: [{ id: teamId, name: state.teamName, key: 'ENG', icon: 'layers', color: '#6366f1' }]
			});
		if (path === '/api/workspaces/test/projects')
			return route.fulfill({
				json: [{ id: projectId, team_id: teamId, name: state.projectName, status: 'planned', sort_order: 0 }]
			});
		if (path === '/api/notifications') return route.fulfill({ json: { notifications: [], unread_count: 0 } });
		if (path === '/api/workspaces/test/members') {
			calls.members++;
			// headers() omits security headers; inspect the complete Cookie header.
			const cookieHeader = await route.request().headerValue('cookie');
			if (cookieHeader?.split(';').some((cookie) => cookie.trim() === 'ku-recovery-session=preserved-session'))
				calls.authenticatedMembers++;
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
		await expect(page.getByRole('main')).toContainText('Something went wrong');
		await expect(page.getByRole('heading', { name: 'Projects', exact: true })).toHaveCount(0);
		await expect(page.locator('aside')).toHaveCount(0);
		await expect(page).toHaveURL(/\/test\/projects$/);
		expect(calls).toMatchObject({ auth: 1, members: 1, refresh: 0, authenticatedMembers: 1 });

		await page.getByRole('button', { name: 'Try again', exact: true }).click();
		await expect(page.getByRole('heading', { name: 'Projects', exact: true })).toBeVisible();
		await expect(page.getByRole('main').getByRole('link', { name: 'Current project' })).toBeVisible();
		await expect(page.getByText('Something went wrong', { exact: true })).toHaveCount(0);
		await expect(page).toHaveURL(/\/test\/projects$/);
		expect(calls).toMatchObject({ auth: 1, members: 2, refresh: 0, authenticatedMembers: 2 });
		expect((await page.context().cookies()).find((cookie) => cookie.name === 'ku-recovery-session')?.value).toBe(
			'preserved-session'
		);
	});
}

test('keeps the loaded view and complete shell snapshot after app refresh fails, then retries', async ({ page }) => {
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
	await expect(sidebar).toContainText('Test Workspace');
	await expect(sidebar).not.toContainText('Updated Workspace');
	await expect(sidebar.getByRole('button', { name: 'Engineering', exact: true })).toBeVisible();
	await expect(sidebarProject).toHaveText('Current project');
	await expect(page.getByRole('main').getByRole('link', { name: 'Current project' })).toBeVisible();
	expect(await currentHeading!.evaluate((node) => node.isConnected)).toBe(true);
	expect(calls).toMatchObject({ auth: 1, members: 2, refresh: 0, authenticatedMembers: 2 });

	await failure.getByRole('button', { name: 'Try again', exact: true }).click();
	await expect(failure).toHaveCount(0);
	await expect(sidebar).toContainText('Updated Workspace');
	await expect(sidebar.getByRole('button', { name: 'Updated Engineering', exact: true })).toBeVisible();
	await expect(sidebarProject).toHaveText('Updated project');
	await expect(page.getByRole('main').getByRole('link', { name: 'Current project' })).toBeVisible();
	expect(await currentHeading!.evaluate((node) => node.isConnected)).toBe(true);
	expect(calls).toMatchObject({ auth: 1, members: 3, refresh: 0, authenticatedMembers: 3 });
	await expect(page).toHaveURL(/\/test\/projects$/);
});

test('still redirects a real expired session to login when refresh returns 401', async ({ page }) => {
	const { calls } = await mockWorkspace(page, 401);
	await page.goto('/test/projects');
	await expect(page).toHaveURL(/\/login$/);
	await expect(page.getByRole('textbox', { name: 'Email' })).toBeVisible();
	expect(calls).toMatchObject({ auth: 1, members: 1, refresh: 1 });
});
