import { expect, test, type APIRequestContext, type Browser, type BrowserContext, type Page } from '@playwright/test';
import { randomUUID } from 'node:crypto';

interface SignalEvent {
	type: string;
	payload?: {
		share_id?: string;
		from_peer?: string;
		negotiation_id?: string;
		data?: { type?: string };
		viewer_peers?: string[];
	};
}

declare global {
	interface Window {
		__kuLive: {
			peers: RTCPeerConnection[];
			sockets: WebSocket[];
			messages: SignalEvent[];
			captures: MediaStream[];
			captureCalls: number;
		};
	}
}

interface Share {
	id: string;
	viewer_count: number;
}

async function checkedJSON<T>(response: Awaited<ReturnType<APIRequestContext['get']>>, status: number): Promise<T> {
	expect(response.status(), await response.text()).toBe(status);
	return response.json() as Promise<T>;
}

async function newUser(browser: Browser, baseURL: string, role: string): Promise<BrowserContext> {
	const context = await browser.newContext({ baseURL, locale: 'en-US' });
	const suffix = randomUUID().slice(0, 8);
	await checkedJSON(
		await context.request.post('/api/auth/register', {
			data: {
				email: `${role}-${suffix}@ku-live.invalid`,
				password: `KuLive-${randomUUID()}-aA9!`,
				name: `${role} ${suffix}`
			}
		}),
		201
	);
	await context.addInitScript(() => {
		localStorage.setItem('PARAGLIDE_LOCALE', 'en');
		window.__kuLive = { peers: [], sockets: [], messages: [], captures: [], captureCalls: 0 };

		// Observe native constructors without replacing any transport behavior.
		// The suite uses real offers/answers, ICE, RTP encoding and decoding.
		const PeerConnection = window.RTCPeerConnection;
		window.RTCPeerConnection = class extends PeerConnection {
			constructor(configuration?: RTCConfiguration) {
				super(configuration);
				window.__kuLive.peers.push(this);
			}
		};
		const Socket = window.WebSocket;
		window.WebSocket = class extends Socket {
			constructor(url: string | URL, protocols?: string | string[]) {
				super(url, protocols);
				window.__kuLive.sockets.push(this);
				this.addEventListener('message', (event) => {
					try {
						window.__kuLive.messages.push(JSON.parse(event.data));
					} catch {
						/* Ignore non-JSON traffic. */
					}
				});
			}
		};
	});
	return context;
}

async function createWorkspace(context: BrowserContext): Promise<string> {
	const slug = `live-${randomUUID().slice(0, 8)}`;
	await checkedJSON(
		await context.request.post('/api/workspaces', {
			data: { name: `Local screen-share test ${slug}`, slug }
		}),
		201
	);
	return slug;
}

async function activeShares(context: BrowserContext, slug: string): Promise<Share[]> {
	const list = await checkedJSON<{ active: Share[] }>(await context.request.get(`/api/workspaces/${slug}/shares`), 200);
	return list.active;
}

async function openWall(context: BrowserContext, slug: string): Promise<Page> {
	const page = await context.newPage();
	await page.goto(`/${slug}/shares`);
	await expect(page.getByRole('button', { name: 'Share my screen', exact: true })).toBeVisible();
	await expect
		.poll(() =>
			page.evaluate(() =>
				window.__kuLive.sockets.some((socket) => socket.url.includes('/ws') && socket.readyState === WebSocket.OPEN)
			)
		)
		.toBe(true);
	return page;
}

async function installCanvasCapture(page: Page, label: string): Promise<void> {
	await page.evaluate((name) => {
		// Headless browsers have no screen picker. Only capture is synthetic;
		// canvas.captureStream produces real video tracks for native WebRTC.
		navigator.mediaDevices.getDisplayMedia = async () => {
			window.__kuLive.captureCalls++;
			const canvas = document.createElement('canvas');
			canvas.width = 640;
			canvas.height = 360;
			const ctx = canvas.getContext('2d')!;
			let frame = 0;
			const capture = window.__kuLive.captureCalls;
			const draw = () => {
				// Stable distinct colors prove replaceTrack changed decoded
				// source, rather than leaving the receiver on old frames.
				ctx.fillStyle = capture % 2 ? '#2255dd' : '#22bb55';
				ctx.fillRect(0, 0, canvas.width, canvas.height);
				ctx.fillStyle = '#fff';
				ctx.font = 'bold 30px sans-serif';
				ctx.fillText(`${name}: capture ${capture}, frame ${frame++}`, 20, 180);
				ctx.fillRect((frame * 8) % canvas.width, 240, 30, 40);
			};
			draw();
			const timer = window.setInterval(draw, 33);
			const stream = canvas.captureStream(30);
			stream.getVideoTracks()[0].addEventListener('ended', () => clearInterval(timer));
			window.__kuLive.captures.push(stream);
			return stream;
		};
	}, label);
}

