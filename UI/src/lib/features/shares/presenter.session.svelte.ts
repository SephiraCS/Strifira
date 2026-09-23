import { browser } from '$app/environment';
import { createShare, endShare } from '$lib/api/shares';
import type { ScreenShare } from '$lib/types/share';
import { appToast } from '$lib/features/toast/toast';
import { m } from '$lib/paraglide/messages.js';
import {
	announceSharePresence,
	onSharePresence,
	onShareSignal,
	onWebSocketReconnected,
	sendShareSignal,
	ICE_SERVERS,
	type SignalData
} from './signaling';
import { sharesState } from './shares.state.svelte';

/**
 * The presenter side of a screen share, hoisted out of the route tree.
 *
 * The captured MediaStream and one RTCPeerConnection per viewer live here,
 * at module scope, so in-app navigation (wall → issue → settings → wall)
 * never ends the share: route components unmount freely, the session does
 * not. Only an explicit Stop, the browser's own capture stop, or the share
 * ending server-side tears it down. A reload or tab close still loses the
 * source — the native beforeunload prompt covers that.
 *
 * Signaling rides the workspace WebSocket like before; on a reconnect the
 * session re-announces presence, and established peer connections keep
 * flowing media throughout (media is P2P and never transits the server).
 */

type QualityPreference = 'sharp' | 'smooth';

const CAPTURE_OPTIONS = {
	audio: true,
	video: { frameRate: 60 },
	selfBrowserSurface: 'exclude',
	surfaceSwitching: 'include',
	systemAudio: 'include'
} as unknown as DisplayMediaStreamOptions;

class PresenterSession {
	share = $state<ScreenShare | null>(null);
	stream = $state<MediaStream | null>(null);
	viewerCount = $state(0);
	live = $state(false);
	switching = $state(false);
	starting = $state(false);
	qualityPreference = $state<QualityPreference>('sharp');
	qualityBusy = $state(false);

	// Plain (non-reactive) bookkeeping — mirrors the pre-store ShareTile.
	private stopping = false;
	private ended = false;
	private endAfterSwitch = false;
	private slug = '';
	private peers = new Map<string, RTCPeerConnection>();
	private pendingCandidates = new Map<string, RTCIceCandidateInit[]>();
	private wired = false;

	constructor() {
		if (browser) this.wire();
	}

	/** True while this tab is the live source of a share. */
	get active(): boolean {
		return this.share !== null && this.live && !this.ended;
	}

	/**
	 * Capture-first start, inside the click's user-activation window
	 * (Chrome denies getDisplayMedia once the gesture has expired behind
	 * the share-creation round-trip). Only after a surface is chosen is
	 * the share row created.
	 */
	async start(workspaceSlug: string): Promise<void> {
		if (this.starting || this.active) return;
		this.starting = true;
		let stream: MediaStream;
		try {
			stream = await navigator.mediaDevices.getDisplayMedia(CAPTURE_OPTIONS);
		} catch (err) {
			appToast.apiError(err, m['shares.capture_failed']());
			return;
		} finally {
			this.starting = false;
		}
		try {
			const joined = await createShare(workspaceSlug, {});
			this.slug = workspaceSlug;
			this.ended = false;
			this.stopping = false;
			this.share = joined.share;
			this.stream = stream;
			this.attachStream(stream);
			// The browser ends the captured track itself when the user hits
			// the native "Stop sharing" bar — treat that exactly like
			// pressing our Stop button.
			stream.getVideoTracks()[0]?.addEventListener('ended', () => this.handleCaptureEnded());
			announceSharePresence(joined.share.id, 'presenter', 'join');
			this.live = true;
			void sharesState.reload();
		} catch (err) {
			stream.getTracks().forEach((t) => t.stop());
			appToast.apiError(err, m['shares.start_failed']());
		}
	}

	/** End the share from this tab: server-side first, then local teardown. */
	async stop(): Promise<void> {
		if (this.stopping || !this.share) return;
		this.stopping = true;
		const share = this.share;
		try {
			await endShare(this.slug, share.id);
		} catch {
			// Server-side end failed; lazy GC closes the row.
		}
		this.teardown();
	}

