import { expect, test, type Page, type WebSocketRoute } from '@playwright/test';

const projectId = 'project-a';
const projectPath = `/test/projects/${projectId}`;
const team = { id: 'team-a', name: 'Engineering', key: 'ENG' };

async function mockProjects(page: Page, role = 'member') {
	let project = {
		id: projectId,
		team_id: team.id,
		name: 'Website',
		status: 'planned',
		description: 'Keep the description'
	};
	const patches: unknown[] = [];
	let failure = 0;
	let socket: WebSocketRoute | undefined;
	await page.routeWebSocket(/\/api\/workspaces\/test\/ws/, (ws) => {
		socket = ws;
	});
	await page.route('https://raw.githubusercontent.com/**/releases.json', (route) => route.fulfill({ json: [] }));
	await page.route('**/api/**', async (route) => {
		const path = new URL(route.request().url()).pathname;
		const workspace = { id: 'workspace-test', name: 'Test Workspace', slug: 'test', current_user_role: role };
		if (path === '/api/auth/me') return route.fulfill({ json: { id: 'user', name: 'Test User' } });
		if (path === '/api/preferences') return route.fulfill({ json: { theme_mode: 'dark', dark_theme: 'dark' } });
		if (path === '/api/workspaces') return route.fulfill({ json: [workspace] });
		if (path === '/api/workspaces/test') return route.fulfill({ json: workspace });
		if (path.endsWith('/teams')) return route.fulfill({ json: [team] });
		if (path.endsWith('/projects')) return route.fulfill({ json: [project] });
		if (path.endsWith(`/projects/${projectId}`)) {
			if (route.request().method() === 'PATCH') {
				const body = route.request().postDataJSON();
				patches.push(body);
				if (failure)
					return route.fulfill({
						status: failure,
						json: { error: { code: failure === 403 ? 'FORBIDDEN' : 'INTERNAL_ERROR' } }
					});
				project = { ...project, ...body };
			}
			return route.fulfill({ json: project });
		}
		if (path.endsWith('/issues'))
			return route.fulfill({ json: { data: [], total_count: 0, page: 1, per_page: 50, has_more: false } });
		if (path.endsWith('/notifications')) return route.fulfill({ json: { notifications: [], unread_count: 0 } });
		if (path.endsWith('/github/status')) return route.fulfill({ json: { connected: false, repos: [] } });
		if (path.endsWith('/dev-machine-scope-setting'))
			return route.fulfill({ json: { github_repo_id: null, environment_id: null } });
		return route.fulfill({ json: [] });
	});
	return {
		patches,
		fail: (status: number) => {
			failure = status;
		},
		connected: () => !!socket,
		renameRemotely: (name: string) => {
			project = { ...project, name };
			socket!.send(JSON.stringify({ type: 'project.updated', payload: project }));
		}
	};
}

const rename = (page: Page) => page.getByRole('button', { name: 'Rename project', exact: true });
const nameField = (page: Page) => page.getByRole('dialog').getByRole('textbox', { name: 'Name', exact: true });
const save = (page: Page) => page.getByRole('dialog').getByRole('button', { name: 'Save', exact: true });
const toggle = (page: Page) => page.locator('aside').getByRole('button', { name: /projects for Engineering$/ });

test('keyboard save updates header, sidebar and lists, retains collapse state and persists at the existing URL', async ({
	page
}) => {
	const api = await mockProjects(page);
	await page.goto(projectPath);
	await toggle(page).click();
	await rename(page).focus();
	await page.keyboard.press('Enter');
	await expect(nameField(page)).toBeFocused();
	await expect(nameField(page)).toHaveValue('Website');
	await nameField(page).fill('  Portal  ');
	await page.keyboard.press('Enter');
	await expect(page.getByRole('dialog')).toBeHidden();
	expect(api.patches).toEqual([{ name: 'Portal' }]);
	await expect(page.getByRole('main').getByRole('navigation', { name: 'Projects' })).toContainText('Portal');
	await expect(toggle(page)).toHaveAttribute('aria-expanded', 'false');
	await expect(page).toHaveURL(projectPath);
	await toggle(page).click();
	await expect(page.locator(`aside a[href="${projectPath}"]`)).toHaveText('Portal');
	await page.locator('aside a[href="/test/projects"]').click();
	await expect(page.locator(`main a[href="${projectPath}"]`)).toContainText('Portal');
	await page.locator('aside a[href="/test/teams/team-a/projects"]').click();
	await expect(page.locator(`main a[href="${projectPath}"]`)).toContainText('Portal');
	await page.locator(`main a[href="${projectPath}"]`).click();
	await page.reload();
	await expect(page.getByRole('main')).toContainText('Portal');
	await expect(page.getByRole('main')).toContainText('Keep the description');
});

