import { expect, test, type Page } from '@playwright/test';

const self = {
	id: '00000000-0000-4000-8000-000000000001',
	email: 'mailbox@example.test',
	name: 'Test user',
	display_name: 'Test user',
	avatar_url: null
};
const workspace = {
	id: '00000000-0000-4000-8000-000000000002',
	name: 'Mail test',
	slug: 'test',
	owner_id: self.id,
	current_user_role: 'owner',
	created_at: '2026-01-01T00:00:00Z',
	updated_at: '2026-01-01T00:00:00Z'
};
const discovered = {
	email_address: self.email,
	provider: 'Mail provider',
	source: 'autoconfig',
	authentication: 'password',
	incoming: { host: 'imap.example.test', port: 993, security: 'tls', username: self.email },
	outgoing: { host: 'smtp.example.test', port: 587, security: 'starttls', username: self.email }
};

async function mailPage(page: Page) {
	await page.route('https://raw.githubusercontent.com/**', (route) => route.fulfill({ json: [] }));
	await page.route('**/api/**', async (route) => {
		const path = new URL(route.request().url()).pathname;
		if (path === '/api/auth/me') return route.fulfill({ json: self });
		if (path === '/api/workspaces') return route.fulfill({ json: [workspace] });
		if (path === '/api/workspaces/test') return route.fulfill({ json: workspace });
		if (path === '/api/preferences')
			return route.fulfill({ json: { theme_mode: 'dark', dark_theme: 'dark', font_size: 'default' } });
		if (path === '/api/notifications') return route.fulfill({ json: { notifications: [], unread_count: 0 } });
		if (path.endsWith('/mail/accounts')) return route.fulfill({ json: { accounts: [] } });
		if (path.endsWith('/mail/messages')) return route.fulfill({ json: { messages: [] } });
		if (path.endsWith('/chat/channels')) return route.fulfill({ json: { channels: [] } });
		if (path.endsWith('/chat/presence')) return route.fulfill({ json: { online: [] } });
		return route.fulfill({ json: [] });
	});
	await page.routeWebSocket('**/api/workspaces/*/ws', () => {});
	await page.goto('/test/mail');
	await page.getByRole('button', { name: 'Add account', exact: true }).click();
	return page.getByRole('dialog', { name: 'Add account' });
}

test('discovers from the login address and retries failed discovery without losing it', async ({ page }) => {
	const dialog = await mailPage(page);
	const address = dialog.getByLabel('E-mail address');
	await expect(address).toHaveValue(self.email);
	await expect(dialog.getByLabel('Password', { exact: true })).toHaveCount(0);
	await expect(dialog.locator('#mail-imap-host, #mail-smtp-host, #mail-account-username')).toHaveCount(0);
	let attempts = 0;
	await page.route('**/mail/accounts/discover', (route) => {
		attempts++;
		return attempts === 1
			? route.fulfill({
					status: 503,
					json: {
						error: { code: 'MAIL_DISCOVERY_UNAVAILABLE', message: 'Provider temporarily unavailable. Try again.' }
					}
				})
			: route.fulfill({ json: discovered });
	});
	await dialog.getByRole('button', { name: 'Continue', exact: true }).click();
	await expect(dialog.getByRole('alert')).toHaveText('Provider temporarily unavailable. Try again.');
	await expect(address).toHaveValue(self.email);
	await dialog.getByRole('button', { name: 'Continue', exact: true }).click();
	await expect(dialog.getByText('Mail provider identified')).toBeVisible();
	await expect(dialog.getByLabel('Password', { exact: true })).toBeVisible();
	await expect(dialog.getByLabel('Password', { exact: true })).toHaveValue('');
});

