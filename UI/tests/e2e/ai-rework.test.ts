import { test, expect, type Page } from '@playwright/test';

const teamId = '00000000-0000-0000-0000-000000000010';

const issue = {
	id: 'i1',
	team_id: teamId,
	identifier: 'ENG-1',
	title: 'Issue with AI',
	description: '<p>Short description</p>',
	status: 'todo',
	status_info: { id: 'todo', name: 'Todo', category: 'unstarted', color: '#888888' },
	priority: 0,
	labels: [],
	assignees: [],
	created_at: '2026-01-01T00:00:00Z',
	updated_at: '2026-01-01T00:00:00Z'
};

async function mockWorkspace(page: Page, opts: { aiConfigured: boolean; reworkRequests?: string[] }) {
	await page.route('https://raw.githubusercontent.com/**/releases.json', (route) => route.fulfill({ json: [] }));
	await page.route('**/api/**', (route) => {
		const url = new URL(route.request().url());
		const path = url.pathname;
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
		if (path === '/api/workspaces/test/ai-settings') {
			return route.fulfill({
				json: {
					provider: 'openai_compatible',
					base_url: opts.aiConfigured ? 'https://ai.example.test/v1' : '',
					model: opts.aiConfigured ? 'test-model' : '',
					has_api_key: opts.aiConfigured,
					description_expand_prompt: 'Expand this issue description.',
					default_prompt: 'Expand this issue description.',
					issue_copy_prompt: '',
					default_issue_copy_prompt: '',
					created_at: '2026-01-01T00:00:00Z',
					updated_at: '2026-01-01T00:00:00Z'
				}
			});
		}
		if (path === '/api/workspaces/test/issues/ENG-1/expand-description') {
			if (opts.reworkRequests) {
				opts.reworkRequests.push(`${route.request().method()} ${route.request().postData() ?? ''}`);
			}
			return route.fulfill({ json: { description: '<p>Expanded by AI</p>' } });
		}
		if (path === '/api/workspaces/test/teams') {
			return route.fulfill({ json: [{ id: teamId, name: 'Engineering', key: 'ENG', color: '#6366f1', icon: 'layers' }] });
		}
		if (
			['/api/workspaces/test/projects', '/api/workspaces/test/labels', '/api/workspaces/test/members', '/api/workspaces/test/views', `/api/workspaces/test/teams/${teamId}/statuses`, `/api/workspaces/test/teams/${teamId}/cycles`, '/api/workspaces/test/issues/ENG-1/comments', '/api/workspaces/test/issues/ENG-1/history'].includes(path)
		) {
			return route.fulfill({ json: [] });
		}
		if (path === '/api/notifications') {
			return route.fulfill({ json: { notifications: [], unread_count: 0 } });
		}
		if (path === '/api/workspaces/test/issues/ENG-1' || path === '/api/workspaces/test/issues') {
			return route.fulfill({ json: path.endsWith('/issues') ? { data: [issue], total_count: 1, page: 1, has_more: false } : issue });
		}
		return route.fulfill({ status: 404, json: { error: { message: `Unhandled ${path}` } } });
	});
}

test.describe('AI rework', () => {
	test('issue page exposes a persistent Improve with AI button that reworks the description', async ({ page }) => {
		const reworkRequests: string[] = [];
		await mockWorkspace(page, { aiConfigured: true, reworkRequests });

		await page.goto('/test/issue/ENG-1');

		const improve = page.getByRole('button', { name: 'Improve with AI' });
		await expect(improve).toBeVisible();
		await expect(improve).toHaveAttribute('title', 'Improve with AI');

		await improve.click();

		await expect.poll(() => reworkRequests.length).toBe(1);
		await expect(page.locator('.rich-editor').first()).toContainText('Expanded by AI');
	});

	test('clicking Improve with AI on an unconfigured workspace shows a setup hint instead of a generic failure', async ({ page }) => {
		const reworkRequests: string[] = [];
		await mockWorkspace(page, { aiConfigured: false, reworkRequests });

		await page.goto('/test/issue/ENG-1');

		const improve = page.getByRole('button', { name: 'Improve with AI' });
		await expect(improve).toBeVisible();
		// The tooltip points at the configuration path instead of the action.
		await expect(improve).toHaveAttribute('title', /configure/i);

		await improve.click();

		await expect(page.getByText('AI is not configured')).toBeVisible();
		expect(reworkRequests).toHaveLength(0);
	});
});
