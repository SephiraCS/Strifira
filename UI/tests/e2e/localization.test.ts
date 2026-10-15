import { execFile } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { promisify } from 'node:util';
import { test, expect, type Page } from '@playwright/test';

const uiRoot = fileURLToPath(new URL('../..', import.meta.url));
const teamId = '00000000-0000-0000-0000-000000000010';
const statusId = '00000000-0000-0000-0000-0000000000a1';

// The locale check is the CI gate that keeps every messages/*.json in step
// with en.json; running it here makes the e2e job fail on a drifted locale.
test('every locale has exactly the keys of en.json', async () => {
	const { stdout } = await promisify(execFile)(process.execPath, ['scripts/check-locales.mjs'], { cwd: uiRoot });
	expect(stdout).toContain('Locale check passed');
});

async function mockWorkspace(page: Page) {
	const created: unknown[] = [];

	await page.route('https://raw.githubusercontent.com/**/releases.json', (route) => route.fulfill({ json: [] }));
	await page.route('**/api/**', async (route) => {
		const request = route.request();
		const url = new URL(request.url());
		const path = url.pathname;
		if (!path.startsWith('/api/')) return route.continue();

		if (path === '/api/auth/me') {
			return route.fulfill({
				json: { id: 'user-1', email: 'test@example.com', name: 'Test User', display_name: 'Test User', avatar_url: null }
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
					default_view_layout: 'list'
				}
			});
		}
		if (path === '/api/workspaces') {
			return route.fulfill({ json: [{ id: 'workspace-1', name: 'Test Workspace', slug: 'test' }] });
		}
		if (path === '/api/workspaces/test') {
			return route.fulfill({
				json: {
					id: 'workspace-1',
					name: 'Test Workspace',
					slug: 'test',
					logo_url: null,
					current_user_role: 'owner',
					created_at: '2026-01-01T00:00:00Z',
					updated_at: '2026-01-01T00:00:00Z'
				}
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
						created_at: '2026-01-01T00:00:00Z',
						updated_at: '2026-01-01T00:00:00Z'
					}
				]
			});
		}
		if (path === `/api/workspaces/test/teams/${teamId}/statuses`) {
			return route.fulfill({
				json: [{ id: statusId, team_id: teamId, name: 'Todo', category: 'unstarted', color: null, position: 0, is_default: true }]
			});
		}
		if (path === '/api/workspaces/test/issues' && request.method() === 'POST') {
			const body = request.postDataJSON() as { title: string };
			const issue = {
				id: 'issue-1',
				identifier: 'ENG-1',
				title: body.title,
				team_id: teamId,
				status_id: statusId,
				status: 'todo',
				status_info: { id: statusId, name: 'Todo', category: 'unstarted', color: null, position: 0 },
				priority: 0,
				labels: [],
				assignees: [],
				sort_order: 0,
				sub_issue_count: 0,
				created_at: '2026-01-01T00:00:00Z',
				updated_at: '2026-01-01T00:00:00Z'
			};
			created.push(issue);
			return route.fulfill({ status: 201, json: issue });
		}
		if (path === '/api/workspaces/test/issues') {
			return route.fulfill({ json: { data: [], total_count: 0, page: 1, has_more: false } });
		}
		if (/^\/api\/workspaces\/test\/(projects|labels|members|views|issue-templates)$/.test(path)) {
			return route.fulfill({ json: [] });
		}
		if (path === `/api/workspaces/test/teams/${teamId}/cycles`) {
			return route.fulfill({ json: [] });
		}
		if (path === '/api/notifications') {
			return route.fulfill({ json: { notifications: [], unread_count: 0 } });
		}
		return route.fulfill({ status: 404, json: { error: { code: 'NOT_FOUND', message: `Unhandled ${path}` } } });
	});

	return created;
}

for (const { locale, title, view, copyLink, placeholder } of [
	{ locale: 'it', title: 'Attività creata', view: 'Visualizza attività', copyLink: 'Copia link attività', placeholder: 'Titolo attività' },
	{ locale: 'pt-BR', title: 'Issue criada', view: 'Ver issue', copyLink: 'Copiar link da issue', placeholder: 'Título da issue' },
	{ locale: 'zh-CN', title: '议题已创建', view: '查看议题', copyLink: '复制议题链接', placeholder: '议题标题' }
]) {
	test(`the issue created toast is translated (${locale})`, async ({ page }) => {
		await page.addInitScript((value) => localStorage.setItem('PARAGLIDE_LOCALE', value), locale);
		const created = await mockWorkspace(page);

		await page.goto(`/test/teams/${teamId}`);
		await expect(page.getByRole('link', { name: 'Engineering' }).first()).toBeVisible();

		await page.keyboard.press('c');
		const titleInput = page.getByPlaceholder(placeholder);
		await expect(titleInput).toBeVisible();
		await titleInput.fill('Localized toast');
		await titleInput.press('Control+Enter');

		await expect.poll(() => created.length).toBe(1);
		const toast = page.locator('.issue-created-toast');
		await expect(toast).toBeVisible();
		await expect(toast).toContainText(title);
		await expect(toast).toContainText('ENG-1');
		await expect(toast.getByRole('link', { name: view })).toBeVisible();
		// The copy commands appear on hover.
		await toast.hover();
		await expect(toast.getByRole('button', { name: copyLink })).toBeVisible();
		await expect(toast).not.toContainText('Issue created');
	});
}
