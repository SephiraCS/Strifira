import { expect, test, type APIRequestContext, type Page } from '@playwright/test';

/**
 * Production wall verification against the deployed strifira.mariogk.com.
 *
 * The synthetic capture replaces getDisplayMedia with an animated canvas
 * stream, so every engine exercises the real peer-to-peer WebRTC media and
 * decode path without a native picker. The Chrome-native-stop simulation
 * dispatches the 'ended' event on the captured track — exactly what the
 * browser fires when the user hits the native stop bar (a page-side
 * track.stop() deliberately does NOT fire it).
 */

const BASE = 'https://strifira.mariogk.com';
const RUN = Date.now().toString(36);
let userSeq = 0;

async function newUser(request: APIRequestContext, role: string) {
	const email = `e2e-${RUN}-${role}-${++userSeq}@mariogk.com`;
	const password = `KuE2E-${RUN}-${userSeq}-aA9!xZ`;
	// Register already issues the session cookies; logging in again
	// within the same second used to 500 on the refresh-token hash
	// collision (fixed with a jti claim, but the rig keeps it simple).
	const reg = await request.post(`${BASE}/api/auth/register`, {
		data: { email, password, name: `E2E ${role} ${RUN}` }
	});
	expect(reg.status(), `register ${email}`).toBe(201);
	// set-cookie arrives as one newline-joined header; parse it into a
	// cookie string (APIResponse.cookies() is not in every version).
	const setCookie = reg.headers()['set-cookie'] ?? '';
	const cookies = setCookie
		.split('\n')
		.map((line) => line.split(';')[0].trim())
		.filter(Boolean)
		.join('; ');
	return { email, password, cookie: cookies };
}

async function newWorkspace(cookie: string, name: string) {
	const res = await fetch(`${BASE}/api/workspaces`, {
		method: 'POST',
		headers: { 'Content-Type': 'application/json', cookie },
		body: JSON.stringify({ name, slug: `e2e-${RUN}-${name.toLowerCase()}` })
	});
	expect(res.status).toBe(201);
	return (await res.json()) as { slug: string };
}

async function invite(cookie: string, slug: string, email: string) {
	const res = await fetch(`${BASE}/api/workspaces/${slug}/invite`, {
		method: 'POST',
		headers: { 'Content-Type': 'application/json', cookie },
		body: JSON.stringify({ email, role: 'member' })
	});
	expect(res.status).toBe(200);
}

async function activeShareCount(cookie: string, slug: string) {
	const res = await fetch(`${BASE}/api/workspaces/${slug}/shares`, { headers: { cookie } });
	expect(res.status).toBe(200);
	const list = (await res.json()) as { active: unknown[] };
	return list.active.length;
}

/** The animated-canvas getDisplayMedia replacement. */
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
		if (!v) return -1;
		// Firefox's frame counters stay at 0 for MediaStream video even
		// while decoding (currentTime advances, readyState 4); fall back
		// to playback progress expressed as ~30fps-equivalent frames.
		const q = v.getVideoPlaybackQuality?.();
		if (q && q.totalVideoFrames > 0) return q.totalVideoFrames;
		return Math.floor(v.currentTime * 30);
	});
}

async function tileText(page: Page) {
	return page.evaluate(() => {
		const tile = document.querySelector('[data-share-id]');
		return tile ? tile.textContent!.replace(/\s+/g, ' ').slice(0, 160) : '';
	});
}

test.describe('wall solo lifecycle', () => {
	test('share → decode → native stop auto-removes', async ({ page, request, browserName }) => {
		test.setTimeout(150_000);
		const user = await newUser(request, browserName);
		const ws = await newWorkspace(user.cookie, browserName);

		await page.context().addCookies([
			{ name: 'access_token', value: user.cookie.split('access_token=')[1]?.split(';')[0]!, url: BASE }
		]);
		// Full session (refresh token) keeps the page logged in.
		for (const part of user.cookie.split('; ')) {
			const [name, value] = part.split('=');
			await page.context().addCookies([{ name, value, url: BASE }]);
		}

		await page.goto(`${BASE}/${ws.slug}/shares`);
		await expect(page.getByRole('heading', { name: 'Screen sharing' })).toBeVisible();
		await expect(page.getByText('No one is sharing right now')).toBeVisible();

		await installSyntheticCapture(page, browserName);
		await page.getByRole('button', { name: 'Share my screen' }).click();

		// Tile mounts and the local preview decodes the captured stream.
		await expect(page.locator('[data-share-id]')).toHaveCount(1, { timeout: 30_000 });
		await expect
			.poll(async () => frameCount(page), { timeout: 30_000 })
			.toBeGreaterThan(20);
		expect(await tileText(page)).toContain('Sharing');

		// The row is active server-side.
		expect(await activeShareCount(user.cookie, ws.slug)).toBe(1);

		// Native stop bar equivalent: an EXTERNAL 'ended' on the capture.
		await page.evaluate(() => {
			const v = document.querySelector('[data-share-id] video') as HTMLVideoElement | null;
			const track = v && v.srcObject ? (v.srcObject as MediaStream).getVideoTracks()[0] : null;
			if (!track) throw new Error('no captured track');
			track.dispatchEvent(new Event('ended'));
		});

		// Wall fully clears — no leftover viewer tile for the dead share.
		await expect(page.locator('[data-share-id]')).toHaveCount(0, { timeout: 20_000 });
		await expect(page.getByRole('button', { name: 'Share my screen' })).toBeVisible();
		await expect(page.getByText('No one is sharing right now')).toBeVisible();
		expect(await activeShareCount(user.cookie, ws.slug)).toBe(0);
	});
});

