import { expect, test, type Page } from '@playwright/test';

/**
 * Voice rooms on the chat channels.
 *
 * The workspace socket is faked with routeWebSocket: it answers a join with
 * the targeted voice.sync and echoes rosters, so the full join → overlay →
 * mute → leave flow runs without a live backend. Microphone capture uses
 * Chromium's fake audio device (a real, audible tone — the speaking
 * detection exercises actual audio levels).
 */

const SELF = {
	id: '00000000-0000-0000-0000-000000000001',
	email: 'test@example.com',
	name: 'Test User',
	display_name: 'Test User',
	avatar_url: null
};
const ALICE = {
	id: '00000000-0000-0000-0000-000000000002',
	name: 'Alice Fontana',
	display_name: 'Alice',
	avatar_url: null
};
const SELF_PEER = '00000000-0000-0000-0000-000000000101';
const ALICE_PEER = '00000000-0000-0000-0000-000000000102';
const CHANNEL_ID = '00000000-0000-0000-0000-000000000011';

interface FakeParticipant {
	peer_id: string;
	user_id: string;
	user: typeof SELF | typeof ALICE;
	muted: boolean;
	deafened: boolean;
}

test.use({
	launchOptions: {
		args: ['--use-fake-ui-for-media-stream', '--use-fake-device-for-media-stream']
	}
});

async function stubChat(page: Page, initialRoom: FakeParticipant[] = []) {
	// The fake room the socket/REST answers from; tests read and mutate it.
	const room: FakeParticipant[] = [...initialRoom];
	let inRoom = false;

	await page.route('https://raw.githubusercontent.com/**/releases.json', (route) => route.fulfill({ json: [] }));
	await page.route('**/api/**', async (route) => {
		const path = new URL(route.request().url()).pathname;
		if (path === '/api/auth/me') return route.fulfill({ json: SELF });
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
					team_workflow_sort_overrides: {},
					issues_group_by: 'status'
				}
			});
		}
		const workspace = {
			id: '00000000-0000-0000-0000-000000000002',
			name: 'Test Workspace',
			slug: 'test',
			logo_url: null,
			owner_id: '00000000-0000-0000-0000-000000000001',
			current_user_role: 'member',
			created_at: '2026-01-01T00:00:00Z',
			updated_at: '2026-01-01T00:00:00Z'
		};
		if (path === '/api/workspaces') return route.fulfill({ json: [workspace] });
		if (path === '/api/workspaces/test') return route.fulfill({ json: workspace });
		if (path === '/api/workspaces/test/teams') return route.fulfill({ json: [] });
		if (path === '/api/workspaces/test/members') {
			return route.fulfill({
				json: [
					{ user_id: SELF.id, email: SELF.email, name: SELF.name, avatar_url: null, role: 'member', created_at: '2026-01-01T00:00:00Z' },
					{ user_id: ALICE.id, email: 'alice@example.com', name: ALICE.name, avatar_url: null, role: 'member', created_at: '2026-01-01T00:00:00Z' }
				]
			});
		}
		if (path === '/api/workspaces/test/projects' || path === '/api/workspaces/test/labels' || path === '/api/workspaces/test/views') {
			return route.fulfill({ json: [] });
		}
		if (path === '/api/notifications') return route.fulfill({ json: { unread_count: 0, notifications: [] } });
		if (path === '/api/workspaces/test/chat/channels') {
			return route.fulfill({
				json: {
					channels: [
						{
							id: CHANNEL_ID,
							workspace_id: workspace.id,
							type: 'public',
							name: 'general',
							topic: '',
							created_by: SELF.id,
							created_at: '2026-01-01T00:00:00Z',
							updated_at: '2026-01-01T00:00:00Z'
						}
					]
				}
			});
		}
		if (path === '/api/workspaces/test/chat/presence') return route.fulfill({ json: { online: [ALICE.id] } });
		if (path === '/api/workspaces/test/chat/voice/rooms') {
			const payload = room.length ? { rooms: [{ channel_id: CHANNEL_ID, participants: room }] } : { rooms: [] };
			return route.fulfill({ json: payload });
		}
		if (path.startsWith('/api/workspaces/test/chat/channels/') && path.endsWith('/messages')) {
			return route.fulfill({ json: { messages: [], has_more: false } });
		}
		return route.fulfill({ json: [] });
	});

	const sockets: { send: (data: string) => void }[] = [];
	const serverFrames: string[] = [];
	await page.routeWebSocket('**/api/workspaces/*/ws', (ws) => {
		sockets.push(ws);
		ws.onMessage((data) => {
			let frame: { type: string; payload: any };
			try {
				frame = JSON.parse(String(data));
			} catch {
				return;
			}
			serverFrames.push(frame.type);
			if (frame.type === 'voice.presence' && frame.payload.action === 'join') {
				inRoom = true;
				// Replace any earlier session of this user (single session per
				// user per room, mirroring the server).
				const withoutSelf = room.filter((p) => p.user_id !== SELF.id);
				room.splice(0, room.length, ...withoutSelf, {
					peer_id: SELF_PEER,
					user_id: SELF.id,
					user: SELF,
					muted: frame.payload.muted,
					deafened: frame.payload.deafened
				});
				ws.send(
					JSON.stringify({
						type: 'voice.sync',
						payload: { channel_id: frame.payload.channel_id, peer_id: SELF_PEER, participants: room }
					})
				);
				ws.send(
					JSON.stringify({
						type: 'voice.presence',
						payload: { channel_id: frame.payload.channel_id, participants: room }
					})
				);
				return;
			}
			if (frame.type === 'voice.presence' && frame.payload.action === 'leave') {
				const rest = room.filter((p) => p.peer_id !== SELF_PEER);
				room.splice(0, room.length, ...rest);
				ws.send(
					JSON.stringify({
						type: 'voice.presence',
						payload: { channel_id: frame.payload.channel_id, participants: room }
					})
				);
				return;
			}
			if (frame.type === 'voice.state') {
				const self = room.find((p) => p.peer_id === SELF_PEER);
				if (self) {
					self.muted = frame.payload.muted;
					self.deafened = frame.payload.deafened;
				}
				ws.send(
					JSON.stringify({
						type: 'voice.presence',
						payload: { channel_id: frame.payload.channel_id, participants: room }
					})
				);
			}
		});
	});

	// Outgoing ws:send frames the page dispatched (what the socket carries).
	await page.addInitScript(() => {
		(window as any).__voiceFrames = [];
		window.addEventListener('ws:send', (event) => {
			const detail = (event as CustomEvent).detail;
			if (String(detail.type).startsWith('voice.')) (window as any).__voiceFrames.push(detail);
		});
	});

	return {
		serverFrames,
		room,
		/** Push a server-side roster snapshot over the page's live socket. */
		pushRoster: (participants: FakeParticipant[]) => {
			room.splice(0, room.length, ...participants);
			sockets[sockets.length - 1].send(
				JSON.stringify({ type: 'voice.presence', payload: { channel_id: CHANNEL_ID, participants } })
			);
		}
	};
}

