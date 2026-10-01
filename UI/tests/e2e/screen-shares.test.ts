import { expect, test, type Page } from '@playwright/test';

const shareId = '00000000-0000-0000-0000-000000000090';
const presenterToken = '00000000-0000-0000-0000-000000000099';
const presenterPeer = '00000000-0000-0000-0000-000000000095';
const presenter = {
	id: '00000000-0000-0000-0000-000000000003',
	name: 'Filippo Rossi',
	display_name: 'Filippo',
	avatar_url: null
};

function workspacePayload(enabled: boolean) {
	return {
		id: '00000000-0000-0000-0000-000000000002',
		name: 'Test Workspace',
		slug: 'test',
		logo_url: null,
		owner_id: '00000000-0000-0000-0000-000000000001',
		share_link_min_role: 'member',
		current_user_role: 'member',
		screen_sharing_enabled: enabled,
		created_at: '2026-01-01T00:00:00Z',
		updated_at: '2026-01-01T00:00:00Z'
	};
}

const activeShare = {
	id: shareId,
	workspace_id: '00000000-0000-0000-0000-000000000002',
	started_by: presenter.id,
	started_by_user: presenter,
	status: 'active',
	viewer_count: 2,
	created_at: new Date(Date.now() - 5 * 60 * 1000).toISOString(),
	ended_at: null
};

const endedShare = {
	...activeShare,
	id: '00000000-0000-0000-0000-000000000091',
	status: 'ended',
	viewer_count: 0,
	ended_at: new Date(Date.now() - 26 * 60 * 60 * 1000).toISOString(),
	created_at: new Date(Date.now() - 27 * 60 * 60 * 1000).toISOString()
};

async function stubCommon(page: import('@playwright/test').Page, enabled: boolean) {
	// Flips to empty when the share.ended test fires, modeling the
	// server-side end the event represents.
	let shareEnded = false;
	await page.exposeFunction('__endShare', () => {
		shareEnded = true;
	});
	await page.route('https://raw.githubusercontent.com/carbogninalberto/kuayle/main/UI/static/releases.json', (route) =>
		route.fulfill({ json: [] })
	);
	await page.route('**/api/**', async (route) => {
		const path = new URL(route.request().url()).pathname;
		if (path === '/api/auth/me') {
			return route.fulfill({
				json: {
					id: '00000000-0000-0000-0000-000000000001',
					email: 'test@example.com',
					name: 'Test User',
					display_name: 'Test User',
					avatar_url: null
				}
			});
		}
		if (path === '/api/preferences') {
			return route.fulfill({
				json: {
					font_size: 'default',
					pointer_cursors: true,
					theme_mode: 'dark',
					light_theme: 'light',
					dark_theme: 'dark',
					workflow_sort_mode: 'default',
					workflow_sort_order: ['backlog', 'unstarted', 'started', 'completed', 'cancelled'],
					team_workflow_sort_overrides: {}
				}
			});
		}
		if (path === '/api/workspaces/test') {
			return route.fulfill({ json: workspacePayload(enabled) });
		}
		if (path === '/api/workspaces') {
			return route.fulfill({ json: [workspacePayload(enabled)] });
		}
		if (path === '/api/workspaces/test/teams') {
			return route.fulfill({ json: [] });
		}
		if (path === '/api/workspaces/test/members') {
			return route.fulfill({
				json: [
					{
						user_id: presenter.id,
						email: 'filippo@example.com',
						name: presenter.name,
						avatar_url: null,
						role: 'member',
						created_at: '2026-01-01T00:00:00Z'
					}
				]
			});
		}
		if (path === '/api/workspaces/test/projects') {
			return route.fulfill({ json: [] });
		}
		if (path === '/api/workspaces/test/labels') {
			return route.fulfill({ json: [] });
		}
		if (path === '/api/workspaces/test/views') {
			return route.fulfill({ json: [] });
		}
		if (path === '/api/notifications') {
			return route.fulfill({ json: { unread_count: 0, notifications: [] } });
		}
		if (path === '/api/workspaces/test/shares') {
			const activeList = enabled && !shareEnded ? [activeShare] : [];
			const recentList = enabled
				? shareEnded
					? [{ ...activeShare, status: 'ended', viewer_count: 0, ended_at: new Date().toISOString() }, endedShare]
					: [endedShare]
				: [];
			return route.fulfill({ json: { active: activeList, recent: recentList } });
		}
		return route.fulfill({ json: [] });
	});
}

test('shares module hidden when the workspace disables it', async ({ page }) => {
	await stubCommon(page, false);
	await page.goto('/test/inbox');
	await expect(page.getByRole('link', { name: 'Screen sharing' })).toHaveCount(0);
});

test('shares module visible when enabled', async ({ page }) => {
	await stubCommon(page, true);
	await page.goto('/test/inbox');
	await expect(page.getByRole('link', { name: 'Screen sharing' })).toBeVisible();
});

