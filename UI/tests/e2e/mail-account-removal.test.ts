import { expect, test, type Page, type Route } from '@playwright/test';

const accountA = '31000000-0000-4000-8000-000000000001';
const accountB = '31000000-0000-4000-8000-000000000002';
const self = {
	id: '31000000-0000-4000-8000-000000000003',
	email: 'test@example.test',
	name: 'Removal test',
	display_name: 'Removal test',
	avatar_url: null
};
const workspace = {
	id: '31000000-0000-4000-8000-000000000004',
	name: 'Mail test',
	slug: 'test',
	owner_id: self.id,
	current_user_role: 'owner',
	created_at: '2026-01-01T00:00:00Z',
	updated_at: '2026-01-01T00:00:00Z'
};
const accounts = [accountA, accountB].map((id, i) => ({
	id,
	email_address: `mailbox-${i ? 'b' : 'a'}@example.test`,
	display_name: `Mailbox ${i ? 'B' : 'A'}`,
	auth_method: 'password',
	imap_host: 'imap.example.test',
	imap_port: 993,
	imap_security: 'tls',
	smtp_host: 'smtp.example.test',
	smtp_port: 587,
	smtp_security: 'starttls',
	username: 'fixture',
	signature: '',
	last_sync_error: '',
	created_at: '2026-01-01T00:00:00Z'
}));
const messages = [accountA, accountB].map((account_id, i) => ({
	id: `message-${i}`,
	account_id,
	folder: 'inbox',
	thread_key: `thread-${i}`,
	subject: `Message ${i ? 'B' : 'A'}`,
	from: { name: 'Sender', address: 'sender@example.test' },
	to: [],
	snippet: 'Test message',
	unread: false,
	starred: false,
	archived: false,
	spam: false,
	has_attachments: false,
	sent_at: '2026-01-01T00:00:00Z',
	reply_to: '',
	body_text: `Body ${i ? 'B' : 'A'}`,
	body_html: '',
	in_reply_to: '',
	attachments: []
}));

async function fixture(page: Page, count = 2) {
	const state = {
		accounts: accounts.slice(0, count),
		deletes: 0,
		reject: false,
		pending: undefined as undefined | ((route: Route) => Promise<void>)
	};
	await page.route('https://raw.githubusercontent.com/**', (r) => r.fulfill({ json: [] }));
	await page.route('**/api/**', async (route) => {
		const request = route.request();
		const url = new URL(request.url());
		const path = url.pathname;
		if (path === '/api/auth/me') return route.fulfill({ json: self });
		if (path === '/api/workspaces') return route.fulfill({ json: [workspace] });
		if (path === '/api/workspaces/test') return route.fulfill({ json: workspace });
		if (path === '/api/preferences')
			return route.fulfill({ json: { theme_mode: 'dark', dark_theme: 'dark', font_size: 'default' } });
		if (path === '/api/notifications') return route.fulfill({ json: { notifications: [], unread_count: 0 } });
		if (path.endsWith('/mail/accounts')) return route.fulfill({ json: { accounts: state.accounts } });
		if (path.includes('/mail/accounts/') && request.method() === 'DELETE') {
			state.deletes++;
			if (state.pending) return state.pending(route);
			if (state.reject)
				return route.fulfill({
					status: 403,
					json: { error: { code: 'FORBIDDEN', message: 'Removal refused. Try again.' } }
				});
			state.accounts = state.accounts.filter((a) => a.id !== path.split('/').pop());
			return route.fulfill({ json: { deleted: true } });
		}
		if (path.endsWith('/mail/messages'))
			return route.fulfill({
				json: {
					messages: messages.filter(
						(m) =>
							state.accounts.some((a) => a.id === m.account_id) &&
							(!url.searchParams.get('accountId') || m.account_id === url.searchParams.get('accountId'))
					)
				}
			});
		if (path.includes('/mail/messages/'))
			return route.fulfill({ json: messages.find((m) => m.id === path.split('/').pop()) });
		if (path.endsWith('/chat/channels')) return route.fulfill({ json: { channels: [] } });
		if (path.endsWith('/chat/presence')) return route.fulfill({ json: { online: [] } });
		return route.fulfill({ json: [] });
	});
	await page.routeWebSocket('**/api/workspaces/*/ws', () => {});
	await page.goto('/test/mail');
	await expect(page.getByRole('button', { name: 'Message A' })).toBeVisible();
	return state;
}

async function confirmRemoval(page: Page) {
	await page.getByRole('button', { name: 'Account settings', exact: true }).first().click();
	const editor = page.getByRole('dialog', { name: 'Edit account', exact: true });
	await editor.getByRole('button', { name: 'Remove account', exact: true }).click();
	const confirmation = page.getByRole('alertdialog', { name: 'Remove account', exact: true });
	await expect(confirmation).toContainText('mailbox-a@example.test');
	await expect(confirmation).toContainText('mailbox and messages at your email provider will stay intact');
	return confirmation;
}

test('cancel preserves the linked account and open message', async ({ page }) => {
	const state = await fixture(page);
	await page.getByRole('button', { name: 'Message A' }).click();
	await expect(page.getByText('Body A', { exact: true }).first()).toBeVisible();
	const confirmation = await confirmRemoval(page);
	await confirmation.getByRole('button', { name: 'Cancel', exact: true }).click();
	await expect(confirmation).toHaveCount(0);
	await page
		.getByRole('dialog', { name: 'Edit account', exact: true })
		.getByRole('button', { name: 'Cancel', exact: true })
		.click();
	await expect(page.getByText('Body A', { exact: true }).first()).toBeVisible();
	expect(state.deletes).toBe(0);
	await expect(page.getByRole('button', { name: 'mailbox-a@example.test', exact: true })).toBeVisible();
});

