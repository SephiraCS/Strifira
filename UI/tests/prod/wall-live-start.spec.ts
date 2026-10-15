import { expect, test, type APIRequestContext, type Page } from '@playwright/test';

/**
 * Regression: viewers parked on the wall must go live whenever a share
 * starts — dynamically mounted tiles, second shares after a stop, and
 * simultaneous presenters. The 2026-09-21 bug: the viewer tile of a newly
 * started share stayed "Connecting…" forever (browser createOffer that
 * never settles + one-shot negotiation + a lost offer in the presenter
 * mount race), and only a manual reload connected.
 */

const BASE = 'https://strifira.mariogk.com';
const RUN = Date.now().toString(36);

async function newUser(request: APIRequestContext, role: string) {
	const email = `e2e-${RUN}-${role}@mariogk.com`;
	const password = `KuE2E-${RUN}-aA9!xZ`;
	const reg = await request.post(`${BASE}/api/auth/register`, {
		data: { email, password, name: `E2E ${role} ${RUN}` }
	});
	expect(reg.status(), `register ${email}`).toBe(201);
	const setCookie = reg.headers()['set-cookie'] ?? '';
	const cookies = setCookie
		.split('\n')
		.map((line) => line.split(';')[0].trim())
		.filter(Boolean)
		.join('; ');
	return { email, cookie: cookies };
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

async function loginCookies(page: Page, cookie: string) {
	for (const part of cookie.split('; ')) {
		const [name, value] = part.split('=');
		await page.context().addCookies([{ name, value, url: BASE }]);
	}
}

/** Concise share.* WS frame log from one page. */
function tapWS(page: Page, tag: string) {
	page.on('websocket', (ws) => {
		ws.on('framesent', (d) => logFrame(tag, '→', d.payload));
		ws.on('framereceived', (d) => logFrame(tag, '←', d.payload));
	});
}

function logFrame(tag: string, dir: string, data: string | Buffer) {
	try {
		const msg = JSON.parse(String(data)) as { type: string; payload: any };
		if (!msg.type.startsWith('share.')) return;
		const p = msg.payload ?? {};
		const bits: string[] = [];
		if (p.share_id) bits.push(String(p.share_id).slice(0, 8));
		if (p.from) bits.push(`from=${String(p.from).slice(0, 8)}`);
		if (p.to) bits.push(`to=${String(p.to).slice(0, 8)}`);
		if (p.role) bits.push(`${p.role}:${p.action ?? ''}`);
		if (p.data?.type) bits.push(`sdp=${p.data.type}`);
		if (p.presenter_online !== undefined) bits.push(`online=${p.presenter_online},v=${p.viewers}`);
		if (p.id) bits.push(`id=${String(p.id).slice(0, 8)}`);
		console.log(`[ws ${tag}] ${dir} ${msg.type} ${bits.join(' ')}`);
	} catch {
		// non-JSON or non-share frame
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

async function frameCount(page: Page, tileIndex = 0) {
	return page.evaluate((i) => {
		const v = document.querySelectorAll('[data-share-id] video')[i] as HTMLVideoElement | undefined;
		if (!v || !v.srcObject) return -1;
		const q = v.getVideoPlaybackQuality?.();
		if (q && q.totalVideoFrames > 0) return q.totalVideoFrames;
		return Math.floor(v.currentTime * 30);
	}, tileIndex);
}

async function expectTileLive(page: Page, tag: string, tileIndex = 0, frames = 10) {
	await expect
		.poll(() => frameCount(page, tileIndex), { timeout: 45_000, intervals: [2_000] })
		.toBeGreaterThan(frames);
	console.log(`[live] ${tag} tile#${tileIndex} decoding`);
}

test('viewer parked on the wall goes live for new, repeated, and simultaneous shares', async ({
	playwright,
	request
}) => {
	test.setTimeout(300_000);

	const presenter = await newUser(request, 'pres');
	const second = await newUser(request, 'pres2');
	const viewer = await newUser(request, 'view');
	const ws = await newWorkspace(presenter.cookie, 'livestart');
	await invite(presenter.cookie, ws.slug, second.email);
	await invite(presenter.cookie, ws.slug, viewer.email);

	const presenterBrowser = await playwright.chromium.launch();
	const secondBrowser = await playwright.chromium.launch();
	const viewerBrowser = await playwright.chromium.launch();
	const presenterPage = await presenterBrowser.newPage();
	const secondPage = await secondBrowser.newPage();
	const viewerPage = await viewerBrowser.newPage();
	for (const [tag, p] of [
		['P', presenterPage],
		['S', secondPage],
		['V', viewerPage]
	] as const) {
		tapWS(p, tag);
		p.on('pageerror', (e) => console.log(`[${tag}:pageerror] ${String(e).slice(0, 300)}`));
	}

	// 1. Viewer parks on an EMPTY wall first — this is the reported setup.
	await loginCookies(viewerPage, viewer.cookie);
	await viewerPage.goto(`${BASE}/${ws.slug}/shares`);
	await expect(viewerPage.getByText('No one is sharing right now')).toBeVisible();

	// 2. Presenter starts a share only now: the dynamically mounted viewer
	// tile must connect with no reload. Stuck "Connecting…" here was the
	// reported failure.
	await loginCookies(presenterPage, presenter.cookie);
	await presenterPage.goto(`${BASE}/${ws.slug}/shares`);
	await installSyntheticCapture(presenterPage, 'pres');
	await presenterPage.getByRole('button', { name: 'Share my screen' }).click();
	await expect(presenterPage.locator('[data-share-id]')).toHaveCount(1, { timeout: 30_000 });
	await expectTileLive(presenterPage, 'P', 0, 20);

	await expect(viewerPage.locator('[data-share-id]')).toHaveCount(1, { timeout: 15_000 });
	await expectTileLive(viewerPage, 'V', 0);

	// 3. Presenter stops and immediately starts a NEW share while the viewer
	// stays parked: the replacement tile must also go live. (Second viewer
	// peer connection on the same page — the hung-createOffer shape.)
	console.log('[phase] presenter native-stops share #1');
	await presenterPage.evaluate(() => {
		const v = document.querySelector('[data-share-id] video') as HTMLVideoElement | null;
		const track = v && v.srcObject ? (v.srcObject as MediaStream).getVideoTracks()[0] : null;
		if (!track) throw new Error('no captured track');
		track.dispatchEvent(new Event('ended'));
	});
	await expect(presenterPage.locator('[data-share-id]')).toHaveCount(0, { timeout: 20_000 });
	await expect(viewerPage.locator('[data-share-id]')).toHaveCount(0, { timeout: 20_000 });

	console.log('[phase] presenter starts share #2 (viewer stays parked)');
	await installSyntheticCapture(presenterPage, 'pres2');
	await presenterPage.getByRole('button', { name: 'Share my screen' }).click();
	await expect(presenterPage.locator('[data-share-id]')).toHaveCount(1, { timeout: 30_000 });
	await expectTileLive(presenterPage, 'P2', 0, 20);

	await expect(viewerPage.locator('[data-share-id]')).toHaveCount(1, { timeout: 15_000 });
	await expectTileLive(viewerPage, 'V2', 0);

	// 4. Simultaneous presenters: a second user shares while share #2 runs.
	// The parked viewer's wall must show two tiles, both decoding — the
	// literal multi-screen-share report.
	console.log('[phase] second presenter starts while share #2 runs');
	await loginCookies(secondPage, second.cookie);
	await secondPage.goto(`${BASE}/${ws.slug}/shares`);
	await installSyntheticCapture(secondPage, 'second');
	await secondPage.getByRole('button', { name: 'Share my screen' }).click();
	await expect(secondPage.locator('[data-share-id]')).toHaveCount(1, { timeout: 30_000 });
	await expectTileLive(secondPage, 'S', 0, 20);

	await expect(viewerPage.locator('[data-share-id]')).toHaveCount(2, { timeout: 15_000 });
	// Both tiles decode (order is not guaranteed; poll both, then require
	// each to have frames).
	await expectTileLive(viewerPage, 'V-multi', 0);
	await expectTileLive(viewerPage, 'V-multi', 1);
	// The first presenter's page also sees the second share as a viewer tile.
	await expect(presenterPage.locator('[data-share-id]')).toHaveCount(2, { timeout: 15_000 });
	await expectTileLive(presenterPage, 'P-sees-S', 1);

	await presenterBrowser.close();
	await secondBrowser.close();
	await viewerBrowser.close();
});