test('wall renders the presenter-named viewer tile', async ({ page }) => {
	await stubCommon(page, true);
	await page.goto('/test/shares');

	await expect(page.getByRole('heading', { name: 'Screen sharing' })).toBeVisible();
	// One tile for the active share: viewer tiles mount immediately (no
	// join token round-trip) and carry the presenter name while the peer
	// connection negotiates.
	const tile = page.locator(`[data-share-id="${shareId}"]`);
	await expect(tile).toHaveCount(1);
	await expect(tile).toContainText('Filippo');
	// Shares are presenter-named: no free-text title is rendered anywhere.
	await expect(page.getByText('Bug triage walkthrough')).toHaveCount(0);
});

test('start flow posts a titleless share and renders the own tile', async ({ page }) => {
	await stubCommon(page, true);
	let createdBody: unknown = null;
	await page.route('**/api/workspaces/test/shares', async (route) => {
		if (route.request().method() !== 'POST') {
			return route.fulfill({ json: { active: [], recent: [endedShare] } });
		}
		createdBody = route.request().postDataJSON();
		return route.fulfill({
			json: {
				share: { ...activeShare, started_by: '00000000-0000-0000-0000-000000000001' },
				presenter_token: presenterToken
			}
		});
	});
	// Synthetic capture: the picker never opens in the rig; an empty
	// MediaStream satisfies the capture-first contract.
	await page.addInitScript(() => {
		navigator.mediaDevices.getUserMedia = async () => new MediaStream();
		(navigator.mediaDevices as any).getDisplayMedia = async () => new MediaStream();
	});

	await page.goto('/test/shares');
	await page.getByRole('button', { name: 'Share my screen' }).click();

	// No title dialog exists anymore: the share starts immediately and
	// the presenter's own tile mounts.
	await expect(page.locator(`[data-share-id="${shareId}"]`)).toHaveCount(1);
	await expect(page.locator(`[data-share-id="${shareId}"]`)).toContainText('Sharing');
	await expect.poll(() => createdBody).toEqual({});
	// The header action hides while presenting.
	await expect(page.getByRole('button', { name: 'Share my screen' })).toHaveCount(0);
});

test('share survives in-app navigation and stops from the sidebar', async ({ page }) => {
	await stubCommon(page, true);
	let createCalls = 0;
	let endCalls = 0;
	await page.route('**/api/workspaces/test/shares', async (route) => {
		if (route.request().method() === 'POST') {
			createCalls++;
			return route.fulfill({
				json: {
					share: { ...activeShare, started_by: '00000000-0000-0000-0000-000000000001' },
					presenter_token: presenterToken
				}
			});
		}
		return route.fulfill({ json: { active: [], recent: [endedShare] } });
	});
	await page.route('**/api/workspaces/test/shares/*/end', (route) => {
		endCalls++;
		return route.fulfill({ json: {} });
	});
	await page.addInitScript(() => {
		navigator.mediaDevices.getUserMedia = async () => new MediaStream();
		(navigator.mediaDevices as any).getDisplayMedia = async () => new MediaStream();
	});

	let dialogs = 0;
	page.on('dialog', (d) => {
		dialogs++;
		void d.dismiss();
	});

	await page.goto('/test/shares');
	await page.getByRole('button', { name: 'Share my screen' }).click();
	await expect(page.locator(`[data-share-id="${shareId}"]`)).toContainText('Sharing');

	// Navigate away mid-share: no confirmation, and the sidebar keeps a
	// live indicator + Stop control on every page.
	await page.locator('a[href="/test/inbox"]').first().click();
	await expect(page).toHaveURL(/\/test\/inbox/);
	expect(dialogs, 'navigation must not prompt while sharing').toBe(0);
	await expect(page.locator('button[title="Stop sharing"]')).toBeVisible();

	// Return to the wall: the share is still live without a second create.
	await page.locator('a[href="/test/shares"]').first().click();
	await expect(page.locator(`[data-share-id="${shareId}"]`)).toContainText('Sharing');
	expect(createCalls).toBe(1);

	// Stop from the sidebar: the session ends (server notified, wall
	// reflects it on the next visit).
	await page.locator('button[title="Stop sharing"]').click();
	await expect.poll(() => endCalls, { timeout: 10_000 }).toBeGreaterThan(0);
	await expect(page.locator(`[data-share-id="${shareId}"]`)).toHaveCount(0, { timeout: 10_000 });
	expect(dialogs).toBe(0);
});

test('share.ended event removes the tile from the wall', async ({ page }) => {
	await stubCommon(page, true);
	await page.goto('/test/shares');
	await expect(page.locator(`[data-share-id="${shareId}"]`)).toHaveCount(1);

	await page.evaluate((id) => {
		void (window as any).__endShare?.();
		window.dispatchEvent(new CustomEvent('ws:share.ended', { detail: { id } }));
	}, shareId);

	await expect(page.locator(`[data-share-id="${shareId}"]`)).toHaveCount(0);
});