test('the rail and header show who is in the voice room', async ({ page }) => {
	await stubChat(page, [
		{ peer_id: ALICE_PEER, user_id: ALICE.id, user: ALICE, muted: false, deafened: false }
	]);
	await page.goto(`/test/chat/${CHANNEL_ID}`);

	// Rail badge: one user in voice on #general.
	await expect(page.locator('[data-testid="channel-voice-count"]')).toContainText('1');
	// Header join control reflects the live room.
	await expect(page.getByTestId('voice-join')).toContainText('1');
	// Not joined: no overlay yet.
	await expect(page.getByTestId('voice-room-bar')).toHaveCount(0);
});

test('joining enters the room, shows the overlay and gates the mic', async ({ page }) => {
	const stub = await stubChat(page);
	await page.goto(`/test/chat/${CHANNEL_ID}`);
	await expect(page.getByTestId('voice-join')).toBeVisible();

	await page.getByTestId('voice-join').click();

	// The fake server must have seen the join before answering.
	await expect.poll(() => stub.serverFrames).toContain('voice.presence');
	// The join rides the workspace socket…
	await expect
		.poll(() => page.evaluate(() => (window as any).__voiceFrames.map((f: { type: string }) => f.type)))
		.toContain('voice.presence');
	// …the fake server answers with voice.sync, and the overlay appears with
	// the local participant.
	const bar = page.getByTestId('voice-room-bar');
	await expect(bar).toBeVisible();
	await expect(bar.locator('[data-testid="voice-participant"]')).toHaveCount(1);
	await expect(bar).toContainText('Test User');

	// Chromium's fake mic produces a real tone: the speaking ring lights up
	// from the actual audio levels. The analyser loop runs at 120ms, but
	// headless first-boot (audio graph, device warm-up) can take a while.
	await expect(bar.locator('[data-testid="voice-speaking-dot"]').first()).toBeVisible({ timeout: 15000 });

	// Mute flips the roster flag through voice.state.
	await page.getByTestId('voice-mute').click();
	await expect
		.poll(() =>
			page.evaluate(
				() => (window as any).__voiceFrames.findLast((f: any) => f.type === 'voice.state')?.payload.muted
			)
		)
		.toBe(true);
	// The control reflects the state and the roster snapshot flags the row.
	await expect(page.getByTestId('voice-mute')).toContainText('Unmute');
	await expect(bar.locator('[data-testid="voice-participant-muted-icon"]').first()).toBeVisible();

	// Leave announces and closes the overlay.
	await page.getByTestId('voice-leave').click();
	await expect(page.getByTestId('voice-room-bar')).toHaveCount(0);
	await expect
		.poll(() =>
			page.evaluate(
				() => (window as any).__voiceFrames.findLast((f: any) => f.type === 'voice.presence')?.payload.action
			)
		)
		.toBe('leave');
});

