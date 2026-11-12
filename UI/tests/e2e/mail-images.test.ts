import { expect, test, type Page } from '@playwright/test';

const png =
	'data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/lJkAAAAASUVORK5CYII=';
const cid = 'attachment:33000000-0000-4000-8000-000000000001';
async function fixture(page: Page) {
	const images: string[] = [],
		external: string[] = [];
	page.on('request', (r) => {
		if (/(tracker|images)\.example\.test/.test(r.url())) external.push(r.url());
	});
	await page.route('https://raw.githubusercontent.com/**', (r) => r.fulfill({ json: [] }));
	await page.routeWebSocket('**/api/workspaces/*/ws', () => {});
	const message = {
		id: 'images',
		account_id: 'account',
		folder: 'inbox',
		thread_key: 'images',
		subject: 'Message with images',
		from: { name: 'Test sender', address: 'sender@example.test' },
		to: [],
		cc: [],
		snippet: 'Images test',
		unread: false,
		starred: false,
		archived: false,
		spam: false,
		has_attachments: true,
		sent_at: '2026-01-01T12:00:00Z',
		reply_to: '',
		in_reply_to: '',
		attachments: [],
		body_text: '',
		body_html: `<h2>Readable mail</h2><p>The surrounding text stays readable.</p><img data-mail-image="${cid}" alt="CID logo" width="2000">
 <img data-mail-image="https://images.example.test/logo" alt="Remote logo"><img data-mail-image="invalid:" alt="Missing logo">
 <img data-mail-image="blocked:" alt="Unsafe logo"><img data-mail-image="https://images.example.test/missing" alt="Unavailable logo">
 <img data-mail-image="https://images.example.test/blocked" alt="Private logo"><script>window.mailImageScriptRan=true</script>
 <style>body{display:none}</style><img src="https://tracker.example.test/x" srcset="https://tracker.example.test/y" onerror="window.mailImageScriptRan=true" alt="Legacy logo">
 <a href="javascript:alert(1)">Unsafe link</a><p>End of message</p>`
	};
	const plain = {
		...message,
		id: 'plain',
		thread_key: 'plain',
		subject: 'Plain text message',
		has_attachments: false,
		body_html: '',
		body_text: 'Plain text remains readable.'
	};
	await page.route('**/api/**', async (route) => {
		const path = new URL(route.request().url()).pathname;
		if (!path.startsWith('/api/')) return route.fallback();
		const self = {
			id: 'test-user',
			email: 'test@example.test',
			name: 'Images test',
			display_name: 'Images test',
			avatar_url: null
		};
		const ws = {
			id: 'workspace',
			name: 'Images test',
			slug: 'test',
			owner_id: self.id,
			current_user_role: 'owner',
			created_at: '2026-01-01T00:00:00Z'
		};
		if (path === '/api/auth/me') return route.fulfill({ json: self });
		if (path === '/api/workspaces') return route.fulfill({ json: [ws] });
		if (path === '/api/workspaces/test') return route.fulfill({ json: ws });
		if (path === '/api/preferences')
			return route.fulfill({ json: { theme_mode: 'dark', dark_theme: 'dark', font_size: 'default' } });
		if (path === '/api/notifications') return route.fulfill({ json: { notifications: [], unread_count: 0 } });
		if (path.endsWith('/mail/accounts'))
			return route.fulfill({
				json: {
					accounts: [
						{
							id: 'account',
							email_address: 'mailbox@example.test',
							display_name: 'Test mailbox',
							signature: '',
							last_sync_error: '',
							created_at: '2026-01-01T00:00:00Z'
						}
					]
				}
			});
		if (path.endsWith('/mail/messages'))
			return route.fulfill({ json: { messages: [message, plain], unread_inbox_count: 0 } });
		if (path.endsWith('/images') && route.request().method() === 'POST') {
			const source = route.request().postDataJSON().source as string;
			images.push(source);
			if (source.endsWith('/blocked'))
				return route.fulfill({ status: 422, json: { error: { code: 'MAIL_IMAGE_BLOCKED' } } });
			if (source.endsWith('/missing'))
				return route.fulfill({ status: 502, json: { error: { code: 'MAIL_IMAGE_UNAVAILABLE' } } });
			return route.fulfill({ json: { data_url: png } });
		}
		if (path.endsWith('/mail/messages/images')) return route.fulfill({ json: message });
		if (path.endsWith('/mail/messages/plain')) return route.fulfill({ json: plain });
		if (path.endsWith('/chat/channels')) return route.fulfill({ json: { channels: [] } });
		if (path.endsWith('/chat/presence')) return route.fulfill({ json: { online: [] } });
		return route.fulfill({ json: [] });
	});
	await page.goto('/test/mail');
	await page.getByRole('button', { name: /Message with images/ }).click();
	return { images, external };
}
test('CID persists on reopen, remote consent and error alternatives preserve readable mail', async ({ page }) => {
	const state = await fixture(page);
	await expect(page.getByRole('img', { name: 'CID logo', exact: true })).toBeVisible();
	expect(state.images.every((source) => source === cid)).toBe(true);
	for (const name of [
		'Missing logo: Image reference is invalid or missing',
		'Unsafe logo: Image blocked by security policy',
		'Remote logo: Remote image awaiting permission'
	])
		await expect(page.getByRole('img', { name, exact: true })).toBeVisible();
	await page.getByRole('button', { name: 'Load remote images', exact: true }).click();
	for (const name of [
		'Remote logo',
		'Unavailable logo: Image unavailable',
		'Private logo: Image blocked by security policy'
	])
		await expect(page.getByRole('img', { name, exact: true })).toBeVisible();
	await expect(page.getByText('End of message', { exact: true }).filter({ visible: true })).toBeVisible();
	expect(state.external).toEqual([]);
	expect(
		await page.evaluate(() => Boolean((window as unknown as { mailImageScriptRan?: boolean }).mailImageScriptRan))
	).toBe(false);
	expect(await page.getByText('Unsafe link', { exact: true }).first().getAttribute('href')).toBeNull();
	await page.getByRole('button', { name: /Plain text message/ }).click();
	await expect(page.getByText('Plain text remains readable.', { exact: true }).filter({ visible: true })).toBeVisible();
	await page.getByRole('button', { name: /Message with images/ }).click();
	await expect(page.getByRole('img', { name: 'CID logo', exact: true })).toBeVisible();
	await expect(page.getByRole('button', { name: 'Load remote images', exact: true })).toBeVisible();
});
test('large images fit desktop and mobile and markup remains isolated', async ({ page }) => {
	await fixture(page);
	const image = page.getByRole('img', { name: 'CID logo', exact: true });
	await expect(image).toBeVisible();
	expect(
		await image.evaluate(
			(el) => el.getBoundingClientRect().width <= (el.getRootNode() as ShadowRoot).host.getBoundingClientRect().width
		)
	).toBe(true);
	await page.setViewportSize({ width: 390, height: 844 });
	await expect(image).toBeVisible();
	expect(await image.evaluate((el) => el.getBoundingClientRect().right <= window.innerWidth)).toBe(true);
	expect(await page.evaluate(() => document.body.scrollWidth <= window.innerWidth)).toBe(true);
	await expect(page.getByText('End of message', { exact: true }).filter({ visible: true })).toBeVisible();
	expect(await image.evaluate((el) => el.getRootNode() instanceof ShadowRoot)).toBe(true);
});