test('wall fills the page: one share takes everything, more split evenly', async ({ page }) => {
	await stubCommon(page, true);
	await page.goto('/test/shares');

	// A single share fills the whole grid: one column, one row.
	const grid = page.locator('[data-tile-count]');
	await expect(page.locator('[data-share-id]')).toHaveCount(1);
	await expect(grid).toHaveAttribute('data-tile-count', '1');
	const singleColumns = await grid.evaluate((el) => getComputedStyle(el).gridTemplateColumns.split(' ').length);
	expect(singleColumns).toBe(1);

	// Three shares arrange in the squarest grid: 2 columns × 2 rows.
	const others = [1, 2].map((n) => ({
		...activeShare,
		id: `00000000-0000-0000-0000-00000000009${n}`,
		started_by: `00000000-0000-0000-0000-0000000000${10 + n}`,
		started_by_user: {
			...presenter,
			id: `00000000-0000-0000-0000-0000000000${10 + n}`,
			name: `Sharer ${n}`,
			display_name: `Sharer ${n}`
		}
	}));
	await page.route('**/api/workspaces/test/shares', (route) =>
		route.fulfill({ json: { active: [activeShare, ...others], recent: [endedShare] } })
	);
	await page.evaluate(() => {
		window.dispatchEvent(
			new CustomEvent('ws:share.started', { detail: { id: '00000000-0000-0000-0000-000000000091' } })
		);
	});

	await expect(page.locator('[data-share-id]')).toHaveCount(3);
	await expect(grid).toHaveAttribute('data-tile-count', '3');
	// 2 columns for 3 tiles (ceil(sqrt(3))), 2 rows.
	const columns = await grid.evaluate((el) => getComputedStyle(el).gridTemplateColumns.split(' ').length);
	const rows = await grid.evaluate((el) => getComputedStyle(el).gridTemplateRows.split(' ').length);
	expect(columns).toBe(2);
	expect(rows).toBe(2);
});

