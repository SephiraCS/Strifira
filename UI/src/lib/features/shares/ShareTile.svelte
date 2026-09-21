<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { beforeNavigate } from '$app/navigation';
	import {
		announceSharePresence,
		onSharePresence,
		onShareSignal,
		onWebSocketReconnected,
		sendShareSignal,
		ICE_SERVERS,
		type SignalData
	} from './signaling';
	import { Button } from '$lib/components/ui/button';
	import {
		Maximize2,
		MonitorUp,
		RefreshCw,
		Loader2,
		SignalHigh,
		SignalMedium,
		SignalLow,
		Volume2,
		VolumeX
	} from 'lucide-svelte';
	import { m } from '$lib/paraglide/messages.js';

	let {
		shareId,
		mode,
		presenterName,
		presenterUserId,
		sourceStream,
		onEnded,
		onPresenterStop
	}: {
		shareId: string;
		/** 'present' sends the local screen; 'view' receives it. */
		mode: 'present' | 'view';
		presenterName: string;
		/** The share's started_by user id — viewers address offers to them. */
		presenterUserId: string;
		/**
		 * Capture-first flow: the page captures via getDisplayMedia inside
		 * the click's user-activation window (Chrome denies capture once
		 * the gesture has expired behind async work — no picker appears)
		 * and hands the stream here for sending.
		 */
		sourceStream?: MediaStream;
		/** Fired when the share ends from any side. */
		onEnded?: (reason: 'disconnected' | 'stopped' | 'error') => void;
		/**
		 * Presenter Stop hook: the owning page knows the workspace/share
		 * ids needed to end the share server-side (broadcasts share.ended).
		 * Failures are non-fatal; the local stream still stops.
		 */
		onPresenterStop?: () => Promise<void>;
	} = $props();

	// "sharp" prioritizes resolution for text-heavy screens; "smooth"
	// prioritizes framerate for video/motion content.
	let qualityPreference = $state<'sharp' | 'smooth'>('sharp');

	let videoEl: HTMLVideoElement;
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
	let switching = $state(false);
	let stopping = false;
	let qualityBusy = $state(false);
	// Browsers block unmuted autoplay: viewer tiles start muted with an
	// explicit unmute toggle.
	let audioMuted = $state(true);

	// ---- Peer plumbing (P2P WebRTC over the workspace WS) ----

	// Presenter side: one RTCPeerConnection per viewer.
	const peers = new Map<string, RTCPeerConnection>();
	// ICE candidates that arrived before the remote description was set.
	const pendingCandidates = new Map<string, RTCIceCandidateInit[]>();
	// Viewer side: this tab's single connection to the presenter.
	let viewerPc = $state<RTCPeerConnection | null>(null);
	let viewerRemoteSettled = false;
	// Why a viewer tile stopped: 'disconnected' (presenter gone — no source
	// to retry against) vs 'error' (negotiation/ICE failed — manual retry).
	let endReason = $state<'disconnected' | 'error' | null>(null);
	let disposed = false;
	const cleanups: Array<() => void> = [];

	function isPresenting(): boolean {
		return mode === 'present';
	}

	// ---- Leave guard ----

	// Leaving this tab (route change, reload, close) while it is the live
	// source of a share stops the stream for every viewer — confirm the
	// departure instead of dropping the share silently.
	function presentingLive(): boolean {
		return isPresenting() && live && !presentEnded && !stopping;
	}

	function handleBeforeUnload(e: BeforeUnloadEvent) {
		if (presentingLive()) e.preventDefault();
	}

	// In-app route changes only. Anything that unloads the document
	// (type 'leave', external links — willUnload) is left to beforeunload
	// so one departure never prompts twice.
	beforeNavigate((nav) => {
		if (nav.willUnload || !nav.from || !nav.to) return;
		if (nav.to.url.pathname === nav.from.url.pathname) return;
		if (presentingLive() && !confirm(m['shares.leave_warning']())) nav.cancel();
	});

	function applyContentHints(stream: MediaStream) {
		const video = stream.getVideoTracks()[0];
		if (video) video.contentHint = qualityPreference === 'sharp' ? 'detail' : 'motion';
	}

	async function applySenderQuality(pc: RTCPeerConnection) {
		for (const sender of pc.getSenders()) {
			if (sender.track?.kind !== 'video') continue;
			try {
				const params = sender.getParameters();
				params.degradationPreference = qualityPreference === 'sharp' ? 'maintain-resolution' : 'balanced';
				await sender.setParameters(params);
			} catch {
				// Browser without degradationPreference support: contentHint
				// already carries the intent.
			}
		}
	}

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

	// ---- Presenter ----

	function createPresenterPeer(viewerId: string): RTCPeerConnection {
		const pc = new RTCPeerConnection({ iceServers: ICE_SERVERS });
		peers.set(viewerId, pc);
		if (sourceStream) {
			applyContentHints(sourceStream);
			for (const track of sourceStream.getTracks()) {
				pc.addTrack(track, sourceStream);
			}
			void applySenderQuality(pc);
		}
		pc.onicecandidate = (e) => {
			if (e.candidate) sendShareSignal(shareId, viewerId, { type: 'candidate', candidate: e.candidate.toJSON() });
		};
		pc.onconnectionstatechange = () => {
			if (pc.connectionState === 'failed' || pc.connectionState === 'closed') {
				if (peers.get(viewerId) === pc) {
					peers.delete(viewerId);
					pendingCandidates.delete(viewerId);
					pc.close();
				}
			}
		};
		return pc;
	}

	async function handleViewerSignal(viewerId: string, data: SignalData) {
		if (data.type === 'offer') {
			// A viewer (re)negotiating: replace any dead connection for it.
			const existing = peers.get(viewerId);
			if (existing) {
				if (existing.connectionState === 'connected' || existing.signalingState === 'have-remote-offer') {
					return; // stale duplicate offer
				}
				peers.delete(viewerId);
				existing.close();
			}
			const pc = createPresenterPeer(viewerId);
			try {
				await pc.setRemoteDescription({ type: 'offer', sdp: data.sdp });
				const answer = await pc.createAnswer();
				await pc.setLocalDescription(answer);
				sendShareSignal(shareId, viewerId, { type: 'answer', sdp: pc.localDescription!.sdp! });
				await flushCandidates(viewerId, pc);
			} catch {
				peers.delete(viewerId);
				pendingCandidates.delete(viewerId);
				pc.close();
			}
		} else if (data.type === 'candidate') {
			const pc = peers.get(viewerId);
			if (pc) queueCandidate(viewerId, data.candidate, pc);
		}
	}

	// ---- Viewer ----

	// Viewer negotiation is a retry loop, not a one-shot: a createOffer can
	// hang forever (observed on Chromium: a fresh pc on a page that closed a
	// previously connected pc — the promise never settles and never rejects),
	// and an offer can be lost when it reaches the presenter before their
	// tile mounted. Every attempt runs on a fresh peer connection under a
	// deadline; abandonment is visible with a manual retry.
	const OFFER_STEP_TIMEOUT_MS = 4000;
	const ATTEMPT_DEADLINE_MS = 12000;
	const RETRY_BACKOFF_MS = 1200;
	const MAX_ATTEMPTS = 4;

	let attempt = 0;
	let attemptToken = 0;

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
		onEnded?.('error');
	}

	/** Start a negotiation attempt unless one is already in flight or live. */
	function ensureNegotiating(force = false) {
		if (disposed || streamEnded || presentEnded || isPresenting()) return;
		if (live) return;
		if (!force && viewerPc) return; // an attempt is in flight; its deadline governs
		startViewerAttempt();
	}

	function startViewerAttempt() {
		const token = ++attemptToken;
		attempt += 1;
		teardownViewerPc(); // fresh peer connection every attempt
		connecting = true;
		reconnecting = false;
		const pc = new RTCPeerConnection({ iceServers: ICE_SERVERS });
		viewerPc = pc;
		viewerRemoteSettled = false;
		pc.addTransceiver('video', { direction: 'recvonly' });
		pc.addTransceiver('audio', { direction: 'recvonly' });
		const remote = new MediaStream();
		pc.ontrack = (e) => {
			remote.addTrack(e.track);
			if (videoEl) {
				videoEl.srcObject = remote;
				videoEl.muted = audioMuted;
				void videoEl.play().catch(() => {});
			}
			if (e.track.kind === 'video') markLive();
		};
		pc.onicecandidate = (e) => {
			if (e.candidate) sendShareSignal(shareId, presenterUserId, { type: 'candidate', candidate: e.candidate.toJSON() });
		};
		pc.onconnectionstatechange = () => {
			if (pc !== viewerPc) return;
			if (pc.connectionState === 'connected') {
				markLive();
			} else if (pc.connectionState === 'disconnected') {
				reconnecting = true;
			} else if (pc.connectionState === 'failed') {
				// A dead attempt recycles immediately (fresh pc) instead of
				// ending the tile; the retry budget bounds it.
				if (token === attemptToken && !live) retryOrGiveUp(token);
			}
		};

		const attemptExpired = () => {
			if (token !== attemptToken || disposed || live || streamEnded) return;
			retryOrGiveUp(token);
		};
		// Covers everything after the offer is out: answer lost in the mount
		// race, ICE that never completes, or a createOffer that hung.
		window.setTimeout(attemptExpired, ATTEMPT_DEADLINE_MS);

		void (async () => {
			try {
				const offer = await withDeadline(pc.createOffer(), 'createOffer');
				if (token !== attemptToken || disposed) return;
				await withDeadline(pc.setLocalDescription(offer), 'setLocalDescription');
				if (token !== attemptToken || disposed) return;
				sendShareSignal(shareId, presenterUserId, { type: 'offer', sdp: pc.localDescription!.sdp! });
			} catch {
				if (token === attemptToken && !live) retryOrGiveUp(token);
			}
		})();
	}

	function retryOrGiveUp(token: number) {
		if (token !== attemptToken || disposed || live || streamEnded) return;
		teardownViewerPc();
		if (attempt >= MAX_ATTEMPTS) {
			giveUp();
			return;
		}
		window.setTimeout(() => {
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
		viewerPc?.close();
		viewerPc = null;
		pendingCandidates.delete('viewer');
	}

	async function handlePresenterSignal(data: SignalData) {
		const pc = viewerPc;
		if (!pc) return;
		if (data.type === 'answer') {
			if (viewerRemoteSettled || pc.signalingState !== 'have-local-offer') return;
			try {
				await pc.setRemoteDescription({ type: 'answer', sdp: data.sdp });
				viewerRemoteSettled = true;
				await flushCandidates('viewer', pc);
			} catch {
				// Broken answer: recycle like any failed attempt instead of
				// killing the tile.
				retryOrGiveUp(attemptToken);
			}
		} else if (data.type === 'candidate') {
			queueCandidate('viewer', data.candidate, pc);
		}
	}

	function markLive() {
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
		announceSharePresence(shareId, isPresenting() ? 'presenter' : 'viewer', 'leave');
		window.removeEventListener('beforeunload', handleBeforeUnload);
		for (const pc of peers.values()) pc.close();
		peers.clear();
		pendingCandidates.clear();
		teardownViewerPc();
		for (const off of cleanups) off();
		cleanups.length = 0;
	}

	onMount(() => {
		window.addEventListener('beforeunload', handleBeforeUnload);
		if (isPresenting()) {
			if (!sourceStream) {
				presentEnded = true;
				connecting = false;
				onEnded?.('error');
				return;
			}
			// Presenter previews locally; muted loopback keeps the room quiet.
			applyContentHints(sourceStream);
			videoEl.srcObject = sourceStream;
			videoEl.muted = true;
			void videoEl.play().catch(() => {});
			// The browser ends the captured track itself when the user hits
			// the native "Stop sharing" bar or closes the shared surface —
			// treat that exactly like pressing our Stop button.
			sourceStream.getVideoTracks()[0]?.addEventListener('ended', handleCaptureEnded);
			announceSharePresence(shareId, 'presenter', 'join');
			live = true;
			connecting = false;
		} else {
			announceSharePresence(shareId, 'viewer', 'join');
			startViewerAttempt();
		}

		cleanups.push(
			onShareSignal((event) => {
				if (event.share_id !== shareId || presentEnded) return;
				if (isPresenting()) {
					if (event.from) void handleViewerSignal(event.from, event.data);
				} else {
					// Only the share's presenter may answer us. Signals still
					// apply after a give-up so a manual retry finds a live pc.
					if (event.from === presenterUserId && !streamEnded) void handlePresenterSignal(event.data);
				}
			})
		);

		cleanups.push(
			onSharePresence((event) => {
				if (event.share_id !== shareId) return;
				viewerCount = event.viewers;
				if (!isPresenting() && !event.presenter_online) {
					// Presenter socket gone: no source exists anymore.
					attemptToken++;
					teardownViewerPc();
					connecting = false;
					endReason = 'disconnected';
					streamEnded = true;
					onEnded?.('disconnected');
				} else if (!isPresenting() && event.presenter_online && !live) {
					// The presenter is (back) online — e.g. their tile just
					// mounted after our first offer was lost in the mount
					// race, or their page reloaded and re-announced. Revive a
					// tile that died on the earlier offline transition; an
					// in-flight attempt keeps running under its own deadline.
					if (streamEnded) {
						attempt = 0;
						streamEnded = false;
						endReason = null;
					}
					ensureNegotiating();
				}
			})
		);

		cleanups.push(
			onWebSocketReconnected(() => {
				if (disposed || presentEnded) return;
				// Presence is per-connection: re-announce, then renegotiate
				// whatever died with the old socket.
				announceSharePresence(shareId, isPresenting() ? 'presenter' : 'viewer', 'join');
				if (!isPresenting() && !live) {
					// The old socket's in-flight negotiation addressed a
					// membership that no longer exists; restart it. A socket
					// drop must not burn the whole retry budget.
					attempt = Math.min(attempt, MAX_ATTEMPTS - 1);
					if (!streamEnded) ensureNegotiating(true);
				}
			})
		);
	});

	onDestroy(() => {
		teardown();
	});

	// A native 'ended' that lands mid-switch (user hit the stop bar while
	// the picker was up) must survive the switch: the source is dead.
	let endAfterSwitch = false;

	function handleCaptureEnded() {
		// Track endings we caused ourselves (Stop, or swapping the surface)
		// already run their own teardown. `stopping` is set synchronously so
		// its own stopped track cannot re-enter.
		if (stopping || presentEnded || !live) return;
		if (switching) {
			endAfterSwitch = true;
			return;
		}
		void stopSharing();
	}

	/** Re-pick the captured screen/window/tab while staying live. */
	async function switchSurface() {
		if (!sourceStream || switching) return;
		switching = true;
		endAfterSwitch = false;
		try {
			// Chrome capture extensions (selfBrowserSurface etc.) sit beyond
			// the TS DOM lib's DisplayMediaStreamOptions.
			const captureOptions = {
				audio: true,
				video: { frameRate: 60 },
				selfBrowserSurface: 'exclude',
				surfaceSwitching: 'include',
				systemAudio: 'include'
			} as unknown as DisplayMediaStreamOptions;
			const next = await navigator.mediaDevices.getDisplayMedia(captureOptions);
			const nextVideo = next.getVideoTracks()[0];
			if (nextVideo) {
				nextVideo.contentHint = qualityPreference === 'sharp' ? 'detail' : 'motion';
				nextVideo.addEventListener('ended', handleCaptureEnded);
			}
			// Swap the track on every live peer; renegotiation is not needed
			// for a same-kind replaceTrack. replaceTrack can take a long
			// time to settle (observed tens of seconds on a Firefox peer) —
			// the switch must not block on it: fire-and-forget, and a dead
			// old source is stopped only after the new capture is live.
			const senders = [...peers.values()].flatMap((pc) => pc.getSenders());
			for (const sender of senders) {
				if (sender.track?.kind === 'video' && nextVideo) void sender.replaceTrack(nextVideo).catch(() => {});
				if (sender.track?.kind === 'audio') {
					const nextAudio = next.getAudioTracks()[0];
					if (sender.track || nextAudio) void sender.replaceTrack(nextAudio ?? null).catch(() => {});
				}
			}
			// Retire the previous capture; the new one feeds the preview.
			sourceStream.getTracks().forEach((t) => t.stop());
			sourceStream = next;
			videoEl.srcObject = next;
			videoEl.muted = true;
			void videoEl.play().catch(() => {});
		} catch {
			// Picker cancelled or capture denied — nothing is being shared
			// anymore; end the share (the row, viewers, tile) cleanly.
			await stopSharing();
		} finally {
			switching = false;
			if (endAfterSwitch && !stopping && !presentEnded) {
				endAfterSwitch = false;
				void stopSharing();
			}
		}
	}

	async function applyQualityPreference(pref: 'sharp' | 'smooth') {
		if (pref === qualityPreference || qualityBusy) return;
		qualityBusy = true;
		qualityPreference = pref;
		try {
			if (sourceStream) applyContentHints(sourceStream);
			for (const pc of peers.values()) await applySenderQuality(pc);
		} finally {
			qualityBusy = false;
		}
	}

	async function stopSharing() {
		if (stopping) return;
		stopping = true;
		// End the share server-side BEFORE notifying the wall: onEnded
		// triggers a shares reload, which must not race ahead of the end
		// commit and re-render this share as a viewer tile. Every async
		// step is best-effort — the capture may already be dead when the
		// browser-native stop fired it.
		try {
			await onPresenterStop?.();
		} catch {
			// Server-side end failed; lazy GC closes the row.
		}
		live = false;
		presentEnded = true;
		teardown();
		onEnded?.('stopped');
		// Release the browser's capture indicator for any still-live
		// source tracks (pre-captured or switched).
		const src = videoEl?.srcObject as MediaStream | null;
		src?.getTracks().forEach((t) => t.stop());
	}

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

<div class="relative flex h-full min-h-0 w-full items-center justify-center overflow-hidden rounded-lg border border-[var(--color-border)] bg-black">
	<!-- svelte-ignore a11y_media_has_caption -->
	<video class="h-full w-full object-contain" playsinline autoplay bind:this={videoEl}></video>

	{#if connecting && !streamEnded}
		<div class="absolute inset-0 flex flex-col items-center justify-center gap-2 bg-black/70 text-white">
			<Loader2 size={24} class="animate-spin" />
			<p class="text-sm">{m['shares.connecting_viewer']()}</p>
		</div>
	{:else if reconnecting && !streamEnded && !presentEnded}
		<div class="absolute left-3 top-3 flex items-center gap-1.5 rounded-full bg-amber-500/90 px-2.5 py-1 text-xs font-medium text-black">
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
			<span class="flex max-w-[50%] items-center gap-1.5 rounded-full bg-red-500/90 px-2.5 py-1 text-xs font-semibold text-white">
				<span class="h-1.5 w-1.5 animate-pulse rounded-full bg-white"></span>
				{mode === 'present' ? m['shares.sharing_badge']() : m['shares.live_badge']()}
			</span>
			{#if presenterName}
				<span class="truncate rounded-full bg-black/60 px-2.5 py-1 text-xs text-white">{presenterName}</span>
			{/if}
			{#if viewerCount > 0}
				<span class="rounded-full bg-black/60 px-2.5 py-1 text-xs text-white">
					{m['shares.viewer_count']({ count: viewerCount })}
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
						class="px-2.5 py-1 {qualityPreference === 'sharp' ? 'bg-white/20 font-medium' : 'hover:bg-white/10'}"
						onclick={() => applyQualityPreference('sharp')}
					>
						{m['shares.quality_sharp']()}
					</button>
					<button
						class="px-2.5 py-1 {qualityPreference === 'smooth' ? 'bg-white/20 font-medium' : 'hover:bg-white/10'}"
						onclick={() => applyQualityPreference('smooth')}
					>
						{m['shares.quality_smooth']()}
					</button>
				</div>
			{:else}
				<Button variant="secondary" size="sm" class="h-7 bg-black/60 text-white hover:bg-black/80" onclick={toggleAudio} title={audioMuted ? m['shares.unmute']() : m['shares.mute']()}>
					{#if audioMuted}
						<VolumeX size={13} />
					{:else}
						<Volume2 size={13} />
					{/if}
				</Button>
			{/if}
			<Button variant="secondary" size="sm" class="h-7 bg-black/60 text-white hover:bg-black/80" onclick={toggleFullscreen}>
				<Maximize2 size={13} />
			</Button>
		</div>

		{#if mode === 'present'}
			<div class="absolute bottom-3 right-3 flex items-center gap-2">
				<Button variant="secondary" size="sm" class="bg-black/60 text-white hover:bg-black/80" onclick={switchSurface} disabled={switching}>
					{#if switching}
						<Loader2 size={14} class="animate-spin" />
					{:else}
						<RefreshCw size={14} />
					{/if}
					{m['shares.switch_surface']()}
				</Button>
				<Button variant="destructive" size="sm" onclick={stopSharing}>
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
