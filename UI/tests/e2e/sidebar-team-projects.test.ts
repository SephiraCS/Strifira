import { expect, test, type Page } from '@playwright/test';

const teams = [
	{ id: 'team-a', name: 'Engineering', key: 'ENG' },
	{ id: 'team-b', name: 'Design', key: 'DES' },
	{ id: 'team-empty', name: 'Empty Team', key: 'EMP' }
];
const projects = [
	{ id: 'project-a', team_id: 'team-a', name: 'Website', status: 'planned' },
	{ id: 'project-a2', team_id: 'team-a', name: 'Dashboard', status: 'planned' },
	{ id: 'project-b', team_id: 'team-b', name: 'Brand Kit', status: 'planned' }
];

async function mockWorkspace(page: Page) {
	await page.route('https://raw.githubusercontent.com/**/releases.json', (route) => route.fulfill({ json: [] }));
	await page.route('**/api/**', (route) => {
		const path = new URL(route.request().url()).pathname;
		if (path === '/api/auth/me') return route.fulfill({ json: { id: 'user', name: 'Test User' } });
		if (path === '/api/preferences') return route.fulfill({ json: { theme_mode: 'dark', dark_theme: 'dark' } });
		const workspaces = ['test', 'other'].map((slug) => ({
			id: `workspace-${slug}`,
			name: slug,
			slug,
			current_user_role: 'owner'
		}));
		if (path === '/api/workspaces') return route.fulfill({ json: workspaces });
		const workspace = workspaces.find((w) => path === `/api/workspaces/${w.slug}`);
		if (workspace) return route.fulfill({ json: workspace });
		if (/\/workspaces\/[^/]+\/teams$/.test(path)) return route.fulfill({ json: teams });
		if (/\/workspaces\/[^/]+\/projects$/.test(path)) return route.fulfill({ json: projects });
		if (path.endsWith('/issues'))
			return route.fulfill({ json: { data: [], total_count: 0, page: 1, per_page: 50, has_more: false } });
		if (path.endsWith('/projects/project-a')) return route.fulfill({ json: projects[0] });
		if (path.endsWith('/notifications')) return route.fulfill({ json: { notifications: [], unread_count: 0 } });
		return route.fulfill({ json: [] });
	});
}

const toggle = (page: Page, team: string) =>
	page.locator('aside').getByRole('button', { name: new RegExp(`projects for ${team}$`) });
const child = (page: Page, id: string, slug = 'test') => page.locator(`aside a[href="/${slug}/projects/${id}"]`);

test.beforeEach(async ({ page }) => {
	await mockWorkspace(page);
});

test('keyboard toggles only one list, preserves route and focus, and persists through navigation and reload', async ({
	page
}) => {
	await page.goto('/test/projects/project-a');
	await expect(page.getByRole('main')).toContainText('Website');
	const engineering = toggle(page, 'Engineering');
	await expect(engineering).toHaveAttribute('aria-expanded', 'true');
	await expect(child(page, 'project-a')).toBeVisible();
	await engineering.focus();
	await page.keyboard.press('Space');
	await expect(engineering).toHaveAttribute('aria-expanded', 'false');
	await expect(engineering).toBeFocused();
	await expect(child(page, 'project-a')).toBeHidden();
	await expect(child(page, 'project-b')).toBeVisible();
	await expect(page).toHaveURL(/\/test\/projects\/project-a$/);
	const controlledId = await engineering.getAttribute('aria-controls');
	await expect(page.locator(`[id="${controlledId}"]`)).toHaveAttribute('hidden', '');
	await expect(page.locator('aside a[href="/test/teams/team-a"]')).toBeVisible();
	await expect(page.locator('aside a[href="/test/teams/team-a/cycles"]')).toBeVisible();
	await expect(page.locator('aside a[href="/test/teams/team-a/views"]')).toBeVisible();
	await page.locator('aside a[href="/test/teams/team-a/projects"]').click();
	await expect(page).toHaveURL(/\/test\/teams\/team-a\/projects$/);
	await expect(engineering).toHaveAttribute('aria-expanded', 'false');
	await page.reload();
	await expect(engineering).toHaveAttribute('aria-expanded', 'false');
	await expect(toggle(page, 'Design')).toHaveAttribute('aria-expanded', 'true');
	await engineering.focus();
	await page.keyboard.press('Enter');
	await expect(child(page, 'project-a')).toBeVisible();
	await child(page, 'project-a').click();
	await expect(page).toHaveURL(/\/test\/projects\/project-a$/);
	await expect(engineering).toHaveAttribute('aria-expanded', 'true');
	await expect(toggle(page, 'Empty Team')).toHaveCount(0);
	await expect(page.locator('aside a[href="/test/teams/team-empty/projects"]')).toBeVisible();
});

