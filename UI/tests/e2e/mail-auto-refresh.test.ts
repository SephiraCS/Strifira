import { expect, test, type Page, type Route, type WebSocketRoute } from '@playwright/test';

const accounts = ['a', 'b'].map((key) => ({
	id: `account-${key}`,
	email_address: `mailbox-${key}@example.test`,
	display_name: `Mailbox ${key.toUpperCase()}`,
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
function message(id: string, extra = {}) {
	return {
		id,
		account_id: accounts[0].id,
		folder: 'inbox',
		thread_key: id,
		subject: `Subject ${id}`,
		from: { name: 'Test sender', address: 'sender@example.test' },
		to: [],
		cc: [],
		snippet: `Preview ${id}`,
		unread: true,
		starred: false,
		archived: false,
		spam: false,
		has_attachments: false,
		sent_at: '2026-01-01T12:00:00Z',
		body_text: `Body ${id}`,
		body_html: '',
		reply_to: '',
		in_reply_to: '',
		attachments: [],
		...extra
	};
}
async function fixture(page: Page, initial = [message('original', { unread: false })]) {
	const state = {
		messages: initial,
		accounts: [...accounts],
		sockets: [] as WebSocketRoute[],
		lists: 0,
		accountLists: 0,
		patches: [] as string[],
		fail: false,
		pending: undefined as ((route: Route) => Promise<void>) | undefined
	};
	await page.route('https://raw.githubusercontent.com/**', (r) => r.fulfill({ json: [] }));
	await page.routeWebSocket('**/api/workspaces/*/ws', (ws) => {
		state.sockets.push(ws);
	});
	await page.route('**/api/**', async (route) => {
		const req = route.request(),
			url = new URL(req.url()),
			path = url.pathname;
		const self = {
			id: 'test-user',
			email: 'test@example.test',
			name: 'Refresh test',
			display_name: 'Refresh test',
			avatar_url: null
		};
		const workspace = {
			id: 'workspace-test',
			name: 'Mail test',
			slug: 'test',
			owner_id: self.id,
			current_user_role: 'owner',
			created_at: '2026-01-01T00:00:00Z',
			updated_at: '2026-01-01T00:00:00Z'
		};
		if (path === '/api/auth/me') return route.fulfill({ json: self });
		if (path === '/api/workspaces')
			return route.fulfill({ json: [workspace, { ...workspace, id: 'other-workspace', slug: 'other' }] });
		if (/^\/api\/workspaces\/(test|other)$/.test(path))
			return route.fulfill({ json: { ...workspace, slug: path.split('/').pop() } });
		if (path === '/api/preferences')
			return route.fulfill({ json: { theme_mode: 'dark', dark_theme: 'dark', font_size: 'default' } });
		if (path === '/api/notifications') return route.fulfill({ json: { notifications: [], unread_count: 0 } });
		if (path.endsWith('/mail/accounts')) {
			state.accountLists++;
			return route.fulfill({ json: { accounts: path.includes('/other/') ? [] : state.accounts } });
		}
		if (path.includes('/mail/accounts/') && req.method() === 'DELETE') {
			const id = path.split('/').pop();
			state.accounts = state.accounts.filter((a) => a.id !== id);
			state.messages = state.messages.filter((m) => m.account_id !== id);
			return route.fulfill({ json: { deleted: true } });
		}
		if (path.endsWith('/mail/messages')) {
			state.lists++;
			if (state.pending) return state.pending(route);
			if (state.fail) return route.fulfill({ status: 503, json: { error: { message: 'Temporary test failure' } } });
			const account = url.searchParams.get('accountId'),
				q = url.searchParams.get('q') ?? '',
				folder = url.searchParams.get('folder');
			const inAccount = state.messages.filter((m) => !account || m.account_id === account);
			const messages = path.includes('/other/')
				? []
				: inAccount.filter(
						(m) =>
							(!folder || m.folder === folder) &&
							m.subject.includes(q) &&
							['starred', 'archived', 'spam'].every(
								(flag) => !url.searchParams.has(flag) || String(m[flag as 'starred']) === url.searchParams.get(flag)
							)
					);
			return route.fulfill({
				json: {
					messages: messages.slice(0, 100),
					unread_inbox_count: inAccount.filter((m) => m.folder === 'inbox' && m.unread && !m.archived && !m.spam).length
				}
			});
		}
		if (path.includes('/mail/messages/')) {
			const m = state.messages.find((m) => m.id === path.split('/').pop());
			if (req.method() === 'PATCH' && m) {
				state.patches.push(m.id);
				Object.assign(m, req.postDataJSON());
			}
			return route.fulfill({ json: m });
		}
		if (path.includes('/mail/drafts'))
			return route.fulfill({
				json: { id: 'draft-test', account_id: accounts[0].id, ...req.postDataJSON(), attachments: [] }
			});
		if (path.endsWith('/chat/channels')) return route.fulfill({ json: { channels: [] } });
		if (path.endsWith('/chat/presence')) return route.fulfill({ json: { online: [] } });
		return route.fulfill({ json: [] });
	});
	await page.goto('/test/mail');
	await expect(page.getByRole('button', { name: accounts[0].email_address, exact: true })).toBeVisible();
	await expect.poll(() => state.lists).toBeGreaterThan(0);
	return state;
}
function notify(state: Awaited<ReturnType<typeof fixture>>, payload = {}) {
	state.sockets
		.at(-1)!
		.send(JSON.stringify({ type: 'mail.updated', payload: { account_id: accounts[0].id, new_count: 1, ...payload } }));
}
const row = (page: Page, id: string) => page.getByRole('button', { name: `Subject ${id}`, includeHidden: true });
const inbox = (page: Page) => page.getByRole('button', { name: /^Inbox(?:\s|$)/, includeHidden: true });

test('the first incoming message updates an empty mailbox through the workspace socket', async ({ page }) => {
	const state = await fixture(page, []);
	await expect(page.getByText('No messages here', { exact: true })).toBeVisible();
	state.messages = [message('first')];
	notify(state);
	await expect(row(page, 'first')).toBeVisible();
	await expect(inbox(page)).toHaveText(/^\s*Inbox\s*1\s*$/);
	expect(state.patches).toEqual([]);
});

test('new conversations and replies update previews and order without replacing the reader or draft focus', async ({
	page
}) => {
	const state = await fixture(page, [
		message('original', { unread: false }),
		message('second', { unread: false, sent_at: '2026-01-01T13:00:00Z' })
	]);
	await row(page, 'original').click();
	await expect(page.getByText('Body original', { exact: true }).first()).toBeVisible();
	await page.getByRole('button', { name: 'Compose', exact: true }).click();
	const draft = page.getByRole('dialog');
	await draft.getByLabel('Subject', { exact: true }).fill('Keep my subject');
	const body = draft.locator('textarea');
	await body.fill('Keep writing this draft');
	state.messages.push(
		message('reply', {
			thread_key: 'original',
			subject: 'Subject original',
			snippet: 'Latest reply preview',
			sent_at: '2026-01-01T14:00:00Z'
		})
	);
	state.messages.push(message('new', { sent_at: '2026-01-01T15:00:00Z' }));
	for (let i = 0; i < 8; i++) notify(state);
	await expect(inbox(page)).toHaveText(/^\s*Inbox\s*2\s*$/);
	await expect(page.getByText('Latest reply preview', { exact: true })).toBeVisible();
	await expect(row(page, 'original')).toHaveCount(1);
	await expect(body).toHaveValue('Keep writing this draft');
	await expect(body).toBeFocused();
	await expect(draft.getByLabel('Subject', { exact: true })).toHaveValue('Keep my subject');
	await page.keyboard.press('Escape');
	await expect(page.getByText('Body original', { exact: true }).first()).toBeVisible();
	expect(state.patches).toEqual([]);
	const subjects = await page.getByRole('button', { name: /Subject (new|original|second)/ }).allTextContents();
	expect(subjects.map((s) => s.match(/Subject (new|original|second)/)?.[0])).toEqual([
		'Subject new',
		'Subject original',
		'Subject second'
	]);
});

test('search and folder keep their selection while the inbox counter includes messages outside the visible page', async ({
	page
}) => {
	const state = await fixture(page);
	await page.getByRole('button', { name: accounts[0].email_address, exact: true }).click();
	await page.getByRole('button', { name: 'Sent', exact: true }).click();
	state.messages.push(
		...Array.from({ length: 105 }, (_, i) => message(`incoming-${i}`)),
		message('other-account', { account_id: accounts[1].id })
	);
	notify(state);
	await expect(inbox(page)).toHaveText(/^\s*Inbox\s*99\+\s*$/);
	await expect(row(page, 'incoming-0')).toHaveCount(0);
	await page.getByRole('button', { name: /^Inbox/ }).click();
	const search = page.getByPlaceholder('Search mail…');
	await search.fill('original');
	await expect(row(page, 'original')).toBeVisible();
	state.messages.push(message('filtered-out'));
	notify(state);
	await expect.poll(() => state.lists).toBeGreaterThan(3);
	await expect(search).toHaveValue('original');
	await expect(search).toBeFocused();
	await expect(row(page, 'filtered-out')).toHaveCount(0);
	await expect(row(page, 'other-account')).toHaveCount(0);
});

test('reconnection and returning to the visible tab recover events missed while disconnected', async ({ page }) => {
	const state = await fixture(page);
	state.messages.push(message('offline'));
	await state.sockets[0].close();
	await expect(row(page, 'offline')).toBeVisible({ timeout: 10_000 });
	state.messages.push(message('hidden'));
	await page.evaluate(() => document.dispatchEvent(new Event('visibilitychange')));
	await expect(row(page, 'hidden')).toBeVisible();
});

test('periodic reconciliation recovers a silently missed event and failures keep the cached reader', async ({
	page
}) => {
	await page.clock.install();
	const state = await fixture(page);
	await row(page, 'original').click();
	state.messages.push(message('missed'));
	state.fail = true;
	notify(state);
	await page.clock.runFor(400);
	await expect(row(page, 'original')).toBeVisible();
	await expect(page.getByText('Body original', { exact: true }).first()).toBeVisible();
	state.fail = false;
	await page.clock.runFor(46_000);
	await expect(row(page, 'missed')).toBeVisible();
	await expect(page.getByText('Body original', { exact: true }).first()).toBeVisible();
});

test('the reading pane survives when arrivals push its message outside the list page', async ({ page }) => {
	const state = await fixture(page);
	await row(page, 'original').click();
	state.messages = Array.from({ length: 100 }, (_, i) => message(`new-${i}`));
	notify(state);
	await expect(row(page, 'new-0')).toBeVisible();
	await expect(row(page, 'original')).toHaveCount(0);
	await expect(page.getByText('Body original', { exact: true }).first()).toBeVisible();
	expect(state.patches).toEqual([]);
});

test('late list responses cannot replace a newer account or search selection', async ({ page }) => {
	const state = await fixture(page);
	let finish!: () => Promise<void>;
	state.pending = async (route) => {
		state.pending = undefined;
		await new Promise<void>((resolve) => {
			finish = async () => {
				await route.fulfill({ json: { messages: [message('stale')], unread_inbox_count: 1 } }).catch(() => {});
				resolve();
			};
		});
	};
	notify(state);
	await expect.poll(() => typeof finish).toBe('function');
	await page.getByRole('button', { name: accounts[1].email_address, exact: true }).click();
	await expect(row(page, 'original')).toHaveCount(0);
	await finish();
	await expect(row(page, 'stale')).toHaveCount(0);
	state.messages.push(message('b', { account_id: accounts[1].id }));
	notify(state, { account_id: accounts[1].id });
	await expect(row(page, 'b')).toBeVisible();
	await expect(row(page, 'original')).toHaveCount(0);
});

test('removed account events are ignored and background work ends after logout', async ({ page }) => {
	await page.clock.install();
	const state = await fixture(page);
	notify(state, { deleted: true });
	await page.clock.runFor(500);
	await expect(row(page, 'original')).toHaveCount(0);
	const lists = state.lists;
	notify(state);
	await page.clock.runFor(500);
	expect(state.lists).toBe(lists);
	await page.getByRole('button', { name: 'R', exact: true }).click();
	await page.getByRole('button', { name: 'Log out', exact: true }).click();
	await expect(page).toHaveURL(/\/login$/);
	const afterLeave = state.lists;
	await page.clock.runFor(46_000);
	expect(state.lists).toBe(afterLeave);
});

test('events from another workspace cannot refresh or populate the active mailbox', async ({ page }) => {
	await page.clock.install();
	const state = await fixture(page);
	const lists = state.lists;
	await page.evaluate(() => {
		window.dispatchEvent(
			new CustomEvent('ws:mail.updated', { detail: { workspaceSlug: 'other', account_id: 'other-account' } })
		);
		window.dispatchEvent(new CustomEvent('ws:reconnected', { detail: { workspaceSlug: 'other' } }));
	});
	await page.clock.runFor(500);
	expect(state.lists).toBe(lists);
});

test('reading during an in-flight refresh keeps read flags and never reads the new arrival', async ({ page }) => {
	const state = await fixture(page, [message('original')]);
	let release!: () => Promise<void>;
	state.pending = async (route) => {
		state.pending = undefined;
		await new Promise<void>((resolve) => {
			release = async () => {
				await route.fulfill({ json: { messages: [message('original')], unread_inbox_count: 1 } }).catch(() => {});
				resolve();
			};
		});
	};
	notify(state);
	await expect.poll(() => typeof release).toBe('function');
	await row(page, 'original').click();
	await expect.poll(() => state.patches.length).toBe(1);
	await release();
	state.messages.push(message('unread-arrival'));
	notify(state);
	await expect(row(page, 'unread-arrival')).toBeVisible();
	await expect(inbox(page)).toHaveText(/^\s*Inbox\s*1\s*$/);
	await expect(page.getByText('Body original', { exact: true }).first()).toBeVisible();
	expect(state.patches).toEqual(['original']);
});