test('a roster snapshot from another participant updates the overlay', async ({ page }) => {
	const stub = await stubChat(page);
	await page.goto(`/test/chat/${CHANNEL_ID}`);
	await page.getByTestId('voice-join').click();
	await expect(page.getByTestId('voice-room-bar')).toBeVisible();

	// Alice joins the room elsewhere: the server pushes the merged roster.
	stub.pushRoster([
		{
			peer_id: SELF_PEER,
			user_id: SELF.id,
			user: SELF,
			muted: false,
			deafened: false
		},
		{
			peer_id: ALICE_PEER,
			user_id: ALICE.id,
			user: ALICE,
			muted: true,
			deafened: false
		}
	]);

	const bar = page.getByTestId('voice-room-bar');
	await expect(bar.locator('[data-testid="voice-participant"]')).toHaveCount(2);
	await expect(bar).toContainText('Alice');
	// Alice's muted flag renders the mute icon on her row.
	await expect(bar.locator('[data-testid="voice-participant-muted-icon"]').first()).toBeVisible();
});

async function joinMovableCall(page: Page) {
	const stub = await stubChat(page);
	await page.addInitScript(() => {
		const original = navigator.mediaDevices.getUserMedia.bind(navigator.mediaDevices);
		(window as any).__micTracks = [];
		navigator.mediaDevices.getUserMedia = async (constraints) => {
			const stream = await original(constraints);
			(window as any).__micTracks.push(...stream.getAudioTracks());
			return stream;
		};
	});
	await page.goto(`/test/chat/${CHANNEL_ID}`);
	await page.getByTestId('voice-join').click();
	await expect(page.getByTestId('voice-mute')).toBeEnabled();
	await expect(page.getByTestId('voice-participant')).toHaveCount(1);
	return stub;
}

async function expectPanelInViewport(page: Page) {
	await expect
		.poll(async () =>
			page.getByTestId('voice-room-bar').evaluate((node) => {
				const rect = node.getBoundingClientRect();
				return rect.left >= 0 && rect.top >= 0 && rect.right <= innerWidth && rect.bottom <= innerHeight;
			})
		)
		.toBe(true);
}

test('dragging preserves the microphone, roster and controls, including across navigation', async ({ page }) => {
	await page.setViewportSize({ width: 1100, height: 800 });
	await joinMovableCall(page);
	const panel = page.getByTestId('voice-room-bar');
	const handle = page.getByTestId('voice-move');
	const before = (await panel.boundingBox())!;
	const grip = (await handle.boundingBox())!;
	await page.mouse.move(grip.x + 10, grip.y + 10);
	await page.mouse.down();
	await page.mouse.move(grip.x + 410, grip.y - 290, { steps: 10 });
	await page.mouse.up();
	await expect.poll(async () => (await panel.boundingBox())!.x).toBe(before.x + 400);
	await expect.poll(async () => (await panel.boundingBox())!.y).toBe(before.y - 300);
	await expectPanelInViewport(page);
	await expect(panel).toContainText('#general');
	await expect(page.getByTestId('voice-mute')).toContainText('Mute');
	await expect(page.getByTestId('voice-deafen')).toContainText('Deafen');
	await expect(page.getByRole('button', { name: 'Voice settings', exact: true })).toBeVisible();
	await expect(page.getByTestId('voice-mode-vad')).toHaveCount(0);
	await expect(page.getByTestId('voice-speaking-dot')).toBeVisible({ timeout: 15000 });
	await expect
		.poll(() => page.evaluate(() => (window as any).__micTracks.map((t: MediaStreamTrack) => t.readyState)))
		.toEqual(['live']);
	await expect
		.poll(() =>
			page.evaluate(
				() =>
					(window as any).__voiceFrames.filter((f: any) => f.type === 'voice.presence' && f.payload.action === 'join')
						.length
			)
		)
		.toBe(1);

	const moved = (await panel.boundingBox())!;
	// Client-side navigation keeps the same workspace call panel mounted.
	await page.locator('a[href="/test/my-issues"]').first().click();
	await expect(page).toHaveURL(/\/test\/my-issues/);
	await expect.poll(async () => (await panel.boundingBox())!.x).toBe(moved.x);
	await expect.poll(async () => (await panel.boundingBox())!.y).toBe(moved.y);
	await expect(page.getByTestId('voice-participant')).toHaveCount(1);

	// Releasing a captured drag over another control must not click it.
	const start = (await handle.boundingBox())!;
	await page.mouse.move(start.x + 8, start.y + 8);
	await page.mouse.down();
	await page.mouse.move(1099, 0, { steps: 10 });
	const leave = (await page.getByTestId('voice-leave').boundingBox())!;
	await page.mouse.move(leave.x + 5, leave.y + 5);
	await page.mouse.up();
	await expect(panel).toBeVisible();
	await expectPanelInViewport(page);

	await page.getByRole('button', { name: 'Voice settings', exact: true }).click();
	await expect(page.getByTestId('voice-mode-vad')).toBeVisible();
	await expectPanelInViewport(page);
	await page.getByTestId('voice-mute').click();
	await expect(page.getByTestId('voice-mute')).toContainText('Unmute');
	await page.getByTestId('voice-deafen').click();
	await expect(page.getByTestId('voice-deafen')).toContainText('Undeafen');
	await page.getByTestId('voice-leave').click();
	await expect(panel).toHaveCount(0);
	await expect
		.poll(() => page.evaluate(() => (window as any).__micTracks.map((t: MediaStreamTrack) => t.readyState)))
		.toEqual(['ended']);
});

