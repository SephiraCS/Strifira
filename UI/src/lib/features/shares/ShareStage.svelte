<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import {
		Room,
		RoomEvent,
		Track,
		VideoPreset,
		ConnectionQuality,
		type RemoteTrack,
		type LocalVideoTrack
	} from 'livekit-client';
	import { Button } from '$lib/components/ui/button';
	import { Maximize2, MonitorUp, Loader2, SignalHigh, SignalMedium, SignalLow } from 'lucide-svelte';
	import { m } from '$lib/paraglide/messages.js';

	let {
		serverUrl,
		token,
		mode,
		onEnded,
		onStatus
	}: {
		serverUrl: string;
		token: string;
		/** 'present' publishes the local screen; 'view' subscribes. */
		mode: 'present' | 'view';
		/** Fired when the room disconnects or the presenter stops. */
		onEnded?: (reason: 'disconnected' | 'stopped' | 'error') => void;
		/** Optional status sink for the hosting page. */
		onStatus?: (status: { state: 'connecting' | 'live' | 'reconnecting' | 'ended'; viewerCount?: number }) => void;
	} = $props();

	// "sharp" prioritizes resolution for text-heavy screens; "smooth"
	// prioritizes framerate for video/motion content.
	let qualityPreference = $state<'sharp' | 'smooth'>('sharp');

	let room = $state<Room | null>(null);
	let videoEl: HTMLVideoElement;
	let live = $state(false);
	let connecting = $state(true);
	let reconnecting = $state(false);
	let viewerCount = $state(0);
	let connQuality = $state<ConnectionQuality>(ConnectionQuality.Unknown);
	let presentEnded = $state(false);
	let streamEnded = $state(false);
	let qualityBusy = $state(false);

	// Screenshare-optimized publishing: high-bitrate 1080p with 60fps
	// headroom, plus simulcast layers so weak links subscribe low instead
	// of stalling the presenter.
	const screenSharePublishOptions = {
		screenShareEncoding: { maxBitrate: 8_000_000, maxFramerate: 60 },
		screenShareSimulcastLayers: [
			new VideoPreset(1920, 1080, 8_000_000, 60),
			new VideoPreset(1280, 720, 3_000_000, 30),
			new VideoPreset(640, 360, 600_000, 15)
		],
		degradationPreference: 'maintain-resolution' as const
	};

	function reportStatus() {
		onStatus?.({
			state: presentEnded || streamEnded ? 'ended' : reconnecting ? 'reconnecting' : live ? 'live' : 'connecting',
			viewerCount
		});
	}

	async function connect() {
		connecting = true;
		reportStatus();

		const r = new Room({
			adaptiveStream: true,
			dynacast: true
		});
		room = r;

		r.on(RoomEvent.TrackSubscribed, (track: RemoteTrack) => {
			if (track.kind === Track.Kind.Video) track.attach(videoEl);
		});
		r.on(RoomEvent.TrackUnsubscribed, (track: RemoteTrack) => {
			track.detach(videoEl);
		});
		r.on(RoomEvent.ParticipantConnected, updateViewerCount);
		r.on(RoomEvent.ParticipantDisconnected, updateViewerCount);
		r.on(RoomEvent.ConnectionQualityChanged, (quality: ConnectionQuality) => {
			connQuality = quality;
		});
		r.on(RoomEvent.Disconnected, () => {
			if (mode === 'view') {
				streamEnded = true;
				reportStatus();
				onEnded?.('disconnected');
			}
		});
		r.on(RoomEvent.Reconnecting, () => {
			reconnecting = true;
			reportStatus();
		});
		r.on(RoomEvent.Reconnected, () => {
			reconnecting = false;
			reportStatus();
		});

		try {
			await r.connect(serverUrl, token);
		} catch {
			reportStatus();
			onEnded?.('error');
			return;
		}

		if (mode === 'present') {
			try {
				await publishScreen();
			} catch {
				// User cancelled the picker, or capture was denied — the
				// share ends before anyone saw anything.
				await r.disconnect();
				presentEnded = true;
				connecting = false;
				reportStatus();
				onEnded?.('stopped');
				return;
			}
		}

		connecting = false;
		live = true;
		updateViewerCount();
		reportStatus();
	}

	async function publishScreen() {
		if (!room) return;
		const publication = await room.localParticipant.setScreenShareEnabled(
			true,
			{
				audio: true,
				selfBrowserSurface: 'exclude',
				surfaceSwitching: 'include',
				systemAudio: 'include',
				contentHint: qualityPreference === 'sharp' ? 'detail' : 'motion'
			},
			screenSharePublishOptions
		);
		const track = publication?.videoTrack as LocalVideoTrack | undefined;
		if (track) {
			// Presenter previews locally; muted loopback avoids feeding the
			// captured audio back into the room.
			track.attach(videoEl);
			videoEl.muted = true;
		}
	}

	async function applyQualityPreference(pref: 'sharp' | 'smooth') {
		if (pref === qualityPreference || qualityBusy || !room) return;
		qualityBusy = true;
		qualityPreference = pref;
		try {
			const publication = [...room.localParticipant.trackPublications.values()].find(
				(p) => p.source === Track.Source.ScreenShare
			);
			const track = publication?.videoTrack as LocalVideoTrack | undefined;
			if (track) {
				track.mediaStreamTrack.contentHint = pref === 'sharp' ? 'detail' : 'motion';
				await track.setDegradationPreference(pref === 'sharp' ? 'maintain-resolution' : 'balanced');
			}
		} finally {
			qualityBusy = false;
		}
	}

	function updateViewerCount() {
		if (!room) return;
		viewerCount = room.remoteParticipants.size;
		reportStatus();
	}

	async function stopSharing() {
		if (room) {
			await room.localParticipant.setScreenShareEnabled(false);
			await room.disconnect();
		}
		live = false;
		presentEnded = true;
		reportStatus();
		onEnded?.('stopped');
	}

	async function leave() {
		if (room) await room.disconnect();
		live = false;
		streamEnded = true;
		reportStatus();
		onEnded?.('stopped');
	}

	function toggleFullscreen() {
		if (!videoEl) return;
		if (document.fullscreenElement) {
			void document.exitFullscreen();
		} else {
			void videoEl.parentElement?.requestFullscreen();
		}
	}

	onMount(connect);

	onDestroy(() => {
		if (room && room.state !== 'disconnected') void room.disconnect();
	});
