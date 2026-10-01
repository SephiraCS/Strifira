import { browser } from '$app/environment';
import { createShare, endShare, getShare } from '$lib/api/shares';
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
	private currentWorkspaceSlug = '';
	private presenterToken = '';
	private pendingCapture: MediaStream | null = null;
	private generation = 0;
	private peers = new Map<string, RTCPeerConnection>();
	private peerNegotiations = new Map<string, string>();
	private pendingCandidates = new Map<string, RTCIceCandidateInit[]>();
	private wired = false;

	constructor() {
		if (browser) this.wire();
	}

	/** True while this tab is the live source of a share. */
	get active(): boolean {
		return this.share !== null && this.live && !this.ended;
	}

	/** Route ownership changes even while the workspace socket is unavailable. */
	setWorkspace(workspaceSlug: string): void {
		if (workspaceSlug === this.currentWorkspaceSlug) return;
		this.currentWorkspaceSlug = workspaceSlug;
		this.generation++;
		this.starting = false;
		this.pendingCapture?.getTracks().forEach((track) => track.stop());
		this.pendingCapture = null;
		if (this.share && workspaceSlug !== this.slug) {
			void this.stop();
			// Release capture immediately; the old workspace's end request
			// must not block starting a share in the newly selected workspace.
			this.teardown();
		}
	}

	/**
	 * Capture-first start, inside the click's user-activation window
	 * (Chrome denies getDisplayMedia once the gesture has expired behind
	 * the share-creation round-trip). Only after a surface is chosen is
	 * the share row created.
	 */
	async start(workspaceSlug: string): Promise<void> {
		if (this.starting || this.share || this.currentWorkspaceSlug !== workspaceSlug) return;
		const generation = ++this.generation;
		const current = () => generation === this.generation && this.currentWorkspaceSlug === workspaceSlug;
		this.starting = true;
		let stream: MediaStream | null = null;
		try {
			try {
				stream = await navigator.mediaDevices.getDisplayMedia(CAPTURE_OPTIONS);
			} catch (err) {
				if (current()) appToast.apiError(err, m['shares.capture_failed']());
				return;
			}
			if (!current()) {
				stream.getTracks().forEach((track) => track.stop());
				return;
			}
			this.pendingCapture = stream;
			const joined = await createShare(workspaceSlug, {});
			if (!current()) {
				stream.getTracks().forEach((track) => track.stop());
				await endShare(workspaceSlug, joined.share.id, joined.presenter_token);
				return;
			}
			this.slug = workspaceSlug;
			this.presenterToken = joined.presenter_token;
			this.ended = false;
			this.stopping = false;
			this.share = joined.share;
			this.stream = stream;
			this.attachStream(stream);
			const captured = stream;
			stream.getVideoTracks()[0]?.addEventListener('ended', () => this.handleCaptureEnded(captured));
			announceSharePresence(this.slug, joined.share.id, 'presenter', 'join', this.presenterToken);
			this.live = true;
			void sharesState.reload();
		} catch (err) {
			stream?.getTracks().forEach((t) => t.stop());
			if (current()) appToast.apiError(err, m['shares.start_failed']());
		} finally {
			if (current()) {
				this.pendingCapture = null;
				this.starting = false;
			}
		}
	}

	/** End the share from this tab: server-side first, then local teardown. */
	async stop(): Promise<void> {
		if (this.stopping || !this.share) return;
		this.stopping = true;
		const share = this.share;
		const generation = this.generation;
		try {
			await endShare(this.slug, share.id, this.presenterToken);
		} catch {
			// Server-side end failed; lazy GC closes the row.
		}
		if (generation === this.generation) this.teardown();
	}

	/** Re-pick the captured screen/window/tab while staying live. */
	async switchSurface(): Promise<void> {
		if (!this.stream || this.switching) return;
		this.switching = true;
		this.endAfterSwitch = false;
		const sourceStream = this.stream;
		const generation = this.generation;
		try {
			const next = await navigator.mediaDevices.getDisplayMedia(CAPTURE_OPTIONS);
			if (generation !== this.generation || this.stopping || this.ended || this.stream !== sourceStream) {
				next.getTracks().forEach((track) => track.stop());
				return;
			}
			const nextVideo = next.getVideoTracks()[0];
			if (nextVideo) {
				nextVideo.contentHint = this.qualityPreference === 'sharp' ? 'detail' : 'motion';
				nextVideo.addEventListener('ended', () => this.handleCaptureEnded(next));
			}
			// Swap the track on every live peer; renegotiation is not needed
			// for a same-kind replaceTrack. replaceTrack can take a long time
			// to settle (observed tens of seconds on a Firefox peer) — the
			// switch must not block on it.
			for (const pc of this.peers.values()) {
				for (const transceiver of pc.getTransceivers()) {
					const kind = transceiver.sender.track?.kind ?? transceiver.receiver.track.kind;
					const track = kind === 'video' ? nextVideo : next.getAudioTracks()[0];
					void transceiver.sender.replaceTrack(track ?? null).catch(() => {});
				}
			}
			// Retire the previous capture; the new one feeds any mounted
			// preview (it rebinds through the reactive stream state).
			this.stream = next;
			this.endAfterSwitch = false;
			sourceStream.getTracks().forEach((t) => t.stop());
		} catch {
			// Cancelling the picker leaves the existing capture untouched.
			// If that source ended while the picker was open, finally stops it.
		} finally {
			if (generation === this.generation) {
				this.switching = false;
				if (this.endAfterSwitch && !this.stopping && !this.ended) {
					this.endAfterSwitch = false;
					void this.stop();
				}
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
			if (event.from_peer && event.negotiation_id)
				void this.handleViewerSignal(event.from_peer, event.negotiation_id, event.data);
		});

		onSharePresence((event) => {
			if (!this.share || event.share_id !== this.share.id) return;
			this.viewerCount = event.viewers;
			for (const [peerId, pc] of this.peers) {
				if (!event.viewer_peers.includes(peerId)) {
					this.peers.delete(peerId);
					this.peerNegotiations.delete(peerId);
					this.pendingCandidates.delete(peerId);
					pc.close();
				}
			}
		});

		onWebSocketReconnected((workspaceSlug) => {
			if (!this.share || this.ended || workspaceSlug !== this.currentWorkspaceSlug || workspaceSlug !== this.slug)
				return;
			const shareId = this.share.id;
			const generation = this.generation;
			announceSharePresence(this.slug, shareId, 'presenter', 'join', this.presenterToken);
			void getShare(this.slug, shareId)
				.then((share) => {
					if (generation === this.generation && share.status !== 'active') this.teardown();
				})
				.catch(() => {
					// A transient REST failure must not release a working capture.
				});
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
				params.degradationPreference = this.qualityPreference === 'sharp' ? 'maintain-resolution' : 'balanced';
				await sender.setParameters(params);
			} catch {
				// Browser without degradationPreference support: contentHint
				// already carries the intent.
			}
		}
	}

	private handleCaptureEnded(source: MediaStream): void {
		if (source !== this.stream) return;
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

	private createPresenterPeer(viewerId: string, negotiationId: string): RTCPeerConnection {
		const shareId = this.share!.id;
		const sourceStream = this.stream;
		const pc = new RTCPeerConnection({ iceServers: ICE_SERVERS });
		this.peers.set(viewerId, pc);
		this.peerNegotiations.set(viewerId, negotiationId);
		if (sourceStream) {
			this.applyContentHints(sourceStream);
			for (const track of sourceStream.getTracks()) {
				pc.addTrack(track, sourceStream);
			}
			void this.applySenderQuality(pc);
		}
		pc.onicecandidate = (e) => {
			if (e.candidate)
				sendShareSignal(this.slug, shareId, { peerId: viewerId }, negotiationId, {
					type: 'candidate',
					candidate: e.candidate.toJSON()
				});
		};
		pc.onconnectionstatechange = () => {
			if (pc.connectionState === 'failed' || pc.connectionState === 'closed') {
				if (this.peers.get(viewerId) === pc) {
					this.peers.delete(viewerId);
					this.peerNegotiations.delete(viewerId);
					this.pendingCandidates.delete(viewerId);
					pc.close();
				}
			}
		};
		return pc;
	}

	private async handleViewerSignal(viewerId: string, negotiationId: string, data: SignalData): Promise<void> {
		if (!this.share || this.stopping || this.ended) return;
		const shareId = this.share.id;
		if (data.type === 'offer') {
			const existing = this.peers.get(viewerId);
			if (existing && this.peerNegotiations.get(viewerId) === negotiationId) return;
			this.pendingCandidates.delete(viewerId);
			if (existing) {
				this.peers.delete(viewerId);
				existing.close();
			}
			const pc = this.createPresenterPeer(viewerId, negotiationId);
			const current = () =>
				this.peers.get(viewerId) === pc && this.share?.id === shareId && !this.ended && !this.stopping;
			try {
				await pc.setRemoteDescription({ type: 'offer', sdp: data.sdp });
				if (!current()) return;
				// Configure the audio section actually associated with the
				// viewer's offer. Reserve sending even for a silent capture so
				// a later surface switch can add audio without renegotiation.
				for (const transceiver of pc.getTransceivers()) {
					if (transceiver.mid === null || transceiver.receiver.track.kind !== 'audio') continue;
					transceiver.direction = 'sendonly';
					const audio = this.stream?.getAudioTracks()[0] ?? null;
					if (transceiver.sender.track !== audio) await transceiver.sender.replaceTrack(audio);
					if (!current()) return;
				}
				const answer = await pc.createAnswer();
				if (!current()) return;
				await pc.setLocalDescription(answer);
				if (!current()) return;
				sendShareSignal(this.slug, shareId, { peerId: viewerId }, negotiationId, {
					type: 'answer',
					sdp: pc.localDescription!.sdp!
				});
				await this.flushCandidates(viewerId, pc);
			} catch {
				if (this.peers.get(viewerId) === pc) {
					this.peers.delete(viewerId);
					this.peerNegotiations.delete(viewerId);
					this.pendingCandidates.delete(viewerId);
				}
				pc.close();
			}
		} else if (data.type === 'candidate' && this.peerNegotiations.get(viewerId) === negotiationId) {
			const pc = this.peers.get(viewerId);
			if (pc) this.queueCandidate(viewerId, data.candidate, pc);
		}
	}

	private teardown(): void {
		if (this.ended) return;
		this.ended = true;
		this.generation++;
		if (this.share) announceSharePresence(this.slug, this.share.id, 'presenter', 'leave', this.presenterToken);
		for (const pc of this.peers.values()) pc.close();
		this.peers.clear();
		this.peerNegotiations.clear();
		this.pendingCandidates.clear();
		// Release the browser's capture indicator for any still-live source
		// tracks (pre-captured or switched).
		this.stream?.getTracks().forEach((t) => t.stop());
		this.share = null;
		this.stream = null;
		this.presenterToken = '';
		this.live = false;
		this.switching = false;
		this.endAfterSwitch = false;
		this.viewerCount = 0;
		this.stopping = false;
		void sharesState.reload();
	}
}

export const presenterSession = new PresenterSession();