test('retries authentication and only sends identification and mailbox credentials', async ({ page }) => {
	const dialog = await mailPage(page);
	await page.route('**/mail/accounts/discover', (route) => route.fulfill({ json: discovered }));
	await dialog.getByRole('button', { name: 'Continue', exact: true }).click();
	let attempts = 0;
	await page.route('**/mail/accounts', (route) => {
		if (route.request().method() !== 'POST') return route.fulfill({ json: { accounts: [] } });
		const body = route.request().postDataJSON();
		expect(Object.keys(body).sort()).toEqual(['display_name', 'email_address', 'password', 'signature']);
		expect(body.email_address).toBe(self.email);
		attempts++;
		return attempts === 1
			? route.fulfill({
					status: 422,
					json: { error: { code: 'MAIL_AUTHENTICATION_FAILED', message: 'Mailbox authentication refused.' } }
				})
			: route.fulfill({ status: 201, json: { id: 'new-account', ...body } });
	});
	const password = dialog.getByLabel('Password', { exact: true });
	await password.fill('test-only-invalid-credential');
	await dialog.getByRole('button', { name: 'Connect account' }).click();
	await expect(dialog.getByRole('alert')).toHaveText('Mailbox authentication refused.');
	await expect(dialog.getByLabel('E-mail address')).toHaveValue(self.email);
	await expect(dialog).not.toContainText('test-only-invalid-credential');
	await password.fill('test-only-valid-credential');
	await dialog.getByRole('button', { name: 'Connect account' }).click();
	await expect(dialog).not.toBeVisible();
	expect(attempts).toBe(2);
});

test('requires provider authorization and clears discovery when the address changes', async ({ page }) => {
	const dialog = await mailPage(page);
	let created = false;
	await page.route('**/mail/accounts/discover', (route) =>
		route.fulfill({
			json: { ...discovered, provider: 'Microsoft', authentication: 'oauth2', oauth_provider: 'microsoft' }
		})
	);
	await page.route('**/mail/accounts', (route) => {
		if (route.request().method() === 'POST') created = true;
		return route.fulfill({ json: { accounts: [] } });
	});
	await dialog.getByRole('button', { name: 'Continue', exact: true }).click();
	await expect(dialog.getByText('You will authorize access at your provider.', { exact: false })).toBeVisible();
	await page.route('**/mail/oauth/start', (route) =>
		route.fulfill({
			status: 503,
			json: { error: { code: 'MAIL_OAUTH_REGISTRATION', message: 'OAuth registration unavailable.' } }
		})
	);
	await dialog.getByRole('button', { name: 'Authorize account', exact: true }).click();
	await expect(dialog.getByRole('alert')).toHaveText('OAuth registration unavailable.');
	await expect(dialog.getByLabel('Password', { exact: true })).toHaveCount(0);
	await expect(dialog.getByRole('button', { name: 'Connect account' })).toHaveCount(0);
	await dialog.getByLabel('E-mail address').fill('another@example.test');
	await expect(dialog.getByText('Microsoft identified')).toHaveCount(0);
	await expect(dialog.getByRole('button', { name: 'Continue', exact: true })).toBeVisible();
	expect(created).toBe(false);
});

test('OAuth callback consumes state, scrubs codes and retains the address after refusal', async ({ page }) => {
	await mailPage(page);
	const state = 'state-fixture-at-least-thirty-two-characters';
	await page.evaluate(
		({ state }) =>
			sessionStorage.setItem(
				'strifira:mail-oauth',
				JSON.stringify({ workspace: 'test', state, email_address: 'retained@example.test' })
			),
		{ state }
	);
	let attempts = 0;
	await page.route('**/mail/oauth/complete', (route) => {
		attempts++;
		expect(route.request().postDataJSON()).toEqual({ state, code: 'private-code', provider_error: '' });
		return route.fulfill({
			status: 422,
			json: { error: { code: 'MAIL_OAUTH_AUTHORIZATION', message: 'Provider authorization refused.' } }
		});
	});
	await page.goto(`/mail/oauth/callback?state=${state}&code=private-code`);
	await expect(page.getByRole('alert')).toHaveText('Provider authorization refused.');
	await expect(page).toHaveURL('/mail/oauth/callback');
	await expect(page.locator('body')).not.toContainText('private-code');
	await page.getByRole('button', { name: 'Back to Mail', exact: true }).click();
	await expect(page.getByRole('dialog', { name: 'Add account' }).getByLabel('E-mail address')).toHaveValue(
		'retained@example.test'
	);
	expect(attempts).toBe(1);
});

test('OAuth callback rejects unrelated state without exchanging the code', async ({ page }) => {
	let exchanged = false;
	await page.route('**/mail/oauth/complete', (route) => {
		exchanged = true;
		return route.fulfill({ json: {} });
	});
	await page.goto('/mail/oauth/callback?state=unrelated-state&code=private-code&error_description=secret-detail');
	await expect(page.getByRole('alert')).toBeVisible();
	await expect(page).toHaveURL('/mail/oauth/callback');
	await expect(page.locator('body')).not.toContainText('secret-detail');
	expect(exchanged).toBe(false);
});