// Native video tracks keep the rig faithful to MediaStream.addTrack/srcObject,
// while deterministic peers let us exercise failures without a live presenter.
async function installPeerRig(page: Page) {
	await page.routeWebSocket('**/api/workspaces/*/ws', () => {});
	await page.addInitScript(() => {
		const rig = {
			peers: [] as MockPeer[],
			messages: [] as Array<{ workspaceSlug: string; type: string; payload: any }>,
			streams: [] as MediaStream[],
			stops: [] as number[],
			captureCalls: 0,
			captureAudio: false,
			pickerMode: 'immediate' as 'immediate' | 'cancel' | 'pending',
			completePicker: null as (() => void) | null,
			makeStream() {
				const canvas = document.createElement('canvas');
				canvas.width = 8;
				canvas.height = 8;
				const stream = canvas.captureStream(1);
				let audioContext: AudioContext | null = null;
				if (rig.captureAudio) {
					audioContext = new AudioContext();
					const destination = audioContext.createMediaStreamDestination();
					const oscillator = audioContext.createOscillator();
					oscillator.connect(destination);
					oscillator.start();
					stream.addTrack(destination.stream.getAudioTracks()[0]);
				}
				const index = rig.streams.length;
				rig.streams.push(stream);
				rig.stops.push(0);
				for (const track of stream.getTracks()) {
					const originalStop = track.stop.bind(track);
					track.stop = () => {
						rig.stops[index]++;
						originalStop();
						if (track.kind === 'audio') void audioContext?.close();
					};
				}
				return stream;
			}
		};
		class MockPeer {
			connectionState: RTCPeerConnectionState = 'new';
			signalingState: RTCSignalingState = 'stable';
			localDescription: RTCSessionDescriptionInit | null = null;
			remoteDescription: RTCSessionDescriptionInit | null = null;
			remoteDescriptions: RTCSessionDescriptionInit[] = [];
			candidates: RTCIceCandidateInit[] = [];
			senders: any[] = [];
			transceivers: any[] = [];
			closed = false;
			onconnectionstatechange: (() => void) | null = null;
			onicecandidate: ((event: { candidate: { toJSON(): RTCIceCandidateInit } }) => void) | null = null;
			private trackHandler: ((event: { track: MediaStreamTrack }) => void) | null = null;
			lastTrackHandler: ((event: { track: MediaStreamTrack }) => void) | null = null;
			set ontrack(handler: ((event: { track: MediaStreamTrack }) => void) | null) {
				this.trackHandler = handler;
				if (handler) this.lastTrackHandler = handler;
			}
			get ontrack() {
				return this.trackHandler;
			}
			constructor() {
				rig.peers.push(this);
			}
			addTransceiver(kind: string, init: RTCRtpTransceiverInit = {}) {
				const sender = {
					track: null as MediaStreamTrack | null,
					replaceTrack: async (next: MediaStreamTrack | null) => {
						sender.track = next;
					},
					getParameters: () => ({}),
					setParameters: async () => {}
				};
				const transceiver = {
					sender,
					receiver: { track: { kind } },
					mid: null as string | null,
					direction: init.direction ?? 'sendrecv',
					currentDirection: null as RTCRtpTransceiverDirection | null,
					fromAddTrack: false
				};
				this.senders.push(sender);
				this.transceivers.push(transceiver);
				return transceiver;
			}
			getTransceivers() {
				return this.transceivers;
			}
			addTrack(track: MediaStreamTrack) {
				const transceiver = this.addTransceiver(track.kind);
				transceiver.sender.track = track;
				transceiver.fromAddTrack = true;
				return transceiver.sender;
			}
			getSenders() {
				return this.senders;
			}
			async createOffer() {
				return { type: 'offer', sdp: `offer-${rig.peers.indexOf(this)}` };
			}
			async createAnswer() {
				return { type: 'answer', sdp: `answer-${rig.peers.indexOf(this)}` };
			}
			async setLocalDescription(description: RTCSessionDescriptionInit) {
				this.localDescription = description;
				this.signalingState = description.type === 'offer' ? 'have-local-offer' : 'stable';
				if (description.type === 'answer') {
					for (const transceiver of this.transceivers) {
						if (transceiver.mid !== null)
							transceiver.currentDirection = ['sendonly', 'sendrecv'].includes(transceiver.direction)
								? 'sendonly'
								: 'inactive';
					}
				}
			}
			async setRemoteDescription(description: RTCSessionDescriptionInit) {
				this.remoteDescription = description;
				this.remoteDescriptions.push(description);
				this.signalingState = description.type === 'offer' ? 'have-remote-offer' : 'stable';
				if (description.type === 'offer') {
					// Match the incoming offer's media sections. addTrack can be
					// reused; an unassociated addTransceiver cannot be reused.
					for (const [index, kind] of ['video', 'audio'].entries()) {
						const transceiver =
							this.transceivers.find(
								(candidate) =>
									candidate.mid === null && candidate.fromAddTrack && candidate.receiver.track.kind === kind
							) ?? this.addTransceiver(kind, { direction: 'recvonly' });
						transceiver.mid = String(index);
					}
				}
			}
			async addIceCandidate(candidate: RTCIceCandidateInit) {
				this.candidates.push(candidate);
			}
			close() {
				this.closed = true;
				this.connectionState = 'closed';
			}
			setConnectionState(state: RTCPeerConnectionState) {
				this.connectionState = state;
				this.onconnectionstatechange?.();
			}
			fireTrack(stale = false) {
				const track = rig.makeStream().getVideoTracks()[0];
				(stale ? this.lastTrackHandler : this.ontrack)?.({ track });
			}
			fireCandidate(candidate: RTCIceCandidateInit) {
				this.onicecandidate?.({ candidate: { toJSON: () => candidate } });
			}
		}
		(window as any).__shareRig = rig;
		(window as any).RTCPeerConnection = MockPeer;
		HTMLMediaElement.prototype.play = async () => {};
		navigator.mediaDevices.getDisplayMedia = async () => {
			rig.captureCalls++;
			if (rig.pickerMode === 'cancel') throw new DOMException('Picker cancelled', 'AbortError');
			if (rig.pickerMode === 'pending') {
				return new Promise<MediaStream>((resolve) => {
					rig.completePicker = () => resolve(rig.makeStream());
				});
			}
			return rig.makeStream();
		};
		window.addEventListener('ws:send', (event) => rig.messages.push((event as CustomEvent).detail));
	});
}

async function sentSignals(page: Page, type: 'offer' | 'answer' | 'candidate') {
	return page.evaluate(
		(signalType) =>
			(window as any).__shareRig.messages.filter(
				(message: any) => message.type === 'share.signal' && message.payload.data.type === signalType
			),
		type
	);
}

async function announcePresenter(page: Page, online: boolean) {
	await page.evaluate(
		({ id, peer, online }) => {
			window.dispatchEvent(
				new CustomEvent('ws:share.presence', {
					detail: {
						share_id: id,
						presenter_online: online,
						presenter_peer: online ? peer : '',
						viewers: 1,
						viewer_peers: ['00000000-0000-0000-0000-000000000096']
					}
				})
			);
		},
		{ id: shareId, peer: presenterPeer, online }
	);
}

async function stubOwnShare(page: Page, holdCreate?: Promise<void>) {
	await stubCommon(page, true);
	const requests = { creates: 0, ends: [] as unknown[] };
	await page.route('**/api/workspaces/test/shares', async (route) => {
		if (route.request().method() !== 'POST') return route.fulfill({ json: { active: [], recent: [] } });
		requests.creates++;
		if (holdCreate) await holdCreate;
		return route.fulfill({
			json: {
				share: { ...activeShare, started_by: '00000000-0000-0000-0000-000000000001' },
				presenter_token: presenterToken
			}
		});
	});
	await page.route('**/api/workspaces/test/shares/*/end', (route) => {
		requests.ends.push(route.request().postDataJSON());
		return route.fulfill({ json: {} });
	});
	await installPeerRig(page);
	return requests;
}

