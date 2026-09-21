import { expect, test, type APIRequestContext, type Page } from '@playwright/test';

/**
 * Presenter leave guard: while a tab is the live source of a share,
 * in-app route changes ask for confirmation (dismiss keeps the share),
 * and document unload (reload/close) raises the native beforeunload
 * prompt. Run via playwright.prod.config.ts against the deployed build.
 */

const BASE = 'https://ku.mariogk.com';
const RUN = Date.now().toString(36);

async function newUser(request: APIRequestContext, role: string) {
	const email = `e2e-${RUN}-${role}@mariogk.com`;
	const password = `KuE2E-${RUN}-aA9!xZ`;
	const reg = await request.post(`${BASE}/api/auth/register`, {
		data: { email, password, name: `E2E ${role} ${RUN}` }
	});
	expect(reg.status(), `register ${email}`).toBe(201);
	const setCookie = reg.headers()['set-cookie'] ?? '';
	const cookie = setCookie
		.split('\n')
		.map((line) => line.split(';')[0].trim())
		.filter(Boolean)
		.join('; ');
	return { email, cookie };
}

async function loginCookies(page: Page, cookie: string) {
	for (const part of cookie.split('; ')) {
		const [name, value] = part.split('=');
		await page.context().addCookies([{ name, value, url: BASE }]);
	}
}

async function installSyntheticCapture(page: Page, label: string) {
	await page.evaluate((l) => {
		const canvas = document.createElement('canvas');
		canvas.width = 1280;
		canvas.height = 720;
		const ctx = canvas.getContext('2d')!;
		let n = 0;
		setInterval(() => {
			ctx.fillStyle = `hsl(${n % 360},70%,40%)`;
			ctx.fillRect(0, 0, canvas.width, canvas.height);
			ctx.fillStyle = '#fff';
			ctx.font = 'bold 64px sans-serif';
			ctx.fillText(`${l} ${n++}`, 80, 400);
		}, 33);
		navigator.mediaDevices.getDisplayMedia = async () => canvas.captureStream(30);
	}, label);
}

async function frameCount(page: Page) {
	return page.evaluate(() => {
		const v = document.querySelector('[data-share-id] video') as HTMLVideoElement | null;
		if (!v || !v.srcObject) return -1;
		const q = v.getVideoPlaybackQuality?.();
		if (q && q.totalVideoFrames > 0) return q.totalVideoFrames;
		return Math.floor(v.currentTime * 30);
	});
}

async function activeShareCount(cookie: string, slug: string) {
	const res = await fetch(`${BASE}/api/workspaces/${slug}/shares`, { headers: { cookie } });
	expect(res.status).toBe(200);
	const list = (await res.json()) as { active: unknown[] };
	return list.active.length;
}

test('presenter is warned before navigating away or unloading mid-share', async ({ playwright, request }) => {
	test.setTimeout(240_000);

	const user = await newUser(request, 'guard');
	const wsRes = await fetch(`${BASE}/api/workspaces`, {
		method: 'POST',
		headers: { 'Content-Type': 'application/json', cookie: user.cookie },
		body: JSON.stringify({ name: 'guard', slug: `e2e-${RUN}-guard` })
	});
	expect(wsRes.status).toBe(201);
	const { slug } = (await wsRes.json()) as { slug: string };

	const browser = await playwright.chromium.launch();
	const page = await browser.newPage();
	await loginCookies(page, user.cookie);
	await page.goto(`${BASE}/${slug}/shares`);
	await expect(page.getByText('No one is sharing right now')).toBeVisible();

	await installSyntheticCapture(page, 'guard');
	await page.getByRole('button', { name: 'Share my screen' }).click();
	await expect(page.locator('[data-share-id]')).toHaveCount(1, { timeout: 30_000 });
	await expect
		.poll(async () => frameCount(page), { timeout: 30_000 })
		.toBeGreaterThan(20);

	// 1. In-app navigation while sharing: dismiss the confirm → stay.
	let dialogMessage = '';
	page.on('dialog', (d) => {
		dialogMessage = d.message();
		void d.dismiss();
	});
	await page.locator(`a[href="/${slug}/inbox"]`).first().click();
	await page.waitForTimeout(1_000);
	expect(page.url(), 'dismissed dialog must keep the route').toContain(`/${slug}/shares`);
	expect(dialogMessage, 'confirm dialog must appear').toContain('sharing');
	await expect
		.poll(async () => frameCount(page), { timeout: 20_000 })
		.toBeGreaterThan(20);
	expect(await activeShareCount(user.cookie, slug)).toBe(1);

	// 2. Document unload while sharing: the native beforeunload prompt
	// fires; accept it, reload, and the source-less share is GC'd.
	let sawBeforeUnload = false;
	page.removeAllListeners('dialog');
	page.on('dialog', (d) => {
		if (d.type() === 'beforeunload') sawBeforeUnload = true;
		void d.accept();
	});
	await page.reload();
	await expect(page.locator('[data-share-id]')).toHaveCount(0, { timeout: 15_000 });
	expect(sawBeforeUnload, 'beforeunload prompt must fire while sharing').toBe(true);

	// No presenter connection exists after the unload; the server's
	// no-source grace (45s) ends the share on the next list call.
	await expect
		.poll(async () => activeShareCount(user.cookie, slug), { timeout: 70_000, intervals: [5_000] })
		.toBe(0);

	// 3. After the share is gone, navigation must NOT prompt anymore.
	let prompts = 0;
	page.removeAllListeners('dialog');
	page.on('dialog', (d) => {
		prompts++;
		void d.accept();
	});
	await page.goto(`${BASE}/${slug}/inbox`);
	await expect(page).toHaveURL(new RegExp(`/${slug}/inbox`));
	expect(prompts, 'no dialog once nothing is shared').toBe(0);

	await browser.close();
});