test('keyboard movement, content growth and resizing keep the panel reachable', async ({ page }) => {
	await page.setViewportSize({ width: 1000, height: 800 });
	await joinMovableCall(page);
	const panel = page.getByTestId('voice-room-bar');
	const handle = page.getByTestId('voice-move');
	await handle.focus();
	const before = (await panel.boundingBox())!;
	await page.keyboard.press('ArrowRight');
	await page.keyboard.press('Shift+ArrowUp');
	await expect.poll(async () => (await panel.boundingBox())!.x).toBe(before.x + 12);
	await expect.poll(async () => (await panel.boundingBox())!.y).toBe(before.y - 48);
	await expect(handle).toBeFocused();
	for (let i = 0; i < 30; i++) await page.keyboard.press('Shift+ArrowRight');
	await expectPanelInViewport(page);
	await page.getByRole('button', { name: 'Voice settings', exact: true }).click();
	await expectPanelInViewport(page);
	await page.setViewportSize({ width: 280, height: 240 });
	await expectPanelInViewport(page);
	// At very small heights, the panel scrolls so both settings and controls remain usable.
	await page.getByTestId('voice-mode-ptt').click();
	await page.getByTestId('voice-mute').click();
	await expect(page.getByTestId('voice-mute')).toContainText('Unmute');
	await page.setViewportSize({ width: 740, height: 360 });
	await expectPanelInViewport(page);
	await handle.focus();
	await page.keyboard.press('Home');
	await expect.poll(async () => (await panel.boundingBox())!.x).toBe(16);
	await page.getByRole('button', { name: 'Voice settings', exact: true }).click();
	await handle.focus();
	await page.keyboard.press('Home');
	await expect
		.poll(async () => {
			const box = (await panel.boundingBox())!;
			return box.y + box.height;
		})
		.toBe(344);
	await page.getByTestId('voice-leave').click();
});

test.describe('touch panel movement', () => {
	test.use({ hasTouch: true, viewport: { width: 390, height: 700 } });

	test('touch capture moves the panel and releases cleanly', async ({ page, context }) => {
		await joinMovableCall(page);
		const panel = page.getByTestId('voice-room-bar');
		const handle = page.getByTestId('voice-move');
		const grip = (await handle.boundingBox())!;
		const before = (await panel.boundingBox())!;
		const cdp = await context.newCDPSession(page);
		await cdp.send('Input.dispatchTouchEvent', {
			type: 'touchStart',
			touchPoints: [{ x: grip.x + 10, y: grip.y + 10 }]
		});
		await cdp.send('Input.dispatchTouchEvent', {
			type: 'touchMove',
			touchPoints: [{ x: grip.x + 50, y: grip.y - 190 }]
		});
		await cdp.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] });
		await expect.poll(async () => (await panel.boundingBox())!.x).toBe(before.x + 40);
		await expect.poll(async () => (await panel.boundingBox())!.y).toBe(before.y - 200);
		await expectPanelInViewport(page);
		await expect(page.getByTestId('voice-participant')).toHaveCount(1);
		await page.getByTestId('voice-mute').tap();
		await expect(page.getByTestId('voice-mute')).toContainText('Unmute');
		await page.setViewportSize({ width: 700, height: 390 });
		await expectPanelInViewport(page);
		await page.getByTestId('voice-leave').tap();
		await expect(panel).toHaveCount(0);
		await page.getByTestId('voice-join').tap();
		await expect(panel).toBeVisible();
		await expect.poll(async () => (await panel.boundingBox())!.x).toBe(16);
		await cdp.detach();
	});
});