test('a previously live viewer recovers a failed connection on a fresh peer', async ({ page }) => {
	await stubCommon(page, true);
	await installPeerRig(page);
	await page.goto('/test/shares');
	await expect.poll(async () => (await sentSignals(page, 'offer')).length).toBe(1);
	const tile = page.locator(`[data-share-id="${shareId}"]`);
	await page.evaluate(() => (window as any).__shareRig.peers[0].setConnectionState('connected'));
	await expect(tile.getByText('Live', { exact: true })).toBeVisible();
	await page.evaluate(() => (window as any).__shareRig.peers[0].setConnectionState('failed'));
	await expect.poll(async () => (await sentSignals(page, 'offer')).length).toBe(2);
	await expect(tile.getByText('Live', { exact: true })).toBeHidden();
	expect(await page.evaluate(() => (window as any).__shareRig.peers[0].closed)).toBe(true);
	await page.evaluate(() => (window as any).__shareRig.peers[1].setConnectionState('connected'));
	await expect(tile.getByText('Live', { exact: true })).toBeVisible();
});

test('a disconnected live viewer is recycled when its watchdog expires', async ({ page }) => {
	await page.clock.install();
	await stubCommon(page, true);
	await installPeerRig(page);
	await page.goto('/test/shares');
	await expect.poll(async () => (await sentSignals(page, 'offer')).length).toBe(1);
	await page.evaluate(() => {
		const peer = (window as any).__shareRig.peers[0];
		peer.setConnectionState('connected');
		peer.setConnectionState('disconnected');
	});
	await page.clock.runFor(10_100);
	await expect.poll(async () => (await sentSignals(page, 'offer')).length).toBe(2);
	await page.evaluate(() => (window as any).__shareRig.peers[1].setConnectionState('connected'));
	await expect(page.locator(`[data-share-id="${shareId}"]`).getByText('Live', { exact: true })).toBeVisible();
});

test('a formerly live viewer revives after presenter offline grace and re-announcement', async ({ page }) => {
	await page.clock.install();
	await stubCommon(page, true);
	await installPeerRig(page);
	await page.goto('/test/shares');
	await expect.poll(async () => (await sentSignals(page, 'offer')).length).toBe(1);
	await page.evaluate(() => (window as any).__shareRig.peers[0].setConnectionState('connected'));
	const tile = page.locator(`[data-share-id="${shareId}"]`);
	await expect(tile.getByText('Live', { exact: true })).toBeVisible();
	await announcePresenter(page, false);
	await page.clock.runFor(30_100);
	await expect(tile.getByText('Live', { exact: true })).toBeHidden();
	await announcePresenter(page, true);
	await expect.poll(async () => (await sentSignals(page, 'offer')).length).toBe(2);
	await page.evaluate(() => (window as any).__shareRig.peers[1].setConnectionState('connected'));
	await expect(tile.getByText('Live', { exact: true })).toBeVisible();
});

test('viewer retries ignore stale negotiation answers, candidates, tracks, and other workspace reconnects', async ({
	page
}) => {
	await stubCommon(page, true);
	await installPeerRig(page);
	await page.goto('/test/shares');
	await expect.poll(async () => (await sentSignals(page, 'offer')).length).toBe(1);
	const initial = (await sentSignals(page, 'offer'))[0];
	await page.evaluate(() =>
		window.dispatchEvent(new CustomEvent('ws:reconnected', { detail: { workspaceSlug: 'other' } }))
	);
	expect((await sentSignals(page, 'offer')).length).toBe(1);
	await page.evaluate(() =>
		window.dispatchEvent(new CustomEvent('ws:reconnected', { detail: { workspaceSlug: 'test' } }))
	);
	await expect.poll(async () => (await sentSignals(page, 'offer')).length).toBe(2);
	const retry = (await sentSignals(page, 'offer'))[1];
	expect(retry.payload.negotiation_id).not.toBe(initial.payload.negotiation_id);
	await announcePresenter(page, true);
	await page.evaluate(
		({ id, from, peer, oldNegotiation }) => {
			const rig = (window as any).__shareRig;
			window.dispatchEvent(
				new CustomEvent('ws:share.signal', {
					detail: {
						share_id: id,
						from,
						from_peer: peer,
						negotiation_id: oldNegotiation,
						data: { type: 'answer', sdp: 'obsolete-answer' }
					}
				})
			);
			window.dispatchEvent(
				new CustomEvent('ws:share.signal', {
					detail: {
						share_id: id,
						from,
						from_peer: peer,
						negotiation_id: oldNegotiation,
						data: { type: 'candidate', candidate: { candidate: 'obsolete-candidate' } }
					}
				})
			);
			rig.peers[0].fireTrack(true);
		},
		{ id: shareId, from: presenter.id, peer: presenterPeer, oldNegotiation: initial.payload.negotiation_id }
	);
	await expect(page.locator(`[data-share-id="${shareId}"]`).getByText('Live', { exact: true })).toBeHidden();
	expect(await page.evaluate(() => (window as any).__shareRig.peers[1].remoteDescriptions.length)).toBe(0);
	expect(await page.evaluate(() => (window as any).__shareRig.peers[1].candidates.length)).toBe(0);
	await page.evaluate(
		({ id, from, peer, negotiation }) =>
			window.dispatchEvent(
				new CustomEvent('ws:share.signal', {
					detail: {
						share_id: id,
						from,
						from_peer: peer,
						negotiation_id: negotiation,
						data: { type: 'answer', sdp: 'current-answer' }
					}
				})
			),
		{ id: shareId, from: presenter.id, peer: presenterPeer, negotiation: retry.payload.negotiation_id }
	);
	await expect.poll(() => page.evaluate(() => (window as any).__shareRig.peers[1].remoteDescriptions.length)).toBe(1);
	await page.evaluate(() => (window as any).__shareRig.peers[1].fireTrack());
	await expect(page.locator(`[data-share-id="${shareId}"]`).getByText('Live', { exact: true })).toBeVisible();
});