test.describe('cross-engine wall', () => {
	test('one chromium presenter, firefox + webkit + chrome viewers', async ({ playwright, request }) => {
		test.setTimeout(300_000);

		const presenter = await newUser(request, 'xp');
		const ws = await newWorkspace(presenter.cookie, 'xwall');
		const viewerCookies: string[] = [];
		for (const role of ['xv-ff', 'xv-wk', 'xv-ch']) {
			const u = await newUser(request, role);
			await invite(presenter.cookie, ws.slug, u.email);
			viewerCookies.push(u.cookie);
		}

		const presenterBrowser = await playwright.chromium.launch();
		const presenterPage = await presenterBrowser.newPage();
		for (const part of presenter.cookie.split('; ')) {
			const [name, value] = part.split('=');
			await presenterPage.context().addCookies([{ name, value, url: BASE }]);
		}
		presenterPage.on('console', (m) => {
			if (m.type() === 'error') console.log('[P-console] ' + m.text().slice(0, 300));
		});
		presenterPage.on('pageerror', (e) => console.log('[P-PAGEERROR] ' + String(e).slice(0, 500)));
		await presenterPage.goto(`${BASE}/${ws.slug}/shares`);
		await installSyntheticCapture(presenterPage, 'xp');
		await presenterPage.getByRole('button', { name: 'Share my screen' }).click();
		await expect(presenterPage.locator('[data-share-id]')).toHaveCount(1, { timeout: 30_000 });
		await expect
			.poll(async () => frameCount(presenterPage), { timeout: 30_000 })
			.toBeGreaterThan(20);

		const viewers: { page: Page; close: () => Promise<void> }[] = [];
		type Launchable = { newPage: () => Promise<Page>; close: () => Promise<void> };
		// WALL_ENGINES overrides the viewer engine set (comma-separated:
		// firefox, webkit, chrome) for hosts that cannot run all of them.
		const wanted = (process.env.WALL_ENGINES ?? 'firefox,webkit,chrome').split(',');
		const all: Array<[string, () => Promise<Launchable>]> = [
			['firefox', () => playwright.firefox.launch() as unknown as Promise<Launchable>],
			['webkit', () => playwright.webkit.launch() as unknown as Promise<Launchable>],
			['chrome', () => playwright.chromium.launch({ channel: 'chrome' }) as unknown as Promise<Launchable>]
		];
		const engines = all.filter(([label]) => wanted.includes(label));
		if (engines.length === 0) throw new Error('WALL_ENGINES selected no viewer engines');
		for (let i = 0; i < engines.length; i++) {
			const [label, launch] = engines[i];
			const browser = await launch();
			const page = await browser.newPage();
			for (const part of viewerCookies[i].split('; ')) {
				const [name, value] = part.split('=');
				await page.context().addCookies([{ name, value, url: BASE }]);
			}
			await page.goto(`${BASE}/${ws.slug}/shares`);
			viewers.push({ page, close: () => browser.close() });
			test.info().annotations.push({ type: 'viewer', description: label });
		}

		// Every engine decodes the remote stream.
		for (const v of viewers) {
			await expect(v.page.locator('[data-share-id]')).toHaveCount(1, { timeout: 30_000 });
			await expect
				.poll(async () => frameCount(v.page), { timeout: 45_000, intervals: [2000] })
				.toBeGreaterThan(10);
		}

		// Presenter sees every viewer (count comes from hub presence).
		await expect
			.poll(async () => (await tileText(presenterPage)).includes(`${engines.length} watching`), { timeout: 20_000 })
			.toBe(true);

		// Switch surface: re-capture under the button's own gesture.
		await installSyntheticCapture(presenterPage, 'xp2');
		await presenterPage.getByRole('button', { name: 'Change what you share' }).click();
		await expect
			.poll(async () => frameCount(presenterPage), { timeout: 30_000 })
			.toBeGreaterThan(0);
		for (const v of viewers) {
			await expect
				.poll(async () => frameCount(v.page), { timeout: 45_000, intervals: [2000] })
				.toBeGreaterThan(10);
		}

		// Native stop clears every wall.
		await presenterPage.evaluate(() => {
			const v = document.querySelector('[data-share-id] video') as HTMLVideoElement | null;
			const track = v && v.srcObject ? (v.srcObject as MediaStream).getVideoTracks()[0] : null;
			track?.dispatchEvent(new Event('ended'));
		});
		await expect(presenterPage.locator('[data-share-id]')).toHaveCount(0, { timeout: 20_000 });
		for (const v of viewers) {
			await expect(v.page.locator('[data-share-id]')).toHaveCount(0, { timeout: 20_000 });
		}
		expect(await activeShareCount(presenter.cookie, ws.slug)).toBe(0);

		for (const v of viewers) await v.close();
		await presenterBrowser.close();
	});
});
