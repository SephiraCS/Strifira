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
	// from the actual audio levels.
	await expect(bar.locator('[data-testid="voice-speaking-dot"]').first()).toBeVisible({ timeout: 5000 });

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
