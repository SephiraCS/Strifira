import { expect, test, type APIRequestContext, type Page } from '@playwright/test';

/**
 * [DEBUG] Instrumented variant of wall-live-start: wraps RTCPeerConnection and
 * WebSocket in the viewer/presenter pages via addInitScript to log every
 * createOffer / setLocalDescription settlement and socket state change. Goal:
 * find why the dynamically mounted viewer tile of a second share never puts an
 * offer on the wire.
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

const INSTRUMENT = `
(() => {
	const log = (...a) => console.log('[inst]', ...a);
	const CO = RTCPeerConnection.prototype.createOffer;
	RTCPeerConnection.prototype.createOffer = function (...a) {
		const id = Math.random().toString(36).slice(2, 7);
		log('createOffer:start', id, 'proto=', Object.prototype.toString.call(this));
		const t0 = performance.now();
		let native;
		try {
			native = CO.apply(this, a);
		} catch (e) {
			log('createOffer:SYNC-THROW', id, String(e), (e && e.stack ? String(e.stack).slice(0, 400) : ''));
			throw e;
		}
		return native.then(
			(r) => { log('createOffer:ok', id, Math.round(performance.now() - t0) + 'ms'); return r; },
			(e) => { log('createOffer:err', id, String(e)); throw e; }
		);
	};
	for (const m of ['setLocalDescription', 'setRemoteDescription', 'createAnswer']) {
		const orig = RTCPeerConnection.prototype[m];
		RTCPeerConnection.prototype[m] = function (...a) {
			const id = Math.random().toString(36).slice(2, 7);
			log(m + ':start', id);
			const t0 = performance.now();
			return orig.apply(this, a).then(
				(r) => { log(m + ':ok', id, Math.round(performance.now() - t0) + 'ms'); return r; },
				(e) => { log(m + ':err', id, String(e)); throw e; }
			);
		};
	}
	const OrigWS = WebSocket;
	window.WebSocket = function (url, proto) {
		const ws = proto === undefined ? new OrigWS(url) : new OrigWS(url, proto);
		log('ws:open-init', url.slice(-14), 'prevCount=', window.__wsCount = (window.__wsCount || 0) + 1);
		ws.addEventListener('open', () => log('ws:OPEN', url.slice(-14)));
		ws.addEventListener('close', (e) => log('ws:CLOSE', url.slice(-14), 'code=', e.code, 'wasClean=', e.wasClean));
		ws.addEventListener('error', () => log('ws:ERROR', url.slice(-14)));
		return ws;
	};
	window.WebSocket.prototype = OrigWS.prototype;
	window.WebSocket.OPEN = OrigWS.OPEN;
	window.addEventListener('unhandledrejection', (e) => log('unhandledrejection', String(e.reason).slice(0, 200)));
})();
`;

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
		if (p.data?.type) bits.push(`sdp=${p.data.type}`);
		if (p.role) bits.push(`${p.role}:${p.action ?? ''}`);
		if (p.presenter_online !== undefined) bits.push(`online=${p.presenter_online},v=${p.viewers}`);
		if (p.id) bits.push(`id=${String(p.id).slice(0, 8)}`);
		console.log(`[ws ${tag}] ${dir} ${msg.type} ${bits.join(' ')}`);
	} catch {
		// non-JSON frame
	}
}

test('instrumented: why does the second dynamic tile never offer', async ({ playwright, request }) => {
	test.setTimeout(300_000);

	const presenter = await newUser(request, 'pres');
	const viewer = await newUser(request, 'view');
	const ws = await newWorkspace(presenter.cookie, 'inst2');
	await invite(presenter.cookie, ws.slug, viewer.email);

	const presenterBrowser = await playwright.chromium.launch();
	const viewerBrowser = await playwright.chromium.launch();
	const presenterPage = await presenterBrowser.newPage();
	const viewerPage = await viewerBrowser.newPage();
	for (const [tag, p] of [['P', presenterPage], ['V', viewerPage]] as const) {
		tapWS(p, tag);
		p.on('console', (m) => {
			const t = m.text();
			if (t.includes('[inst]') || t.includes('[ws ') || m.type() === 'error') console.log(`[${tag}:console] ${t.slice(0, 220)}`);
		});
		p.on('pageerror', (e) => console.log(`[${tag}:pageerror] ${String(e).slice(0, 400)}`));
		await p.addInitScript(INSTRUMENT);
	}

	await loginCookies(viewerPage, viewer.cookie);
	await viewerPage.goto(`${BASE}/${ws.slug}/shares`);
	await expect(viewerPage.getByText('No one is sharing right now')).toBeVisible();

	await loginCookies(presenterPage, presenter.cookie);
	await presenterPage.goto(`${BASE}/${ws.slug}/shares`);
	await installSyntheticCapture(presenterPage, 'pres');
	await presenterPage.getByRole('button', { name: 'Share my screen' }).click();
	await expect(presenterPage.locator('[data-share-id]')).toHaveCount(1, { timeout: 30_000 });
	await expect.poll(async () => frameCount(presenterPage), { timeout: 30_000 }).toBeGreaterThan(20);

	await expect(viewerPage.locator('[data-share-id]')).toHaveCount(1, { timeout: 15_000 });
	const c1 = await viewerPage
		.evaluate(() => (document.querySelector('[data-share-id]')?.textContent ?? '').replace(/\s+/g, ' ').slice(0, 80))
		.catch(() => 'eval-failed');
	console.log(`[verdict] #1 tile="${c1}"`);

	console.log('[phase] presenter stops share #1');
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

	await expect(viewerPage.locator('[data-share-id]')).toHaveCount(1, { timeout: 15_000 });
	// Give the tile 25s (past the 20s watchdog) and sample everything.
	await viewerPage.waitForTimeout(25_000);
	const dump = await viewerPage.evaluate(() => {
		const tile = document.querySelector('[data-share-id]');
		return {
			tileText: tile ? tile.textContent!.replace(/\s+/g, ' ').slice(0, 120) : null,
			wsState: (() => {
				try {
					return 'probe';
				} catch {
					return 'n/a';
				}
			})()
		};
	});
	console.log(`[verdict] #2 after 25s tile="${dump.tileText}"`);
	const fc = await frameCount(viewerPage).catch(() => -99);
	console.log(`[verdict] #2 frameCount=${fc}`);
	const wsProbe = await viewerPage.evaluate(() => {
		// Any live websocket objects are not directly enumerable; report online
		// state via navigator.onLine and let the instrumented console speak.
		return { online: navigator.onLine, hidden: document.hidden };
	});
	console.log(`[probe] ${JSON.stringify(wsProbe)}`);

	// Recovery experiment: inside the hung page, close a brand-new pc quickly,
	// then attempt a full fresh offer race. If a fresh pc can still produce an
	// offer, a client-side retry loop is a viable fix.
	const recovery = await viewerPage.evaluate(async () => {
		const ICE = [{ urls: ['stun:stun.l.google.com:19302'] }];
		const race = (p: Promise<unknown>, ms: number) =>
			Promise.race([p.then(() => 'ok', (e) => 'err:' + String(e).slice(0, 80)), new Promise((r) => setTimeout(() => r('TIMEOUT'), ms))]);
		const out: string[] = [];
		// 1. fresh pc offered with a deadline
		const a = new RTCPeerConnection({ iceServers: ICE });
		a.addTransceiver('video', { direction: 'recvonly' });
		out.push('freshA createOffer: ' + (await race(a.createOffer(), 5000)));
		a.close();
		// 2. even after closing a mid-offer hung pc pattern: create, close immediately, create again
		const b = new RTCPeerConnection({ iceServers: ICE });
		b.close();
		const c = new RTCPeerConnection({ iceServers: ICE });
		c.addTransceiver('video', { direction: 'recvonly' });
		out.push('freshC createOffer: ' + (await race(c.createOffer(), 5000)));
		c.close();
		return out;
	});
	console.log(`[recovery] ${recovery.join(' | ')}`);

	await presenterBrowser.close();
	await viewerBrowser.close();
});