test('presenter routes same-user viewer tabs separately and closes only a departed tab', async ({ page }) => {
	await stubOwnShare(page);
	await page.goto('/test/shares');
	await page.getByRole('button', { name: 'Share my screen' }).click();
	await expect(page.locator(`[data-share-id="${shareId}"]`)).toContainText('Sharing');
	const viewerUser = '00000000-0000-0000-0000-000000000020';
	const peers = ['00000000-0000-0000-0000-000000000021', '00000000-0000-0000-0000-000000000022'];
	const negotiations = ['00000000-0000-0000-0000-000000000023', '00000000-0000-0000-0000-000000000024'];
	await page.evaluate(
		({ id, user, peers, negotiations }) => {
			for (const [index, peer] of peers.entries())
				window.dispatchEvent(
					new CustomEvent('ws:share.signal', {
						detail: {
							share_id: id,
							from: user,
							from_peer: peer,
							negotiation_id: negotiations[index],
							data: { type: 'offer', sdp: `viewer-offer-${index}` }
						}
					})
				);
		},
		{ id: shareId, user: viewerUser, peers, negotiations }
	);
	await expect.poll(async () => (await sentSignals(page, 'answer')).length).toBe(2);
	const answers = await sentSignals(page, 'answer');
	expect(answers.map((answer: any) => answer.payload.to_peer)).toEqual(peers);
	expect(answers.map((answer: any) => answer.payload.negotiation_id)).toEqual(negotiations);
	expect(answers.every((answer: any) => answer.workspaceSlug === 'test' && !answer.payload.to)).toBe(true);
	await page.evaluate(() => {
		for (const peer of (window as any).__shareRig.peers) peer.fireCandidate({ candidate: 'candidate' });
	});
	await expect.poll(async () => (await sentSignals(page, 'candidate')).length).toBe(2);
	expect((await sentSignals(page, 'candidate')).map((message: any) => message.payload.to_peer)).toEqual(peers);
	await page.evaluate(
		({ id, remainingPeer, presenterPeer }) =>
			window.dispatchEvent(
				new CustomEvent('ws:share.presence', {
					detail: {
						share_id: id,
						presenter_online: true,
						presenter_peer: presenterPeer,
						viewers: 1,
						viewer_peers: [remainingPeer]
					}
				})
			),
		{ id: shareId, remainingPeer: peers[1], presenterPeer }
	);
	await expect
		.poll(() => page.evaluate(() => (window as any).__shareRig.peers.map((peer: any) => peer.closed)))
		.toEqual([true, false]);
});

test('cancelling a source switch keeps the current capture and share alive', async ({ page }) => {
	const requests = await stubOwnShare(page);
	await page.goto('/test/shares');
	await page.getByRole('button', { name: 'Share my screen' }).click();
	const tile = page.locator(`[data-share-id="${shareId}"]`);
	await expect(tile).toContainText('Sharing');
	await page.evaluate(() => {
		(window as any).__shareRig.pickerMode = 'cancel';
	});
	await tile.getByRole('button', { name: 'Change what you share' }).click();
	await expect.poll(() => page.evaluate(() => (window as any).__shareRig.captureCalls)).toBe(2);
	await expect(tile.getByRole('button', { name: 'Change what you share' })).toBeEnabled();
	await expect(tile).toContainText('Sharing');
	expect(requests.ends).toEqual([]);
	expect(await page.evaluate(() => (window as any).__shareRig.stops[0])).toBe(0);
});

