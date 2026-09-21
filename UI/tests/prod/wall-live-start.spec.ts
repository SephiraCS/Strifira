import { expect, test, type APIRequestContext, type Page } from '@playwright/test';

/**
 * [DEBUG-kudyn] Repro: viewer is already parked on the wall when the
 * presenter starts a NEW share — reports say the tile stays
 * "Connecting…" forever and only a manual reload connects.
 *
 * Control at the end: reloading the same viewer page must connect,
 * proving the ICE/network path is fine and only the dynamic mount is
 * broken. WS frames from both pages are logged for evidence.
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

async function frameCount(page: Page) {
	return page.evaluate(() => {
		const v = document.querySelector('[data-share-id] video') as HTMLVideoElement | null;
		if (!v || !v.srcObject) return -1;
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

test('viewer parked on wall before presenter starts must go live without reload', async ({
	playwright,
	request
}) => {
	test.setTimeout(240_000);

	const presenter = await newUser(request, 'pres');
	const viewer = await newUser(request, 'view');
	const ws = await newWorkspace(presenter.cookie, 'livestart');
	await invite(presenter.cookie, ws.slug, viewer.email);

	const presenterBrowser = await playwright.chromium.launch();
	const viewerBrowser = await playwright.chromium.launch();
	const presenterPage = await presenterBrowser.newPage();
	const viewerPage = await viewerBrowser.newPage();
	tapWS(presenterPage, 'P');
	tapWS(viewerPage, 'V');
	for (const p of [presenterPage, viewerPage]) p.on('pageerror', (e) => console.log(`[pageerror] ${String(e).slice(0, 300)}`));

	// 1. Viewer parks on an EMPTY wall first — this is the reported setup.
	await loginCookies(viewerPage, viewer.cookie);
	await viewerPage.goto(`${BASE}/${ws.slug}/shares`);
	await expect(viewerPage.getByText('No one is sharing right now')).toBeVisible();

	// 2. Presenter starts a new share only now.
	await loginCookies(presenterPage, presenter.cookie);
	await presenterPage.goto(`${BASE}/${ws.slug}/shares`);
	await installSyntheticCapture(presenterPage, 'pres');
	await presenterPage.getByRole('button', { name: 'Share my screen' }).click();
	await expect(presenterPage.locator('[data-share-id]')).toHaveCount(1, { timeout: 30_000 });
	await expect
		.poll(async () => frameCount(presenterPage), { timeout: 30_000 })
		.toBeGreaterThan(20);

	// 3. THE BUG: the parked viewer's tile must mount AND decode, with no
	// reload. Stuck "Connecting…" here is the reported failure.
	await expect(viewerPage.locator('[data-share-id]')).toHaveCount(1, { timeout: 15_000 });
	let connected = true;
	try {
		await expect
			.poll(async () => frameCount(viewerPage), { timeout: 40_000, intervals: [2_000] })
			.toBeGreaterThan(10);
	} catch {
		connected = false;
	}
	console.log(`[verdict] dynamic-mount #1 connected=${connected} tile="${await tileText(viewerPage)}"`);

	// 3b. The reported shape needs a PREVIOUS share: presenter stops, then
	// starts a NEW share while the viewer stays parked. The new tile must
	// also go live with no reload.
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
	await expect
		.poll(async () => frameCount(presenterPage), { timeout: 30_000 })
		.toBeGreaterThan(20);

	await expect(viewerPage.locator('[data-share-id]')).toHaveCount(1, { timeout: 15_000 });
	let connected2 = true;
	try {
		await expect
			.poll(async () => frameCount(viewerPage), { timeout: 40_000, intervals: [2_000] })
			.toBeGreaterThan(10);
	} catch {
		connected2 = false;
	}
	console.log(`[verdict] dynamic-mount #2 connected=${connected2} tile="${await tileText(viewerPage)}"`);
	connected = connected && connected2;

	// 4. Control: the user's workaround — reload the SAME page — must
	// connect quickly, proving ICE/network are fine.
	if (!connected) {
		await viewerPage.reload();
		await expect(viewerPage.locator('[data-share-id]')).toHaveCount(1, { timeout: 15_000 });
		await expect
			.poll(async () => frameCount(viewerPage), { timeout: 40_000, intervals: [2_000] })
			.toBeGreaterThan(10);
		console.log('[verdict] control reload connected=true');
	}

	await presenterBrowser.close();
	await viewerBrowser.close();
});