async function frameCount(page: Page, shareId: string): Promise<number> {
	return page.evaluate((id) => {
		const video = document.querySelector(`[data-share-id="${id}"] video`) as HTMLVideoElement | null;
		if (!video || !video.srcObject || !video.videoWidth || video.readyState < 2) return 0;
		return video.getVideoPlaybackQuality().totalVideoFrames;
	}, shareId);
}

async function receivedVideo(page: Page): Promise<{ frames: number; bytes: number; connected: number }> {
	return page.evaluate(async () => {
		let frames = 0;
		let bytes = 0;
		let connected = 0;
		for (const peer of window.__kuLive.peers) {
			if (peer.connectionState !== 'connected') continue;
			connected++;
			const stats = await peer.getStats();
			for (const report of stats.values()) {
				if (report.type === 'inbound-rtp' && report.kind === 'video') {
					frames += report.framesDecoded ?? 0;
					bytes += report.bytesReceived ?? 0;
				}
			}
		}
		return { frames, bytes, connected };
	});
}

async function expectDecodedVideo(page: Page, shareId: string): Promise<void> {
	await expect(page.locator(`[data-share-id="${shareId}"] video`)).toBeVisible();
	await expect.poll(() => frameCount(page, shareId)).toBeGreaterThan(10);
	await expect.poll(async () => (await receivedVideo(page)).frames).toBeGreaterThan(10);
}

async function expectVideoContinues(page: Page, shareId: string): Promise<void> {
	const before = await frameCount(page, shareId);
	await expect.poll(() => frameCount(page, shareId)).toBeGreaterThan(before + 5);
}

async function closeWorkspaceSocket(page: Page): Promise<void> {
	await page.evaluate(() => {
		const socket = window.__kuLive.sockets.findLast(
			(candidate) => candidate.url.includes('/ws') && candidate.readyState === WebSocket.OPEN
		);
		if (!socket) throw new Error('No real workspace socket to disconnect');
		socket.close(1000, 'Live integration reconnect probe');
	});
}

async function hasGreenSource(page: Page, shareId: string): Promise<boolean> {
	return page.evaluate((id) => {
		const video = document.querySelector(`[data-share-id="${id}"] video`) as HTMLVideoElement | null;
		if (!video || video.readyState < 2) return false;
		const canvas = document.createElement('canvas');
		canvas.width = canvas.height = 1;
		const ctx = canvas.getContext('2d')!;
		ctx.drawImage(video, 10, 10, 1, 1, 0, 0, 1, 1);
		const [red, green, blue] = ctx.getImageData(0, 0, 1, 1).data;
		return green > blue + 50 && green > red + 50;
	}, shareId);
}

