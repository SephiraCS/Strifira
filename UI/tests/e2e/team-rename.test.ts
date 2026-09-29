import { test, expect, type Page } from '@playwright/test';

const teamId = '00000000-0000-0000-0000-0000000000aa';
const todoStatus = '00000000-0000-0000-0000-00000000b001';

const team = {
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
};

const issue = (id: string, identifier: string, title: string) => ({
	id,
	identifier,
	title,
	team_id: teamId,
	status_id: todoStatus,
	status: 'todo',
	priority: 0,
	labels: [],
	assignees: [],
	sort_order: 0,
	sub_issue_count: 0,
	created_at: '2026-01-01T00:00:00Z',
	updated_at: '2026-01-01T00:00:00Z'
});

async function mockTeamPage(page: Page, opts: { patchStatus?: number; patchBody?: unknown } = {}) {
	const patchBodies: string[] = [];
	await page.route('https://raw.githubusercontent.com/**/releases.json', (route) => route.fulfill({ json: [] }));
	await page.route('**/api/**', (route) => {
		const path = new URL(route.request().url()).pathname;
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
					issues_group_by: 'status'
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
			return route.fulfill({ json: [team] });
		}
		if (path === `/api/workspaces/test/teams/${teamId}` && route.request().method() === 'PATCH') {
			patchBodies.push(route.request().postData() ?? '');
			if (opts.patchStatus && opts.patchStatus !== 200) {
				return route.fulfill({ status: opts.patchStatus, json: opts.patchBody });
			}
			const body = JSON.parse(route.request().postData() ?? '{}');
			return route.fulfill({ json: { ...team, ...body, updated_at: '2026-09-29T00:00:00Z' } });
		}
		if (path === `/api/workspaces/test/teams/${teamId}/statuses`) {
			return route.fulfill({
				json: [
					{ id: todoStatus, team_id: teamId, name: 'Todo', slug: 'todo', category: 'unstarted', color: null, position: 0, is_default: true, created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-01T00:00:00Z' }
				]
			});
		}
		if (path === '/api/workspaces/test/forgejo/status') {
			return route.fulfill({ json: { connected: false, repos: [] } });
		}
		if (path === '/api/workspaces/test/dev-machine-scope-setting') {
			return route.fulfill({ json: { forgejo_repo_id: null, environment_id: null } });
		}
		if (path === '/api/workspaces/test/dev-machine-environments') {
			return route.fulfill({ json: [] });
		}
		if (['/api/workspaces/test/projects', '/api/workspaces/test/labels', '/api/workspaces/test/members', '/api/workspaces/test/views', `/api/workspaces/test/teams/${teamId}/cycles`].includes(path)) {
			return route.fulfill({ json: [] });
		}
		if (path === '/api/notifications') {
			return route.fulfill({ json: { notifications: [], unread_count: 0 } });
		}
		if (path === '/api/workspaces/test/issues') {
			return route.fulfill({
				json: {
					data: [issue('i1', 'ENG-1', 'First issue'), issue('i2', 'ENG-2', 'Second issue')],
					total_count: 2,
					page: 1,
					per_page: 50,
					has_more: false
				}
			});
		}
		return route.fulfill({ status: 404, json: { error: { message: `Unhandled ${path}` } } });
	});
	return patchBodies;
}

test('rename team and change identifier sends name + key and confirms the cascade', async ({ page }) => {
	const patchBodies = await mockTeamPage(page);

	await page.goto(`/test/teams/${teamId}`);
	await expect(page.getByText('First issue')).toBeVisible();

	await page.getByRole('button', { name: 'Rename team / change identifier' }).click();
	const dialog = page.getByRole('dialog');
	await expect(dialog).toBeVisible();

	await dialog.getByLabel('Name').fill('Platform');
	await dialog.getByLabel('Identifier').fill('new');

	// The hint previews the identifier switch.
	await expect(dialog.getByText('1–10 uppercase letters')).toContainText('ENG-12');
	await expect(dialog.getByText('1–10 uppercase letters')).toContainText('NEW-12');

	await dialog.getByRole('button', { name: 'Save changes' }).click();

	await expect.poll(() => patchBodies.length).toBe(1);
	expect(JSON.parse(patchBodies[0])).toEqual({ name: 'Platform', key: 'NEW' });
	await expect(page.getByText('Team renamed — all tickets updated.')).toBeVisible();
	await expect(dialog).toBeHidden();
});

test('identifier collision keeps the dialog open with a clear message', async ({ page }) => {
	const patchBodies = await mockTeamPage(page, {
		patchStatus: 409,
		patchBody: { error: { code: 'TEAM_KEY_COLLISION', message: 'Another team already uses identifiers under this key' } }
	});

	await page.goto(`/test/teams/${teamId}`);
	await page.getByRole('button', { name: 'Rename team / change identifier' }).click();
	const dialog = page.getByRole('dialog');
	await dialog.getByLabel('Identifier').fill('oth');
	await dialog.getByRole('button', { name: 'Save changes' }).click();

	await expect(dialog.getByText('Another team already uses identifiers under this key.')).toBeVisible();
	await expect(dialog).toBeVisible();
	expect(patchBodies).toHaveLength(1);
});

test('settings team page edit changes the identifier through the same endpoint', async ({ page }) => {
	const patchBodies = await mockTeamPage(page);

	await page.goto(`/test/settings/teams/${teamId}`);
	// Read-only General card shows the current identifier.
	await expect(page.getByText('ENG').first()).toBeVisible();

	await page.getByRole('button', { name: 'Edit' }).click();
	const keyInput = page.locator('input[placeholder="ENG"]');
	await expect(keyInput).toBeVisible();
	await expect(keyInput).toHaveValue('ENG');

	await keyInput.fill('new');
	// The input uppercases and the hint previews the identifier switch.
	await expect(keyInput).toHaveValue('NEW');
	await expect(page.getByText('1–10 uppercase letters')).toContainText('NEW-12');

	await page.getByRole('button', { name: 'Save', exact: true }).click();

	await expect.poll(() => patchBodies.length).toBe(1);
	expect(JSON.parse(patchBodies[0])).toEqual({ name: 'Engineering', description: null, key: 'NEW' });
	await expect(page.getByText('Team details updated')).toBeVisible();
	// The form closes and the read-only card shows the new identifier.
	await expect(page.locator('input[placeholder="ENG"]')).toBeHidden();
	await expect(page.getByText('NEW').first()).toBeVisible();
});

test('settings team page identifier collision keeps the form open', async ({ page }) => {
	const patchBodies = await mockTeamPage(page, {
		patchStatus: 409,
		patchBody: { error: { code: 'TEAM_KEY_COLLISION', message: 'Another team already uses identifiers under this key' } }
	});

	await page.goto(`/test/settings/teams/${teamId}`);
	await page.getByRole('button', { name: 'Edit' }).click();
	const keyInput = page.locator('input[placeholder="ENG"]');
	await keyInput.fill('oth');
	await page.getByRole('button', { name: 'Save', exact: true }).click();

	await expect(page.getByText('Another team already uses identifiers under this key.')).toBeVisible();
	await expect(keyInput).toBeVisible();
	expect(patchBodies).toHaveLength(1);
});