test('stopping during a pending source picker releases both old and late capture tracks', async ({ page }) => {
	const requests = await stubOwnShare(page);
	await page.goto('/test/shares');
	await page.getByRole('button', { name: 'Share my screen' }).click();
	const tile = page.locator(`[data-share-id="${shareId}"]`);
	await expect(tile).toContainText('Sharing');
	await page.evaluate(() => {
		(window as any).__shareRig.pickerMode = 'pending';
	});
	await tile.getByRole('button', { name: 'Change what you share' }).click();
	await expect.poll(() => page.evaluate(() => (window as any).__shareRig.captureCalls)).toBe(2);
	await tile.getByRole('button', { name: 'Stop sharing' }).click();
	await expect(tile).toHaveCount(0);
	expect(requests.ends).toEqual([{ presenter_token: presenterToken }]);
	await page.evaluate(() => (window as any).__shareRig.completePicker());
	await expect.poll(() => page.evaluate(() => (window as any).__shareRig.stops)).toEqual([1, 1]);
	await expect(page.getByRole('button', { name: 'Share my screen' })).toBeEnabled();
	await expect(tile).toHaveCount(0);
});

test('duplicate start attempts stay blocked until capture and share creation both finish', async ({ page }) => {
	let finishCreate!: () => void;
	const pendingCreate = new Promise<void>((resolve) => {
		finishCreate = resolve;
	});
	const requests = await stubOwnShare(page, pendingCreate);
	await page.goto('/test/shares');
	const start = page.getByRole('button', { name: 'Share my screen' });
	await start.click();
	await expect.poll(() => requests.creates).toBe(1);
	await expect(start).toBeDisabled();
	// Programmatically repeat the event to exercise the session guard too.
	await start.evaluate((button) => button.dispatchEvent(new MouseEvent('click', { bubbles: true })));
	expect(requests.creates).toBe(1);
	expect(await page.evaluate(() => (window as any).__shareRig.captureCalls)).toBe(1);
	finishCreate();
	await expect(page.locator(`[data-share-id="${shareId}"]`)).toContainText('Sharing');
	expect(requests.creates).toBe(1);
	const joins = await page.evaluate(() =>
		(window as any).__shareRig.messages.filter(
			(message: any) =>
				message.type === 'share.presence' && message.payload.role === 'presenter' && message.payload.action === 'join'
		)
	);
	expect(joins).toHaveLength(1);
	expect(joins[0].payload.presenter_token).toBe(presenterToken);
});

test('workspace navigation invalidates a pending picker and allows capture before the new socket opens', async ({
	page
}) => {
	const requests = await stubOwnShare(page);
	const otherShareId = '00000000-0000-0000-0000-000000000092';
	const otherWorkspace = {
		...workspacePayload(true),
		id: '00000000-0000-0000-0000-000000000093',
		slug: 'other',
		name: 'Other Workspace'
	};
	let otherCreates = 0;
	await page.route('**/api/workspaces', (route) => route.fulfill({ json: [workspacePayload(true), otherWorkspace] }));
	await page.route('**/api/workspaces/other{,/**}', (route) => {
		const path = new URL(route.request().url()).pathname;
		if (path === '/api/workspaces/other') return route.fulfill({ json: otherWorkspace });
		if (path === '/api/workspaces/other/shares') {
			if (route.request().method() === 'POST') {
				otherCreates++;
				return route.fulfill({
					json: {
						share: {
							...activeShare,
							id: otherShareId,
							workspace_id: otherWorkspace.id,
							started_by: '00000000-0000-0000-0000-000000000001'
						},
						presenter_token: presenterToken
					}
				});
			}
			return route.fulfill({ json: { active: [], recent: [] } });
		}
		if (path === '/api/workspaces/other/issues') {
			return route.fulfill({ json: { data: [], total_count: 0, page: 1, per_page: 50, has_more: false } });
		}
		return route.fulfill({ json: [] });
	});
	await page.addInitScript(() => {
		const Socket = window.WebSocket;
		class PendingSocket extends EventTarget {
			readyState: number = Socket.CONNECTING;
			onopen: (() => void) | null = null;
			onclose: (() => void) | null = null;
			onerror: (() => void) | null = null;
			onmessage: ((event: MessageEvent) => void) | null = null;
			close() {
				this.readyState = Socket.CLOSED;
				this.onclose?.();
			}
		}
		(window as any).WebSocket = class extends Socket {
			constructor(url: string | URL, protocols?: string | string[]) {
				if (String(url).includes('/api/workspaces/other/ws')) return new PendingSocket() as any;
				super(url, protocols);
			}
		};
		(window as any).__workspaceReconnects = [];
		window.addEventListener('ws:reconnected', (event) => {
			(window as any).__workspaceReconnects.push((event as CustomEvent).detail.workspaceSlug);
		});
	});
	await page.goto('/test/shares');
	await expect.poll(() => page.evaluate(() => (window as any).__workspaceReconnects)).toContain('test');
	await page.evaluate(() => {
		(window as any).__shareRig.pickerMode = 'pending';
	});
	await page.getByRole('button', { name: 'Share my screen', exact: true }).click();
	await expect.poll(() => page.evaluate(() => (window as any).__shareRig.captureCalls)).toBe(1);
	await page.getByText('Test Workspace', { exact: true }).first().click();
	await page.getByText('Other Workspace', { exact: true }).click();
	await expect(page).toHaveURL(/\/other\/my-issues$/);
	await page.locator('a[href="/other/shares"]').first().click();
	const start = page.getByRole('button', { name: 'Share my screen', exact: true });
	await expect(start).toBeEnabled();
	await page.evaluate(() => {
		(window as any).__shareRig.pickerMode = 'immediate';
	});
	await start.click();
	await expect(page.locator(`[data-share-id="${otherShareId}"]`)).toContainText('Sharing');
	await page.evaluate(() => (window as any).__shareRig.completePicker());
	await expect.poll(() => page.evaluate(() => (window as any).__shareRig.stops)).toEqual([0, 1]);
	expect(requests.creates).toBe(0);
	expect(otherCreates).toBe(1);
	expect(await page.evaluate(() => (window as any).__workspaceReconnects)).not.toContain('other');
	await expect(page.locator(`[data-share-id="${otherShareId}"]`)).toContainText('Sharing');
});

