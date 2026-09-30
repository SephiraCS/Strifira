<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import {
		announceSharePresence,
		onSharePresence,
		onShareSignal,
		onWebSocketDisconnected,
		onWebSocketReconnected,
		sendShareSignal,
		ICE_SERVERS,
		type SignalData
	} from './signaling';
	import { presenterSession } from './presenter.session.svelte';
	import { Button } from '$lib/components/ui/button';
	import Maximize2 from '@lucide/svelte/icons/maximize-2';
	import MonitorUp from '@lucide/svelte/icons/monitor-up';
	import RefreshCw from '@lucide/svelte/icons/refresh-cw';
	import Loader2 from '@lucide/svelte/icons/loader-circle';
	import SignalHigh from '@lucide/svelte/icons/signal-high';
	import SignalMedium from '@lucide/svelte/icons/signal-medium';
	import SignalLow from '@lucide/svelte/icons/signal-low';
	import Volume2 from '@lucide/svelte/icons/volume-2';
	import VolumeX from '@lucide/svelte/icons/volume-x';
	import { m } from '$lib/paraglide/messages.js';

	let {
		workspaceSlug,
		shareId,
		mode,
		presenterName,
		presenterUserId
	}: {
		workspaceSlug: string;
		shareId: string;
		/**
		 * 'present' previews this tab's own persistent share (the engine
		 * lives in presenterSession and survives navigation); 'view'
		 * receives a remote share with this tile's own peer connection.
		 */
		mode: 'present' | 'view';
		presenterName: string;
		/** The share's started_by user id — viewers address offers to them. */
		presenterUserId?: string;
	} = $props();

	let videoEl = $state<HTMLVideoElement | null>(null);
	// Initial-value capture is intentional: `mode` never changes for a
	// mounted tile.
	// svelte-ignore state_referenced_locally
	const initiallyConnecting = mode === 'view';
	let live = $state(false);
	let connecting = $state(initiallyConnecting);
	let reconnecting = $state(false);
	let viewerCount = $state(0);
	let presentEnded = $state(false);
	let streamEnded = $state(false);
	// Browsers block unmuted autoplay: viewer tiles start muted with an
	// explicit unmute toggle.
	let audioMuted = $state(true);
	// Viewer-side: the presenter's *signaling* is down (their socket
	// dropped). Media may still flow — the share gets a grace window
	// before it is treated as source-less.
	let presenterOffline = $state(false);

	// ---- Peer plumbing (P2P WebRTC over the workspace WS) ----

	// Viewer side: this tab's single connection to the presenter.
	let viewerPc = $state<RTCPeerConnection | null>(null);
	let viewerRemoteSettled = false;
	let negotiationId = '';
	let presenterPeerId = '';
	let knownPresenterPeer = '';
	// Why a viewer tile stopped: 'disconnected' (presenter gone — no source
	// to retry against) vs 'error' (negotiation/ICE failed — manual retry).
	let endReason = $state<'disconnected' | 'error' | null>(null);
	let disposed = false;
	const cleanups: Array<() => void> = [];

	// ICE candidates that arrived before the remote description was set.
	const pendingCandidates = new Map<string, RTCIceCandidateInit[]>();

	function queueCandidate(key: string, candidate: RTCIceCandidateInit | null, pc: RTCPeerConnection) {
		if (candidate === null) return;
		if (pc.remoteDescription) {
			void pc.addIceCandidate(candidate).catch(() => {});
		} else {
			const queue = pendingCandidates.get(key) ?? [];
			queue.push(candidate);
			pendingCandidates.set(key, queue);
		}
	}

	async function flushCandidates(key: string, pc: RTCPeerConnection) {
		const queue = pendingCandidates.get(key) ?? [];
		pendingCandidates.delete(key);
		for (const candidate of queue) {
			await pc.addIceCandidate(candidate).catch(() => {});
		}
	}

	function isPresenting(): boolean {
		return mode === 'present';
	}

	// Presenter preview: the persistent session owns the stream; the tile
	// only displays (and re-displays after surface switches or navigation).
	$effect(() => {
		if (!isPresenting() || !videoEl) return;
		const stream = presenterSession.stream;
		if (stream) {
			videoEl.srcObject = stream;
			videoEl.muted = true;
			void videoEl.play().catch(() => {});
		}
	});

	// ---- Viewer ----

	// Viewer negotiation is a retry loop, not a one-shot: a createOffer can
	// hang forever (observed on Chromium: a fresh pc on a page that closed a
	// previously connected pc — the promise never settles and never rejects),
	// and an offer can be lost when it reaches the presenter before their
	// session mounted. Every attempt runs on a fresh peer connection under a
	// deadline; abandonment is visible with a manual retry.
	const OFFER_STEP_TIMEOUT_MS = 4000;
	const ATTEMPT_DEADLINE_MS = 12000;
	const RETRY_BACKOFF_MS = 1200;
	const MAX_ATTEMPTS = 4;
	// How long a connected pc may sit in 'disconnected' before it is
	// recycled (network change, sleep/resume — a fresh connection is the
	// reliable recovery), and how long the presenter's signaling may be
	// gone before the share is treated as source-less (bounded by the
	// server's no-source GC grace).
	const DISCONNECTED_WATCHDOG_MS = 10_000;
	const PRESENTER_OFFLINE_GRACE_MS = 30_000;

	let attempt = 0;
	let attemptToken = 0;
	let disconnectedWatchdog: number | null = null;
	let presenterOfflineTimer: number | null = null;
	let attemptDeadline: number | null = null;
	let retryTimer: number | null = null;

	/** Reject if `step` (createOffer / setLocalDescription) never settles. */
	function withDeadline<T>(step: Promise<T>, what: string): Promise<T> {
		return new Promise<T>((resolve, reject) => {
			const timer = window.setTimeout(() => reject(new Error(`${what} timed out`)), OFFER_STEP_TIMEOUT_MS);
			step.then(
				(v) => {
					window.clearTimeout(timer);
					resolve(v);
				},
				(e) => {
					window.clearTimeout(timer);
					reject(e);
				}
			);
		});
	}

	function giveUp() {
		teardownViewerPc();
		connecting = false;
		reconnecting = false;
		endReason = 'error';
		streamEnded = true;
	}

	/** Start a negotiation attempt unless one is already in flight or live. */
	function ensureNegotiating(force = false) {
		if (disposed || streamEnded || presentEnded || isPresenting()) return;
		if (live && !force) return;
		if (!force && viewerPc) return; // an attempt is in flight; its deadline governs
		startViewerAttempt();
	}

	function startViewerAttempt() {
		const token = ++attemptToken;
		attempt += 1;
		clearWatchdog();
		teardownViewerPc(); // fresh peer connection every attempt
		connecting = true;
		reconnecting = false;
		const pc = new RTCPeerConnection({ iceServers: ICE_SERVERS });
		viewerPc = pc;
		viewerRemoteSettled = false;
		negotiationId = crypto.randomUUID();
		presenterPeerId = '';
		const currentNegotiation = negotiationId;
		let offerSent = false;
		const outgoingCandidates: RTCIceCandidateInit[] = [];
		const sendCandidate = (candidate: RTCIceCandidateInit) => {
			sendShareSignal(
				workspaceSlug,
				shareId,
				presenterPeerId || knownPresenterPeer
					? { peerId: presenterPeerId || knownPresenterPeer }
					: { userId: presenterUserId },
				currentNegotiation,
				{ type: 'candidate', candidate }
			);
		};
		pc.addTransceiver('video', { direction: 'recvonly' });
		pc.addTransceiver('audio', { direction: 'recvonly' });
		const remote = new MediaStream();
		pc.ontrack = (e) => {
			if (pc !== viewerPc || token !== attemptToken || disposed) return;
			remote.addTrack(e.track);
			if (videoEl) {
				videoEl.srcObject = remote;
				videoEl.muted = audioMuted;
				void videoEl.play().catch(() => {});
			}
			if (e.track.kind === 'video') markLive();
		};
		pc.onicecandidate = (e) => {
			if (e.candidate && presenterUserId && pc === viewerPc && token === attemptToken) {
				const candidate = e.candidate.toJSON();
				if (offerSent) sendCandidate(candidate);
				else outgoingCandidates.push(candidate);
			}
		};
		pc.onconnectionstatechange = () => {
			if (pc !== viewerPc || token !== attemptToken || disposed) return;
			if (pc.connectionState === 'connected') {
				markLive();
			} else if (pc.connectionState === 'disconnected') {
				// Media may resume on its own; if it does not within the
				// watchdog window, recycle the connection.
				reconnecting = true;
				armWatchdog(token);
			} else if (pc.connectionState === 'failed') {
				// A dead connection recycles immediately (fresh pc) instead
				// of ending the tile — live or not; the retry budget bounds
				// it.
				if (token === attemptToken && !streamEnded) {
					attempt = live ? 0 : attempt;
					live = false;
					retryOrGiveUp(token);
				}
			}
		};

		const attemptExpired = () => {
			if (token !== attemptToken || disposed || live || streamEnded) return;
			retryOrGiveUp(token);
		};
		// Covers everything after the offer is out: answer lost in the mount
		// race, ICE that never completes, or a createOffer that hung.
		attemptDeadline = window.setTimeout(attemptExpired, ATTEMPT_DEADLINE_MS);

		void (async () => {
			try {
				const offer = await withDeadline(pc.createOffer(), 'createOffer');
				if (token !== attemptToken || disposed) return;
				await withDeadline(pc.setLocalDescription(offer), 'setLocalDescription');
				if (token !== attemptToken || disposed) return;
				if (!presenterUserId) return;
				sendShareSignal(
					workspaceSlug,
					shareId,
					knownPresenterPeer ? { peerId: knownPresenterPeer } : { userId: presenterUserId },
					currentNegotiation,
					{ type: 'offer', sdp: pc.localDescription!.sdp! }
				);
				offerSent = true;
				for (const candidate of outgoingCandidates) sendCandidate(candidate);
				outgoingCandidates.length = 0;
			} catch {
				if (token === attemptToken && !live) retryOrGiveUp(token);
			}
		})();
	}

	function armWatchdog(token: number) {
		clearWatchdog();
		disconnectedWatchdog = window.setTimeout(() => {
			if (token !== attemptToken || disposed || streamEnded || !live) return;
			if (viewerPc?.connectionState !== 'disconnected') return;
			// Still limping: replace the connection outright. A live share
			// keeps its retry budget; a dead one gives up as usual.
			attempt = 0;
			startViewerAttempt();
		}, DISCONNECTED_WATCHDOG_MS);
	}

	function clearWatchdog() {
		if (disconnectedWatchdog) {
			window.clearTimeout(disconnectedWatchdog);
			disconnectedWatchdog = null;
		}
	}

	function retryOrGiveUp(token: number) {
		if (token !== attemptToken || disposed || live || streamEnded) return;
		teardownViewerPc();
		if (attempt >= MAX_ATTEMPTS) {
			giveUp();
			return;
		}
		retryTimer = window.setTimeout(() => {
			if (token === attemptToken && !disposed && !live && !streamEnded) startViewerAttempt();
		}, RETRY_BACKOFF_MS);
	}

	/** Manual retry after the tile gave up. */
	function retryConnection() {
		if (!streamEnded || live || isPresenting()) return;
		attempt = 0;
		streamEnded = false;
		endReason = null;
		ensureNegotiating(true);
	}

	function teardownViewerPc() {
		clearWatchdog();
		if (attemptDeadline !== null) window.clearTimeout(attemptDeadline);
		if (retryTimer !== null) window.clearTimeout(retryTimer);
		attemptDeadline = null;
		retryTimer = null;
		const retired = viewerPc;
		viewerPc = null;
		live = false;
		if (retired) {
			retired.ontrack = null;
			retired.onicecandidate = null;
			retired.onconnectionstatechange = null;
			retired.close();
		}
		pendingCandidates.delete('viewer');
	}

	function clearPresenterOffline() {
		presenterOffline = false;
		if (presenterOfflineTimer) {
			window.clearTimeout(presenterOfflineTimer);
			presenterOfflineTimer = null;
		}
	}

	async function handlePresenterSignal(peerId: string, incomingNegotiation: string, data: SignalData) {
		const pc = viewerPc;
		const token = attemptToken;
		if (!pc || incomingNegotiation !== negotiationId || (presenterPeerId && peerId !== presenterPeerId)) return;
		presenterPeerId = peerId;
		if (data.type === 'answer') {
			if (viewerRemoteSettled || pc.signalingState !== 'have-local-offer') return;
			try {
				await pc.setRemoteDescription({ type: 'answer', sdp: data.sdp });
				if (pc !== viewerPc || token !== attemptToken || disposed) return;
				viewerRemoteSettled = true;
				await flushCandidates('viewer', pc);
			} catch {
				// Broken answer: recycle like any failed attempt instead of
				// killing the tile.
				if (pc === viewerPc && token === attemptToken) retryOrGiveUp(token);
			}
		} else if (data.type === 'candidate') {
			queueCandidate('viewer', data.candidate, pc);
		}
	}

	function markLive() {
		clearWatchdog();
		if (attemptDeadline !== null) window.clearTimeout(attemptDeadline);
		attemptDeadline = null;
		connecting = false;
		reconnecting = false;
		endReason = null;
		streamEnded = false;
		live = true;
	}

	// ---- Lifecycle ----

	function teardown() {
		if (disposed) return;
		disposed = true;
		attemptToken++; // cancel any in-flight attempt bookkeeping
		if (!isPresenting()) {
			announceSharePresence(workspaceSlug, shareId, 'viewer', 'leave');
			clearPresenterOffline();
			teardownViewerPc();
		}
		for (const off of cleanups) off();
		cleanups.length = 0;
	}

	onMount(() => {
		if (isPresenting()) {
			// Presenter view over the persistent session store; the engine
			// (capture, peers, signaling) lives there, not in this tile.
			presentEnded = !presenterSession.active;
			live = presenterSession.active;
			connecting = false;
		} else {
			announceSharePresence(workspaceSlug, shareId, 'viewer', 'join');
			startViewerAttempt();
		}

		cleanups.push(
			onShareSignal((event) => {
				if (event.share_id !== shareId || presentEnded) return;
				if (!isPresenting()) {
					// Only the share's presenter may answer us. Signals still
					// apply after a give-up so a manual retry finds a live pc.
					if (event.from === presenterUserId && !streamEnded)
						void handlePresenterSignal(event.from_peer, event.negotiation_id, event.data);
				}
			})
		);

		cleanups.push(
			onSharePresence((event) => {
				if (event.share_id !== shareId) return;
				if (isPresenting()) return;
				viewerCount = event.viewers;
				const previousPresenterPeer = knownPresenterPeer;
				knownPresenterPeer = event.presenter_peer;
				if (!event.presenter_online) {
					// The presenter's socket dropped. Media is peer-to-peer
					// and may still flow; treat the share as gone only after
					// a grace window (the server's no-source GC runs on the
					// same clock at 45s).
					if (streamEnded) return;
					if (!presenterOffline) {
						presenterOffline = true;
						presenterOfflineTimer = window.setTimeout(() => {
							if (disposed || isPresenting() || streamEnded) return;
							attemptToken++;
							teardownViewerPc();
							connecting = false;
							endReason = 'disconnected';
							streamEnded = true;
						}, PRESENTER_OFFLINE_GRACE_MS);
					}
				} else {
					clearPresenterOffline();
					if (
						(presenterPeerId || previousPresenterPeer) &&
						(presenterPeerId || previousPresenterPeer) !== knownPresenterPeer &&
						viewerPc
					) {
						attempt = 0;
						streamEnded = false;
						endReason = null;
						ensureNegotiating(true);
					} else if (!live) {
						// The presenter is (back) online — e.g. their session
						// just re-announced after a reconnect. Revive a tile
						// that died on the earlier offline transition; an
						// in-flight attempt keeps running under its own
						// deadline.
						if (streamEnded) {
							attempt = 0;
							streamEnded = false;
							endReason = null;
						}
						ensureNegotiating();
					}
				}
			})
		);

		cleanups.push(
			onWebSocketReconnected((reconnectedSlug) => {
				if (reconnectedSlug !== workspaceSlug) return;
				if (disposed || presentEnded) return;
				if (isPresenting()) return; // the session store re-announces itself
				// Presence is per-connection: re-announce, then renegotiate
				// whatever died with the old socket.
				announceSharePresence(workspaceSlug, shareId, 'viewer', 'join');
				attempt = 0;
				streamEnded = false;
				endReason = null;
				knownPresenterPeer = '';
				ensureNegotiating(true);
			})
		);

		cleanups.push(
			onWebSocketDisconnected((disconnectedSlug) => {
				if (disconnectedSlug !== workspaceSlug) return;
				if (disposed || isPresenting() || streamEnded) return;
				// Signaling is down (e.g. a backend restart): surface it; the
				// reconnect handler above re-establishes everything.
				reconnecting = true;
			})
		);

		// Returning to the tab after sleep/OS suspend: renegotiate anything
		// that died while hidden.
		const handleVisibility = () => {
			if (document.visibilityState !== 'visible' || isPresenting() || streamEnded) return;
			if (!live || (viewerPc && viewerPc.connectionState !== 'connected')) {
				attempt = 0;
				ensureNegotiating(true);
			}
		};
		document.addEventListener('visibilitychange', handleVisibility);
		cleanups.push(() => document.removeEventListener('visibilitychange', handleVisibility));
	});

	onDestroy(() => {
		teardown();
	});

	function toggleAudio() {
		audioMuted = !audioMuted;
		if (videoEl) videoEl.muted = audioMuted;
		if (!audioMuted) void videoEl?.play();
	}

	function toggleFullscreen() {
		if (!videoEl) return;
		if (document.fullscreenElement) {
			void document.exitFullscreen();
		} else {
			void videoEl.parentElement?.requestFullscreen();
		}
	}