test('cancel, Escape and unchanged save make no request; empty and overlong names stay editable', async ({ page }) => {
	const api = await mockProjects(page);
	await page.goto(projectPath);
	await rename(page).click();
	await nameField(page).fill('Discard');
	await page.getByRole('dialog').getByRole('button', { name: 'Cancel', exact: true }).click();
	await rename(page).click();
	await expect(nameField(page)).toHaveValue('Website');
	await nameField(page).fill('Discard via Escape');
	await page.keyboard.press('Escape');
	await expect(page.getByRole('dialog')).toBeHidden();
	await expect(rename(page)).toBeFocused();
	await rename(page).click();
	await save(page).click();
	await expect(page.getByRole('dialog')).toBeHidden();
	for (const name of ['', '   ', 'x'.repeat(201)]) {
		if (!(await page.getByRole('dialog').isVisible())) await rename(page).click();
		await nameField(page).fill(name);
		await save(page).click();
		await expect(page.getByRole('dialog').getByRole('alert')).toHaveText('Enter a name between 1 and 200 characters.');
		await expect(nameField(page)).toHaveValue(name);
	}
	expect(api.patches).toEqual([]);
	await nameField(page).fill('界'.repeat(200));
	await save(page).click();
	await expect(page.getByRole('dialog')).toBeHidden();
	expect(api.patches).toEqual([{ name: '界'.repeat(200) }]);
});

test('server failure and revoked permission preserve the draft and allow retry', async ({ page }) => {
	const api = await mockProjects(page);
	await page.goto(projectPath);
	await rename(page).click();
	await nameField(page).fill('Draft name');
	for (const status of [500, 403]) {
		api.fail(status);
		await save(page).click();
		await expect(page.getByRole('dialog').getByRole('alert')).not.toBeEmpty();
		await expect(nameField(page)).toHaveValue('Draft name');
		await expect(page.getByRole('main')).toContainText('Website');
	}
	api.fail(0);
	await save(page).click();
	await expect(page.getByRole('dialog')).toBeHidden();
	await expect(page.getByRole('main')).toContainText('Draft name');
});

test('guest has no rename action', async ({ page }) => {
	await mockProjects(page, 'guest');
	await page.goto(projectPath);
	await expect(page.getByRole('main')).toContainText('Website');
	await expect(rename(page)).toHaveCount(0);
});

test('realtime rename refreshes the header and open lists without overwriting an unfinished draft', async ({
	page
}) => {
	const api = await mockProjects(page);
	await page.goto(projectPath);
	await expect.poll(api.connected).toBe(true);
	await rename(page).click();
	await nameField(page).fill('Unfinished draft');
	api.renameRemotely('Renamed remotely');
	await expect(page.getByRole('main')).toContainText('Renamed remotely');
	await expect(nameField(page)).toHaveValue('Unfinished draft');
	await page.keyboard.press('Escape');
	for (const path of ['/test/projects', '/test/teams/team-a/projects']) {
		await page.locator(`aside a[href="${path}"]`).click();
		await expect(page.locator(`main a[href="${projectPath}"]`)).toContainText('Renamed remotely');
		api.renameRemotely('Live list name');
		await expect(page.locator(`main a[href="${projectPath}"]`)).toContainText('Live list name');
		api.renameRemotely('Renamed remotely');
	}
});

test.describe('mobile', () => {
	test.use({ viewport: { width: 390, height: 844 }, hasTouch: true });
	test('touch rename remains reachable with a long name and preserves the drawer collapse choice', async ({ page }) => {
		await mockProjects(page);
		await page.goto(projectPath);
		await page.getByRole('button', { name: 'Open navigation', exact: true }).tap();
		const drawer = page.getByRole('dialog');
		await drawer.getByRole('button', { name: /projects for Engineering$/ }).tap();
		await page.keyboard.press('Escape');
		await rename(page).tap();
		await nameField(page).fill('Projeto com nome longo '.repeat(8).trim());
		await save(page).tap();
		await expect(page.getByRole('dialog')).toBeHidden();
		await expect(rename(page)).toBeInViewport();
		await rename(page).tap();
		await page.getByRole('dialog').getByRole('button', { name: 'Cancel', exact: true }).tap();
		await page.getByRole('button', { name: 'Open navigation', exact: true }).tap();
		await expect(drawer.getByRole('button', { name: /projects for Engineering$/ })).toHaveAttribute(
			'aria-expanded',
			'false'
		);
	});
});