test('a silent presenter reserves the offered audio sender for a later source with audio', async ({ page }) => {
	await stubOwnShare(page);
	await page.goto('/test/shares');
	await page.getByRole('button', { name: 'Share my screen', exact: true }).click();
	const tile = page.locator(`[data-share-id="${shareId}"]`);
	await expect(tile).toContainText('Sharing');
	await page.evaluate(
		(id) =>
			window.dispatchEvent(
				new CustomEvent('ws:share.signal', {
					detail: {
						share_id: id,
						from_peer: '00000000-0000-0000-0000-000000000021',
						negotiation_id: '00000000-0000-0000-0000-000000000023',
						data: { type: 'offer', sdp: 'viewer-offer' }
					}
				})
			),
		shareId
	);
	await expect.poll(async () => (await sentSignals(page, 'answer')).length).toBe(1);
	const audioSections = () =>
		page.evaluate(() =>
			(window as any).__shareRig.peers[0]
				.getTransceivers()
				.filter((transceiver: any) => transceiver.receiver.track.kind === 'audio')
				.map((transceiver: any) => ({
					mid: transceiver.mid,
					direction: transceiver.currentDirection,
					track: transceiver.sender.track?.kind ?? null
				}))
		);
	expect(await audioSections()).toEqual([{ mid: '1', direction: 'sendonly', track: null }]);
	await page.evaluate(() => {
		(window as any).__shareRig.captureAudio = true;
	});
	await tile.getByRole('button', { name: 'Change what you share', exact: true }).click();
	await expect.poll(audioSections).toEqual([{ mid: '1', direction: 'sendonly', track: 'audio' }]);
	expect((await sentSignals(page, 'answer')).length).toBe(1);
	expect(await page.evaluate(() => (window as any).__shareRig.peers.length)).toBe(1);
});

test('two viewer browser tabs for the same user keep independent negotiations', async ({ page, context }) => {
	const otherTab = await context.newPage();
	try {
		for (const tab of [page, otherTab]) {
			await stubCommon(tab, true);
			await installPeerRig(tab);
			await tab.goto('/test/shares');
			await expect.poll(async () => (await sentSignals(tab, 'offer')).length).toBe(1);
		}
		const firstOffer = (await sentSignals(page, 'offer'))[0];
		const secondOffer = (await sentSignals(otherTab, 'offer'))[0];
		expect(firstOffer.payload.negotiation_id).not.toBe(secondOffer.payload.negotiation_id);
		expect(firstOffer.payload.to).toBe(presenter.id);
		expect(secondOffer.payload.to).toBe(presenter.id);
		await otherTab.evaluate(() => (window as any).__shareRig.peers[0].setConnectionState('connected'));
		await page.evaluate(() =>
			window.dispatchEvent(new CustomEvent('ws:reconnected', { detail: { workspaceSlug: 'test' } }))
		);
		await expect.poll(async () => (await sentSignals(page, 'offer')).length).toBe(2);
		expect((await sentSignals(otherTab, 'offer')).length).toBe(1);
		expect(await otherTab.evaluate(() => (window as any).__shareRig.peers[0].closed)).toBe(false);
		await expect(otherTab.locator(`[data-share-id="${shareId}"]`).getByText('Live', { exact: true })).toBeVisible();
	} finally {
		await otherTab.close();
	}
});