	/** Re-pick the captured screen/window/tab while staying live. */
	async switchSurface(): Promise<void> {
		if (!this.stream || this.switching) return;
		this.switching = true;
		this.endAfterSwitch = false;
		const sourceStream = this.stream;
		try {
			const next = await navigator.mediaDevices.getDisplayMedia(CAPTURE_OPTIONS);
			const nextVideo = next.getVideoTracks()[0];
			if (nextVideo) {
				nextVideo.contentHint = this.qualityPreference === 'sharp' ? 'detail' : 'motion';
				nextVideo.addEventListener('ended', () => this.handleCaptureEnded());
			}
			// Swap the track on every live peer; renegotiation is not needed
			// for a same-kind replaceTrack. replaceTrack can take a long time
			// to settle (observed tens of seconds on a Firefox peer) — the
			// switch must not block on it.
			const senders = [...this.peers.values()].flatMap((pc) => pc.getSenders());
			for (const sender of senders) {
				if (sender.track?.kind === 'video' && nextVideo) void sender.replaceTrack(nextVideo).catch(() => {});
				if (sender.track?.kind === 'audio') {
					const nextAudio = next.getAudioTracks()[0];
					if (sender.track || nextAudio) void sender.replaceTrack(nextAudio ?? null).catch(() => {});
				}
			}
			// Retire the previous capture; the new one feeds any mounted
			// preview (it rebinds through the reactive stream state).
			sourceStream.getTracks().forEach((t) => t.stop());
			this.stream = next;
		} catch {
			// Picker cancelled or capture denied — nothing is being shared
			// anymore; end the share cleanly.
			await this.stop();
		} finally {
			this.switching = false;
			if (this.endAfterSwitch && !this.stopping && !this.ended) {
				this.endAfterSwitch = false;
				void this.stop();
			}
		}
	}

	async setQuality(pref: QualityPreference): Promise<void> {
		if (pref === this.qualityPreference || this.qualityBusy) return;
		this.qualityBusy = true;
		this.qualityPreference = pref;
		try {
			if (this.stream) this.applyContentHints(this.stream);
			for (const pc of this.peers.values()) await this.applySenderQuality(pc);
		} finally {
			this.qualityBusy = false;
		}
	}

	// ---- internals ----

	private wire(): void {
		if (this.wired) return;
		this.wired = true;

		window.addEventListener('beforeunload', (e) => {
			if (this.active) e.preventDefault();
		});

		onShareSignal((event) => {
			if (!this.share || this.ended) return;
			if (event.share_id !== this.share.id) return;
			if (event.from) void this.handleViewerSignal(event.from, event.data);
		});

		onSharePresence((event) => {
			if (!this.share || event.share_id !== this.share.id) return;
			this.viewerCount = event.viewers;
		});

		onWebSocketReconnected(() => {
			if (!this.share || this.ended) return;
			// Presence is per-connection: re-announce, then reconcile with
			// the server's view in case the share was ended (GC, admin)
			// while the socket was down.
			announceSharePresence(this.share.id, 'presenter', 'join');
			void (async () => {
				await sharesState.load(this.slug);
				const stillActive = sharesState.active.some((s) => s.id === this.share?.id);
				if (!stillActive) this.teardown();
			})();
		});

		// The server ended the share (our Stop, an admin force-end, or the
		// no-source GC): stop capturing immediately.
		window.addEventListener('ws:share.ended', ((e: CustomEvent<{ id?: string }>) => {
			if (!this.share || e.detail?.id !== this.share.id) return;
			this.teardown();
		}) as EventListener);
	}

	private attachStream(stream: MediaStream): void {
		this.applyContentHints(stream);
	}

	private applyContentHints(stream: MediaStream): void {
		const video = stream.getVideoTracks()[0];
		if (video) video.contentHint = this.qualityPreference === 'sharp' ? 'detail' : 'motion';
	}

	private async applySenderQuality(pc: RTCPeerConnection): Promise<void> {
		for (const sender of pc.getSenders()) {
			if (sender.track?.kind !== 'video') continue;
			try {
				const params = sender.getParameters();
				params.degradationPreference =
					this.qualityPreference === 'sharp' ? 'maintain-resolution' : 'balanced';
				await sender.setParameters(params);
			} catch {
				// Browser without degradationPreference support: contentHint
				// already carries the intent.
			}
		}
	}

