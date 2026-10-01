import { expect, test, type Page } from '@playwright/test';

async function mockSettings(page: Page, role = 'owner') {
	const patches: Record<string, unknown>[] = [];
	let tests = 0;
	let failTest = false;
	let settings = {
		provider: 'openai_compatible',
		base_url: 'https://ai.example.test/v1',
		model: 'test-model',
		has_api_key: true,
		description_expand_prompt: 'Improve the description.',
		default_prompt: 'Improve the description.',
		issue_copy_prompt: '{{issue_xml}}',
		default_issue_copy_prompt: '{{issue_xml}}',
		created_at: '2026-01-01T00:00:00Z',
		updated_at: '2026-01-01T00:00:00Z'
	};
	await page.route('**/api/**', async (route) => {
		const req = route.request();
		const path = new URL(req.url()).pathname;
		if (path === '/api/auth/me') return route.fulfill({ json: { id: 'u1', name: 'Test', email: 'test@example.test' } });
		if (path === '/api/workspaces') return route.fulfill({ json: [{ id: 'w1', name: 'Test', slug: 'test' }] });
		if (path === '/api/workspaces/test') {
			return route.fulfill({ json: { id: 'w1', name: 'Test', slug: 'test', current_user_role: role } });
		}
		if (path === '/api/preferences') return route.fulfill({ json: {} });
		if (path === '/api/notifications') return route.fulfill({ json: { notifications: [], unread_count: 0 } });
		if (path === '/api/workspaces/test/ai-settings') {
			if (req.method() === 'PATCH') {
				const patch = req.postDataJSON();
				patches.push(patch);
				const { api_key, ...fields } = patch;
				settings = {
					...settings,
					...fields,
					has_api_key: api_key === null ? false : settings.has_api_key || !!api_key
				};
			}
			return route.fulfill({ json: settings });
		}
		if (path === '/api/workspaces/test/ai-settings/test') {
			tests++;
			return failTest
				? route.fulfill({
						status: 502,
						json: { error: { code: 'AI_PROVIDER_ERROR', message: 'Provider authentication failed' } }
					})
				: route.fulfill({ json: { model: settings.model, latency_ms: 120 } });
		}
		if (/\/(teams|projects|members|labels|views|favorites)$/.test(path)) return route.fulfill({ json: [] });
		return route.fulfill({ status: 404, json: { error: { message: `Unhandled ${path}` } } });
	});
	return {
		patches,
		get tests() {
			return tests;
		},
		fail: () => {
			failTest = true;
		}
	};
}

test('tests saved AI settings and requires saving changed connection fields', async ({ page }) => {
	const state = await mockSettings(page);
	await page.goto('/test/settings/ai');
	const testConnection = page.getByRole('button', { name: 'Test connection', exact: true });
	await expect(testConnection).toBeEnabled();
	await testConnection.click();
	await expect(page.getByRole('status').filter({ hasText: 'Connected to test-model' })).toContainText('120 ms');
	expect(state.tests).toBe(1);
	await page.getByRole('textbox', { name: 'Model', exact: true }).fill('another-model');
	await expect(testConnection).toBeDisabled();
	await expect(page.getByText('Connected to test-model')).toHaveCount(0);
	await page.getByRole('button', { name: 'Save AI settings', exact: true }).click();
	await expect(testConnection).toBeEnabled();
	await testConnection.click();
	await expect(page.getByText('Connected to another-model. Response received in 120 ms.')).toBeVisible();
	expect(state.tests).toBe(2);
});

test('API key removal is reviewable and persisted only on save', async ({ page }) => {
	const state = await mockSettings(page);
	await page.goto('/test/settings/ai');
	await page.getByRole('button', { name: 'Remove API key', exact: true }).click();
	await expect(page.getByText('The API key will be removed when you save.')).toBeVisible();
	expect(state.patches).toHaveLength(0);
	await expect(page.getByRole('button', { name: 'Test connection', exact: true })).toBeDisabled();
	await page.getByRole('button', { name: 'Keep API key', exact: true }).click();
	await expect(page.getByRole('button', { name: 'Test connection', exact: true })).toBeEnabled();
	await page.getByRole('button', { name: 'Remove API key', exact: true }).click();
	await page.getByRole('button', { name: 'Save AI settings', exact: true }).click();
	await expect.poll(() => state.patches.length).toBe(1);
	expect(state.patches[0].api_key).toBeNull();
	await expect(page.getByRole('button', { name: 'Remove API key', exact: true })).toHaveCount(0);
	await expect(page.getByRole('button', { name: 'Test connection', exact: true })).toBeDisabled();
});

test('connection failures show the provider error and allow retry', async ({ page }) => {
	const state = await mockSettings(page);
	state.fail();
	await page.goto('/test/settings/ai');
	const button = page.getByRole('button', { name: 'Test connection', exact: true });
	await button.click();
	await expect(page.getByText('Provider authentication failed')).toBeVisible();
	await expect(button).toBeEnabled();
	await expect(page.getByText(/Connected to/)).toHaveCount(0);
});

test('members can read settings without connection or credential controls', async ({ page }) => {
	const state = await mockSettings(page, 'member');
	await page.goto('/test/settings/ai');
	await expect(page.getByRole('textbox', { name: 'Model', exact: true })).toBeDisabled();
	await expect(page.getByRole('button', { name: 'Test connection', exact: true })).toHaveCount(0);
	await expect(page.getByRole('button', { name: 'Remove API key', exact: true })).toHaveCount(0);
	expect(state.tests).toBe(0);
});

test('AI settings fit a narrow viewport', async ({ page }) => {
	await page.setViewportSize({ width: 390, height: 844 });
	await mockSettings(page);
	await page.goto('/test/settings/ai');
	await expect(page.getByRole('textbox', { name: 'Model', exact: true })).toBeVisible();
	const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
	expect(overflow).toBeLessThanOrEqual(1);
});