</script>

<div
	class="relative flex h-full min-h-0 w-full items-center justify-center overflow-hidden rounded-lg border border-[var(--color-border)] bg-black"
>
	<!-- svelte-ignore a11y_media_has_caption -->
	<video class="h-full w-full object-contain" playsinline autoplay bind:this={videoEl}></video>

	{#if mode === 'view' && connecting && !streamEnded}
		<div class="absolute inset-0 flex flex-col items-center justify-center gap-2 bg-black/70 text-white">
			<Loader2 size={24} class="animate-spin" />
			<p class="text-sm">{m['shares.connecting_viewer']()}</p>
		</div>
	{/if}

	{#if mode === 'view' && presenterOffline && !streamEnded}
		<div
			class="absolute left-3 top-3 flex items-center gap-1.5 rounded-full bg-amber-500/90 px-2.5 py-1 text-xs font-medium text-black"
		>
			<Loader2 size={12} class="animate-spin" />
			{m['shares.presenter_reconnecting']()}
		</div>
	{:else if reconnecting && !streamEnded && !presentEnded}
		<div
			class="absolute left-3 top-3 flex items-center gap-1.5 rounded-full bg-amber-500/90 px-2.5 py-1 text-xs font-medium text-black"
		>
			<Loader2 size={12} class="animate-spin" />
			{m['shares.reconnecting']()}
		</div>
	{/if}

	{#if !(live && !connecting) && presenterName}
		<div class="pointer-events-none absolute left-3 top-3">
			<span class="truncate rounded-full bg-black/60 px-2.5 py-1 text-xs text-white">{presenterName}</span>
		</div>
	{/if}

	{#if live && !connecting}
		<div class="pointer-events-none absolute left-3 top-3 flex items-center gap-2">
			<span
				class="flex max-w-[50%] items-center gap-1.5 rounded-full bg-red-500/90 px-2.5 py-1 text-xs font-semibold text-white"
			>
				<span class="h-1.5 w-1.5 animate-pulse rounded-full bg-white"></span>
				{mode === 'present' ? m['shares.sharing_badge']() : m['shares.live_badge']()}
			</span>
			{#if presenterName}
				<span class="truncate rounded-full bg-black/60 px-2.5 py-1 text-xs text-white">{presenterName}</span>
			{/if}
			{#if (mode === 'present' ? presenterSession.viewerCount : viewerCount) > 0}
				<span class="rounded-full bg-black/60 px-2.5 py-1 text-xs text-white">
					{m['shares.viewer_count']({ count: mode === 'present' ? presenterSession.viewerCount : viewerCount })}
				</span>
			{/if}
			{#if mode === 'view'}
				<span class="rounded-full bg-black/60 p-1.5 text-white" title={viewerPc?.connectionState ?? 'new'}>
					{#if viewerPc?.connectionState === 'connected'}
						<SignalHigh size={12} />
					{:else if viewerPc?.connectionState === 'disconnected'}
						<SignalLow size={12} />
					{:else}
						<SignalMedium size={12} />
					{/if}
				</span>
			{/if}
		</div>

		<div class="absolute right-3 top-3 flex items-center gap-2">
			{#if mode === 'present'}
				<div class="flex overflow-hidden rounded-md border border-white/20 bg-black/60 text-xs text-white">
					<button
						class="px-2.5 py-1 {presenterSession.qualityPreference === 'sharp'
							? 'bg-white/20 font-medium'
							: 'hover:bg-white/10'}"
						onclick={() => void presenterSession.setQuality('sharp')}
					>
						{m['shares.quality_sharp']()}
					</button>
					<button
						class="px-2.5 py-1 {presenterSession.qualityPreference === 'smooth'
							? 'bg-white/20 font-medium'
							: 'hover:bg-white/10'}"
						onclick={() => void presenterSession.setQuality('smooth')}
					>
						{m['shares.quality_smooth']()}
					</button>
				</div>
			{:else}
				<Button
					variant="secondary"
					size="sm"
					class="h-7 bg-black/60 text-white hover:bg-black/80"
					onclick={toggleAudio}
					title={audioMuted ? m['shares.unmute']() : m['shares.mute']()}
				>
					{#if audioMuted}
						<VolumeX size={13} />
					{:else}
						<Volume2 size={13} />
					{/if}
				</Button>
			{/if}
			<Button
				variant="secondary"
				size="sm"
				class="h-7 bg-black/60 text-white hover:bg-black/80"
				onclick={toggleFullscreen}
			>
				<Maximize2 size={13} />
			</Button>
		</div>

		{#if mode === 'present'}
			<div class="absolute bottom-3 right-3 flex items-center gap-2">
				<Button
					variant="secondary"
					size="sm"
					class="bg-black/60 text-white hover:bg-black/80"
					onclick={() => void presenterSession.switchSurface()}
					disabled={presenterSession.switching}
				>
					{#if presenterSession.switching}
						<Loader2 size={14} class="animate-spin" />
					{:else}
						<RefreshCw size={14} />
					{/if}
					{m['shares.switch_surface']()}
				</Button>
				<Button variant="destructive" size="sm" onclick={() => void presenterSession.stop()}>
					<MonitorUp size={14} />
					{m['shares.stop']()}
				</Button>
			</div>
		{/if}
	{/if}

	{#if streamEnded && endReason === 'error'}
		<!-- Viewer gave up (retry budget exhausted or ICE failed with no
		     relay by design): say so and offer a manual retry instead of
		     spinning on "Connecting…" forever. -->
		<div class="absolute inset-0 flex flex-col items-center justify-center gap-2 bg-black/80 text-white">
			<p class="text-sm font-medium">{m['shares.connect_failed']()}</p>
			<Button variant="secondary" size="sm" class="bg-white/10 text-white hover:bg-white/20" onclick={retryConnection}>
				<RefreshCw size={14} />
				{m['shares.retry']()}
			</Button>
		</div>
	{:else if streamEnded || presentEnded}
		<!-- Presenter gone or stopped: the share has no source. -->
		<div class="absolute inset-0 flex flex-col items-center justify-center gap-1 bg-black/80 text-white">
			<p class="text-sm font-medium">{m['shares.ended']()}</p>
		</div>
	{/if}
</div>