	private handleCaptureEnded(): void {
		// Track endings we caused ourselves (Stop, or swapping the surface)
		// already run their own teardown. `stopping` is set synchronously so
		// its own stopped track cannot re-enter.
		if (this.stopping || this.ended || !this.live) return;
		if (this.switching) {
			this.endAfterSwitch = true;
			return;
		}
		void this.stop();
	}

	private queueCandidate(key: string, candidate: RTCIceCandidateInit | null, pc: RTCPeerConnection): void {
		if (candidate === null) return;
		if (pc.remoteDescription) {
			void pc.addIceCandidate(candidate).catch(() => {});
		} else {
			const queue = this.pendingCandidates.get(key) ?? [];
			queue.push(candidate);
			this.pendingCandidates.set(key, queue);
		}
	}

	private async flushCandidates(key: string, pc: RTCPeerConnection): Promise<void> {
		const queue = this.pendingCandidates.get(key) ?? [];
		this.pendingCandidates.delete(key);
		for (const candidate of queue) {
			await pc.addIceCandidate(candidate).catch(() => {});
		}
	}

	private createPresenterPeer(viewerId: string): RTCPeerConnection {
		const shareId = this.share!.id;
		const sourceStream = this.stream;
		const pc = new RTCPeerConnection({ iceServers: ICE_SERVERS });
		this.peers.set(viewerId, pc);
		if (sourceStream) {
			this.applyContentHints(sourceStream);
			for (const track of sourceStream.getTracks()) {
				pc.addTrack(track, sourceStream);
			}
			void this.applySenderQuality(pc);
		}
		pc.onicecandidate = (e) => {
			if (e.candidate) sendShareSignal(shareId, viewerId, { type: 'candidate', candidate: e.candidate.toJSON() });
		};
		pc.onconnectionstatechange = () => {
			if (pc.connectionState === 'failed' || pc.connectionState === 'closed') {
				if (this.peers.get(viewerId) === pc) {
					this.peers.delete(viewerId);
					this.pendingCandidates.delete(viewerId);
					pc.close();
				}
			}
		};
		return pc;
	}

	private async handleViewerSignal(viewerId: string, data: SignalData): Promise<void> {
		if (!this.share) return;
		const shareId = this.share.id;
		if (data.type === 'offer') {
			// A viewer (re)negotiating: replace any dead connection for it.
			// A still-connected peer ignores the stale duplicate offer; a
			// reconnecting viewer (or one recovering a failed connection
			// with an ICE restart) lands on a fresh peer.
			const existing = this.peers.get(viewerId);
			if (existing) {
				if (existing.connectionState === 'connected' || existing.signalingState === 'have-remote-offer') {
					return; // stale duplicate offer
				}
				this.peers.delete(viewerId);
				existing.close();
			}
			const pc = this.createPresenterPeer(viewerId);
			try {
				await pc.setRemoteDescription({ type: 'offer', sdp: data.sdp });
				const answer = await pc.createAnswer();
				await pc.setLocalDescription(answer);
				sendShareSignal(shareId, viewerId, { type: 'answer', sdp: pc.localDescription!.sdp! });
				await this.flushCandidates(viewerId, pc);
			} catch {
				this.peers.delete(viewerId);
				this.pendingCandidates.delete(viewerId);
				pc.close();
			}
		} else if (data.type === 'candidate') {
			const pc = this.peers.get(viewerId);
			if (pc) this.queueCandidate(viewerId, data.candidate, pc);
		}
	}

	private teardown(): void {
		if (this.ended) return;
		this.ended = true;
		if (this.share) announceSharePresence(this.share.id, 'presenter', 'leave');
		for (const pc of this.peers.values()) pc.close();
		this.peers.clear();
		this.pendingCandidates.clear();
		// Release the browser's capture indicator for any still-live source
		// tracks (pre-captured or switched).
		this.stream?.getTracks().forEach((t) => t.stop());
		this.share = null;
		this.stream = null;
		this.live = false;
		this.viewerCount = 0;
		this.stopping = false;
		void sharesState.reload();
	}
}

export const presenterSession = new PresenterSession();