test('real media supports same-user tabs, navigation, surface changes, reconnect and restart', async ({
	browser,
	baseURL
}, testInfo) => {
	const presenter = await newUser(browser, baseURL!, 'presenter');
	const viewer = await newUser(browser, baseURL!, 'viewer');
	const contexts = [presenter, viewer];
	const pages: Array<{ role: string; page: Page }> = [];
	try {
		const slug = await createWorkspace(presenter);
		const member = await checkedJSON<{ email: string }>(await viewer.request.get('/api/auth/me'), 200);
		await checkedJSON(
			await presenter.request.post(`/api/workspaces/${slug}/invite`, {
				data: { email: member.email, role: 'member' }
			}),
			200
		);
		const source = await openWall(presenter, slug);
		pages.push({ role: 'presenter', page: source });
		await installCanvasCapture(source, 'primary');
		await source.getByRole('button', { name: 'Share my screen', exact: true }).click();
		await expect.poll(async () => (await activeShares(presenter, slug)).length).toBe(1);
		const shareId = (await activeShares(presenter, slug))[0].id;
		await expect.poll(() => frameCount(source, shareId)).toBeGreaterThan(10);

		// Identical account cookies must produce independent socket/peer IDs.
		const ownViewerA = await openWall(presenter, slug);
		const ownViewerB = await openWall(presenter, slug);
		const otherViewerA = await openWall(viewer, slug);
		const otherViewerB = await openWall(viewer, slug);
		const viewers = [ownViewerA, ownViewerB, otherViewerA, otherViewerB];
		pages.push(...viewers.map((page, index) => ({ role: `viewer-${index}`, page })));
		for (const page of viewers) await expectDecodedVideo(page, shareId);
		// Counts displayed to users deduplicate accounts; membership and
		// transport remain per tab, including viewers signed in as presenter.
		await expect.poll(async () => (await activeShares(presenter, slug))[0]?.viewer_count).toBe(2);
		await expect
			.poll(() =>
				source.evaluate(
					(id) =>
						window.__kuLive.messages.findLast(
							(message) => message.type === 'share.presence' && message.payload?.share_id === id
						)?.payload?.viewer_peers?.length,
					shareId
				)
			)
			.toBe(4);
		await expect
			.poll(() =>
				source.evaluate(
					(id) =>
						new Set(
							window.__kuLive.messages
								.filter(
									(message) =>
										message.type === 'share.signal' &&
										message.payload?.share_id === id &&
										message.payload?.data?.type === 'offer'
								)
								.map((message) => message.payload?.from_peer)
								.filter(Boolean)
						).size,
					shareId
				)
			)
			.toBe(4);
		await testInfo.attach('four-decoded-viewer-tabs', {
			body: JSON.stringify(
				await Promise.all(
					viewers.map(async (page, index) => ({
						viewer: index,
						displayedFrames: await frameCount(page, shareId),
						rtp: await receivedVideo(page)
					}))
				),
				null,
				2
			),
			contentType: 'application/json'
		});

		// A second capturing tab cannot take over or end the first capture.
		await installCanvasCapture(ownViewerA, 'conflicting-tab');
		const conflict = ownViewerA.waitForResponse(
			(response) => response.url().endsWith(`/api/workspaces/${slug}/shares`) && response.request().method() === 'POST'
		);
		await ownViewerA.getByRole('button', { name: 'Share my screen', exact: true }).click();
		expect((await conflict).status()).toBe(409);
		await expect
			.poll(() =>
				ownViewerA.evaluate(() =>
					window.__kuLive.captures.every((capture) =>
						capture.getTracks().every((track) => track.readyState === 'ended')
					)
				)
			)
			.toBe(true);
		expect((await activeShares(presenter, slug)).map((share) => share.id)).toEqual([shareId]);
		await expectVideoContinues(otherViewerA, shareId);

		// Closing one same-user viewer must leave its sibling and source live.
		await ownViewerA.close();
		await expect.poll(async () => (await activeShares(presenter, slug))[0]?.viewer_count).toBe(2);
		await expect
			.poll(() =>
				source.evaluate(
					(id) =>
						window.__kuLive.messages.findLast(
							(message) => message.type === 'share.presence' && message.payload?.share_id === id
						)?.payload?.viewer_peers?.length,
					shareId
				)
			)
			.toBe(3);
		await expectVideoContinues(ownViewerB, shareId);
		await expectVideoContinues(otherViewerB, shareId);

		// In-app navigation never unloads the persistent presenter engine.
		let prompts = 0;
		source.on('dialog', (dialog) => {
			prompts++;
			void dialog.dismiss();
		});
		await source.locator(`a[href="/${slug}/inbox"]`).first().click();
		await expect(source).toHaveURL(new RegExp(`/${slug}/inbox$`));
		await expect(source.locator('button[title="Stop sharing"]')).toBeVisible();
		await expectVideoContinues(ownViewerB, shareId);
		await expectVideoContinues(otherViewerA, shareId);
		expect(prompts).toBe(0);
		await source.locator(`a[href="/${slug}/shares"]`).first().click();
		await expect(source.locator(`[data-share-id="${shareId}"]`)).toBeVisible();
		expect(await source.evaluate(() => window.__kuLive.captureCalls)).toBe(1);

		// Replacing the video track keeps the same share and remote streams.
		await source.getByRole('button', { name: 'Change what you share', exact: true }).click();
		await expect.poll(() => source.evaluate(() => window.__kuLive.captureCalls)).toBe(2);
		await expect
			.poll(() => source.evaluate(() => window.__kuLive.captures[0].getVideoTracks()[0].readyState))
			.toBe('ended');
		expect((await activeShares(presenter, slug))[0].id).toBe(shareId);
		for (const page of [ownViewerB, otherViewerA, otherViewerB]) {
			await expectVideoContinues(page, shareId);
			await expect.poll(() => hasGreenSource(page, shareId)).toBe(true);
		}

		// Drop actual signaling sockets. Automatic layout reconnect and share
		// re-announcement must recover; no synthetic reconnect event is fired.
		const viewerSockets = await otherViewerA.evaluate(() => window.__kuLive.sockets.length);
		await closeWorkspaceSocket(otherViewerA);
		await expect.poll(() => otherViewerA.evaluate(() => window.__kuLive.sockets.length)).toBeGreaterThan(viewerSockets);
		await expectDecodedVideo(otherViewerA, shareId);
		await expectVideoContinues(otherViewerA, shareId);
		const sourceSockets = await source.evaluate(() => window.__kuLive.sockets.length);
		await closeWorkspaceSocket(source);
		await expect.poll(() => source.evaluate(() => window.__kuLive.sockets.length)).toBeGreaterThan(sourceSockets);
		for (const page of [ownViewerB, otherViewerA, otherViewerB]) await expectVideoContinues(page, shareId);
		await expect.poll(async () => (await activeShares(presenter, slug))[0]?.viewer_count).toBe(2);

		await source
			.locator(`[data-share-id="${shareId}"]`)
			.getByRole('button', { name: 'Stop sharing', exact: true })
			.click();
		await expect.poll(async () => (await activeShares(presenter, slug)).length).toBe(0);
		for (const page of [ownViewerB, otherViewerA, otherViewerB]) {
			await expect(page.locator('[data-share-id]')).toHaveCount(0);
		}
		await expect
			.poll(() =>
				source.evaluate(() =>
					window.__kuLive.captures.every((capture) =>
						capture.getTracks().every((track) => track.readyState === 'ended')
					)
				)
			)
			.toBe(true);

		await source.getByRole('button', { name: 'Share my screen', exact: true }).click();
		await expect.poll(async () => (await activeShares(presenter, slug)).length).toBe(1);
		const restartedShareId = (await activeShares(presenter, slug))[0].id;
		expect(restartedShareId).not.toBe(shareId);
		for (const page of [ownViewerB, otherViewerA, otherViewerB]) await expectDecodedVideo(page, restartedShareId);
		await source
			.locator(`[data-share-id="${restartedShareId}"]`)
			.getByRole('button', { name: 'Stop sharing', exact: true })
			.click();
		await expect.poll(async () => (await activeShares(presenter, slug)).length).toBe(0);
	} finally {
		const evidence = await Promise.all(
			pages
				.filter(({ page }) => !page.isClosed())
				.map(async ({ role, page }) => ({
					role,
					media: await receivedVideo(page).catch(() => null),
					signals: await page
						.evaluate(() =>
							window.__kuLive.messages
								.filter((message) => message.type === 'share.signal' || message.type === 'share.presence')
								.map((message) => ({
									type: message.type,
									shareId: message.payload?.share_id,
									peer: message.payload?.from_peer,
									negotiation: message.payload?.negotiation_id,
									signalType: message.payload?.data?.type,
									viewerPeers: message.payload?.viewer_peers
								}))
						)
						.catch(() => [])
				}))
		);
		await testInfo.attach('real-transport-evidence', {
			body: JSON.stringify(evidence, null, 2),
			contentType: 'application/json'
		});
		for (const context of contexts) await context.close();
	}
});