test('workspace ids isolate choices even when team ids and names match', async ({ page }) => {
	await page.goto('/test/projects');
	await toggle(page, 'Engineering').click();
	await page.goto('/other/projects');
	await expect(toggle(page, 'Engineering')).toHaveAttribute('aria-expanded', 'true');
	await expect(child(page, 'project-a', 'other')).toBeVisible();
	await toggle(page, 'Design').click();
	await page.goto('/test/projects');
	await expect(toggle(page, 'Engineering')).toHaveAttribute('aria-expanded', 'false');
	await expect(toggle(page, 'Design')).toHaveAttribute('aria-expanded', 'true');
});

test('storage write failure still allows repeated toggles', async ({ page }) => {
	await page.addInitScript(() => {
		const setItem = Storage.prototype.setItem;
		Storage.prototype.setItem = function (key, value) {
			if (key.startsWith('sidebar_team_projects_')) throw new DOMException('Storage unavailable', 'QuotaExceededError');
			return setItem.call(this, key, value);
		};
	});
	await page.goto('/test/projects');
	await toggle(page, 'Engineering').click();
	await expect(child(page, 'project-a')).toBeHidden();
	await toggle(page, 'Engineering').click();
	await expect(child(page, 'project-a')).toBeVisible();
});

test.describe('mobile', () => {
	test.use({ viewport: { width: 390, height: 844 }, hasTouch: true });
	test('touch keeps the drawer open and shares choices with desktop', async ({ page }) => {
		await page.goto('/test/projects');
		await page.getByRole('button', { name: 'Open navigation', exact: true }).tap();
		const drawer = page.getByRole('dialog');
		const engineering = drawer.getByRole('button', { name: /projects for Engineering$/ });
		await engineering.tap();
		await expect(drawer).toBeVisible();
		await expect(engineering).toHaveAttribute('aria-expanded', 'false');
		await expect(drawer.locator('a[href="/test/projects/project-a"]')).toBeHidden();
		await expect(drawer.locator('a[href="/test/projects/project-b"]')).toBeVisible();
		await expect(drawer.getByRole('button', { name: /projects for Empty Team$/ })).toHaveCount(0);
		await page.keyboard.press('Escape');
		await page.setViewportSize({ width: 1280, height: 1000 });
		await expect(toggle(page, 'Engineering')).toHaveAttribute('aria-expanded', 'false');
		await toggle(page, 'Engineering').click();
		await page.setViewportSize({ width: 390, height: 844 });
		await page.getByRole('button', { name: 'Open navigation', exact: true }).tap();
		await expect(engineering).toHaveAttribute('aria-expanded', 'true');
		await drawer.locator('a[href="/test/projects/project-a"]').tap();
		await expect(drawer).toBeHidden();
		await expect(page).toHaveURL(/\/test\/projects\/project-a$/);
		await page.getByRole('button', { name: 'Open navigation', exact: true }).tap();
		await drawer.locator('a[href="/test/teams/team-a/projects"]').tap();
		await expect(page).toHaveURL(/\/test\/teams\/team-a\/projects$/);
		await expect(drawer).toBeHidden();
	});
});