</script>

<div class="relative flex h-full w-full items-center justify-center overflow-hidden rounded-lg border border-[var(--color-border)] bg-black">
	<!-- svelte-ignore a11y_media_has_caption -->
	<video class="h-full w-full object-contain" playsinline autoplay bind:this={videoEl}></video>

	{#if connecting}
		<div class="absolute inset-0 flex flex-col items-center justify-center gap-2 bg-black/70 text-white">
			<Loader2 size={24} class="animate-spin" />
			<p class="text-sm">
				{mode === 'present' ? m['shares.connecting_presenter']() : m['shares.connecting_viewer']()}
			</p>
		</div>
	{:else if reconnecting && !streamEnded && !presentEnded}
		<div class="absolute left-3 top-3 flex items-center gap-1.5 rounded-full bg-amber-500/90 px-2.5 py-1 text-xs font-medium text-black">
			<Loader2 size={12} class="animate-spin" />
			{m['shares.reconnecting']()}
		</div>
	{/if}

	{#if live && !connecting}
		<div class="pointer-events-none absolute left-3 top-3 flex items-center gap-2">
			<span class="flex items-center gap-1.5 rounded-full bg-red-500/90 px-2.5 py-1 text-xs font-semibold text-white">
				<span class="h-1.5 w-1.5 animate-pulse rounded-full bg-white"></span>
				{mode === 'present' ? m['shares.sharing_badge']() : m['shares.live_badge']()}
			</span>
			{#if viewerCount > 0}
				<span class="rounded-full bg-black/60 px-2.5 py-1 text-xs text-white">
					{m['shares.viewer_count']({ count: viewerCount })}
				</span>
			{/if}
			<span class="rounded-full bg-black/60 p-1.5 text-white" title={connQuality}>
				{#if connQuality === ConnectionQuality.Poor}
					<SignalLow size={12} />
				{:else if connQuality === ConnectionQuality.Good}
					<SignalMedium size={12} />
				{:else}
					<SignalHigh size={12} />
				{/if}
			</span>
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
			{/if}
			<Button variant="secondary" size="sm" class="h-7 bg-black/60 text-white hover:bg-black/80" onclick={toggleFullscreen}>
				<Maximize2 size={13} />
			</Button>
		</div>

		<div class="absolute bottom-3 right-3">
			{#if mode === 'present'}
				<Button variant="destructive" size="sm" onclick={stopSharing}>
					<MonitorUp size={14} />
					{m['shares.stop']()}
				</Button>
			{:else}
				<Button variant="secondary" size="sm" onclick={leave}>
					{m['shares.leave']()}
				</Button>
			{/if}
		</div>
	{/if}

	{#if (streamEnded || presentEnded) && !connecting}
		<div class="absolute inset-0 flex flex-col items-center justify-center gap-1 bg-black/80 text-white">
			<p class="text-sm font-medium">{m['shares.ended']()}</p>
		</div>
	{/if}
</div>