test('switching workspaces ends the old capture and its remote viewer', async ({ browser, baseURL }) => {
	const context = await newUser(browser, baseURL!, 'workspace-switch');
	try {
		const originalSlug = await createWorkspace(context);
		const nextSlug = await createWorkspace(context);
		const source = await openWall(context, originalSlug);
		await installCanvasCapture(source, 'workspace-switch');
		await source.getByRole('button', { name: 'Share my screen', exact: true }).click();
		await expect.poll(async () => (await activeShares(context, originalSlug)).length).toBe(1);
		const shareId = (await activeShares(context, originalSlug))[0].id;
		const viewer = await openWall(context, originalSlug);
		await expectDecodedVideo(viewer, shareId);
		let prompts = 0;
		source.on('dialog', (dialog) => {
			prompts++;
			void dialog.dismiss();
		});
		await source.getByText(`Local screen-share test ${originalSlug}`, { exact: true }).first().click();
		await source.getByText(`Local screen-share test ${nextSlug}`, { exact: true }).click();
		await expect(source).toHaveURL(new RegExp(`/${nextSlug}/my-issues$`));
		expect(prompts).toBe(0);
		await expect.poll(async () => (await activeShares(context, originalSlug)).length).toBe(0);
		expect(await activeShares(context, nextSlug)).toEqual([]);
		await expect(viewer.locator('[data-share-id]')).toHaveCount(0);
		await expect(source.locator('button[title="Stop sharing"]')).toHaveCount(0);
		await expect
			.poll(() =>
				source.evaluate(() =>
					window.__kuLive.captures.every((capture) =>
						capture.getTracks().every((track) => track.readyState === 'ended')
					)
				)
			)
			.toBe(true);
	} finally {
		await context.close();
	}
});
