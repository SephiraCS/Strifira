import { expect, test, type APIRequestContext, type Page } from '@playwright/test';

/**
 * Presenter session persistence: the share's capture and peer connections
 * live in a module-level session store, so navigating the app mid-share
 * (wall → inbox → issue detail → wall) must NOT end the share, prompt, or
 * reconnect the media — and a Stop control stays reachable from every
 * page via the sidebar. A document unload (reload/close) still raises the
 * native beforeunload prompt, and the source-less share is GC'd after the
 * presenter-contact grace period. Run via playwright.prod.config.ts
 * against the deployed build.
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

async function createIssueForNavigation(cookie: string, slug: string) {
	const meRes = await fetch(`${BASE}/api/auth/me`, { headers: { cookie } });
	if (meRes.status !== 200) return null;
	const me = (await meRes.json()) as { id: string };
	const teamRes = await fetch(`${BASE}/api/workspaces/${slug}/teams`, {
		method: 'POST',
		headers: { 'Content-Type': 'application/json', cookie },
		body: JSON.stringify({ name: 'Persist', key: 'PST' })
	});
	if (teamRes.status !== 201) return null;
	const team = (await teamRes.json()) as { id: string };
	// Assigned to the creator so it shows on My Issues' default
	// (assigned) tab.
	const issueRes = await fetch(`${BASE}/api/workspaces/${slug}/issues`, {
		method: 'POST',
		headers: { 'Content-Type': 'application/json', cookie },
		body: JSON.stringify({ team_id: team.id, title: 'Navigation persistence probe', assignee_ids: [me.id] })
	});
	if (issueRes.status !== 201) return null;
	const issue = (await issueRes.json()) as { identifier: string };
	return `/${slug}/issue/${issue.identifier}`;
}

test('share survives navigation to inbox and issues; reload prompts and GCs', async ({ playwright, request }) => {
	test.setTimeout(300_000);

	const user = await newUser(request, 'persist');
	const wsRes = await fetch(`${BASE}/api/workspaces`, {
		method: 'POST',
		headers: { 'Content-Type': 'application/json', cookie: user.cookie },
		body: JSON.stringify({ name: 'persist', slug: `e2e-${RUN}-persist` })
	});
	expect(wsRes.status).toBe(201);
	const { slug } = (await wsRes.json()) as { slug: string };
	const issuePath = await createIssueForNavigation(user.cookie, slug);

	const browser = await playwright.chromium.launch();
	const page = await browser.newPage();
	await loginCookies(page, user.cookie);
	let dialogs = 0;
	page.on('dialog', (d) => {
		dialogs++;
		void d.dismiss();
	});

	await page.goto(`${BASE}/${slug}/shares`);
	await expect(page.getByText('No one is sharing right now')).toBeVisible();

	await installSyntheticCapture(page, 'persist');
	await page.getByRole('button', { name: 'Share my screen' }).click();
	await expect(page.locator('[data-share-id]')).toHaveCount(1, { timeout: 30_000 });
	await expect
		.poll(async () => frameCount(page), { timeout: 30_000 })
		.toBeGreaterThan(20);

	// 1. In-app navigation while sharing must be silent and non-destructive:
	// the session store outlives the route component.
	await page.locator(`a[href="/${slug}/inbox"]`).first().click();
	await expect(page).toHaveURL(new RegExp(`/${slug}/inbox`));
	expect(dialogs, 'navigation must not prompt while sharing').toBe(0);
	await expect(page.locator('button[title="Stop sharing"]')).toBeVisible();
	expect(await activeShareCount(user.cookie, slug)).toBe(1);

	// 2. The literal report: the presenter goes to an issue mid-share.
	// (In-app navigation only — a full page.goto here would hit the native
	// beforeunload prompt, which is exactly the reload/close case below.
	// Issue rows navigate via click handlers, not anchors.)
	if (issuePath) {
		await page.locator(`a[href="/${slug}/my-issues"]`).first().click();
		await expect(page).toHaveURL(new RegExp(`/${slug}/my-issues`));
		const issueRow = page.getByText('Navigation persistence probe').first();
		await expect(issueRow).toBeVisible({ timeout: 15_000 });
		await issueRow.click();
		await expect(page).toHaveURL(new RegExp(`/${slug}/issue/`));
		expect(dialogs).toBe(0);
		await expect(page.locator('button[title="Stop sharing"]')).toBeVisible();
		expect(await activeShareCount(user.cookie, slug)).toBe(1);
	}

	// 3. Back on the wall: the same share is still live — media kept
	// flowing throughout, no second share was created.
	await page.locator(`a[href="/${slug}/shares"]`).first().click();
	await expect(page.locator('[data-share-id]')).toHaveCount(1, { timeout: 15_000 });
	await expect
		.poll(async () => frameCount(page), { timeout: 30_000 })
		.toBeGreaterThan(20);
	expect(await activeShareCount(user.cookie, slug)).toBe(1);

	// 4. Document unload while sharing: the native beforeunload prompt
	// fires; accept it, reload, and the source-less share is GC'd after the
	// presenter-contact grace (45s) on the next list call.
	page.removeAllListeners('dialog');
	let sawBeforeUnload = false;
	page.on('dialog', (d) => {
		if (d.type() === 'beforeunload') sawBeforeUnload = true;
		void d.accept();
	});
	await page.reload();
	await expect(page.locator('[data-share-id]')).toHaveCount(0, { timeout: 15_000 });
	expect(sawBeforeUnload, 'beforeunload prompt must fire while sharing').toBe(true);

	await expect
		.poll(async () => activeShareCount(user.cookie, slug), { timeout: 70_000, intervals: [5_000] })
		.toBe(0);

	// 5. With the share gone, nothing prompts and the sidebar Stop control
	// is gone.
	let prompts = 0;
	page.removeAllListeners('dialog');
	page.on('dialog', (d) => {
		prompts++;
		void d.accept();
	});
	await page.goto(`${BASE}/${slug}/inbox`);
	await expect(page).toHaveURL(new RegExp(`/${slug}/inbox`));
	expect(prompts, 'no dialog once nothing is shared').toBe(0);
	await expect(page.locator('button[title="Stop sharing"]')).toHaveCount(0);

	await browser.close();
});