test('failure keeps the account and confirmation available for retry, then preserves the other account', async ({
	page
}) => {
	const state = await fixture(page);
	state.reject = true;
	await page.getByRole('button', { name: 'mailbox-a@example.test', exact: true }).click();
	await page.getByRole('button', { name: 'Message A' }).click();
	const confirmation = await confirmRemoval(page);
	await confirmation.getByRole('button', { name: 'Remove', exact: true }).click();
	await expect(page.getByText('Removal refused. Try again.', { exact: true })).toBeVisible();
	await expect(confirmation).toBeVisible();
	expect(state.accounts).toHaveLength(2);
	state.reject = false;
	await confirmation.getByRole('button', { name: 'Remove', exact: true }).click();
	await expect(confirmation).toHaveCount(0);
	await expect(page.getByRole('button', { name: 'mailbox-a@example.test', exact: true })).toHaveCount(0);
	await expect(page.getByText('Body A', { exact: true })).toHaveCount(0);
	await expect(page.getByRole('button', { name: 'Message B' })).toBeVisible();
	await page.reload();
	await expect(page.getByRole('button', { name: 'mailbox-b@example.test', exact: true })).toBeVisible();
	await expect(page.getByRole('button', { name: 'mailbox-a@example.test', exact: true })).toHaveCount(0);
	expect(state.deletes).toBe(2);
});

test('last account removal waits for the response and ignores duplicate confirmation', async ({ page }) => {
	const state = await fixture(page, 1);
	let finish!: () => Promise<void>;
	state.pending = async (route) => {
		await new Promise<void>((resolve) => {
			finish = async () => {
				state.accounts = [];
				await route.fulfill({ json: { deleted: true } });
				resolve();
			};
		});
	};
	const confirmation = await confirmRemoval(page);
	const remove = confirmation.getByRole('button', { name: 'Remove', exact: true });
	await remove.click();
	await expect(remove).toHaveAttribute('aria-busy', 'true');
	await remove.click({ force: true });
	expect(state.deletes).toBe(1);
	await page.keyboard.press('Escape');
	await expect(confirmation).toBeVisible();
	await finish();
	await expect(page.getByText('Account removed', { exact: true })).toBeVisible();
	await expect(page.getByRole('button', { name: 'mailbox-a@example.test', exact: true })).toHaveCount(0);
	await expect(page.getByRole('button', { name: 'Message A' })).toHaveCount(0);
	await expect(page.getByText('Select a message', { exact: true })).toBeVisible();
	await page.reload();
	await expect(page.getByRole('button', { name: 'Message A' })).toHaveCount(0);
});

test('a late message detail cannot restore the removed account reader', async ({ page }) => {
	await fixture(page);
	let finish!: () => Promise<void>;
	await page.route('**/mail/messages/message-0', async (route) => {
		await new Promise<void>((resolve) => {
			finish = async () => {
				await route.fulfill({ json: messages[0] });
				resolve();
			};
		});
	});
	await page.getByRole('button', { name: 'Message A' }).click();
	await expect.poll(() => typeof finish).toBe('function');
	const confirmation = await confirmRemoval(page);
	await confirmation.getByRole('button', { name: 'Remove', exact: true }).click();
	await expect(confirmation).toHaveCount(0);
	await finish();
	await expect(page.getByText('Body A', { exact: true })).toHaveCount(0);
	await page.getByRole('button', { name: 'Message B' }).click();
	await expect(page.getByText('Body B', { exact: true }).first()).toBeVisible();
});

test('a late account list cannot restore a removed account', async ({ page }) => {
	await fixture(page);
	let finish!: () => Promise<void>;
	await page.route(
		'**/mail/accounts',
		async (route) => {
			await new Promise<void>((resolve) => {
				finish = async () => {
					await route.fulfill({ json: { accounts } });
					resolve();
				};
			});
		},
		{ times: 1 }
	);
	await page.evaluate(() => window.dispatchEvent(new CustomEvent('ws:reconnected')));
	await expect.poll(() => typeof finish).toBe('function');
	const confirmation = await confirmRemoval(page);
	await confirmation.getByRole('button', { name: 'Remove', exact: true }).click();
	await expect(confirmation).toHaveCount(0);
	await finish();
	await expect(page.getByRole('button', { name: 'mailbox-a@example.test', exact: true })).toHaveCount(0);
	await expect(page.getByRole('button', { name: 'mailbox-b@example.test', exact: true })).toBeVisible();
});

test('mobile account management exposes removal and cancellation', async ({ page }) => {
	await page.setViewportSize({ width: 390, height: 844 });
	// The desktop account rail is deliberately hidden on mobile.
	const state = await fixture(page);
	await page.getByRole('button', { name: 'Account settings', exact: true }).click();
	await page.getByRole('menuitem', { name: 'mailbox-a@example.test', exact: true }).click();
	const editor = page.getByRole('dialog', { name: 'Edit account', exact: true });
	await editor.getByRole('button', { name: 'Remove account', exact: true }).click();
	const confirmation = page.getByRole('alertdialog', { name: 'Remove account', exact: true });
	await expect(confirmation).toBeVisible();
	await confirmation.getByRole('button', { name: 'Cancel', exact: true }).click();
	await expect(editor).toBeVisible();
	await expect(page.getByRole('button', { name: 'Message A' })).toBeVisible();
	expect(state.deletes).toBe(0);
});
