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
		serverUrl,
		token,
		mode,
		presenterName,
		onEnded,
		onPresenterStop,
		onViewerCount
	}: {
		serverUrl: string;
		token: string;
		/** 'present' publishes the local screen; 'view' subscribes. */
		mode: 'present' | 'view';
		presenterName: string;
		/** Fired when the room disconnects or the presenter stops. */
		onEnded?: (reason: 'disconnected' | 'stopped' | 'error') => void;
		/**
		 * Presenter Stop hook: the owning page knows the workspace/share
		 * ids needed to end the share server-side (broadcasts share.ended).
		 * Failures are non-fatal; the local stream still stops.
		 */
		onPresenterStop?: () => Promise<void>;
		/** Live viewer count sink (driven by room membership). */
		onViewerCount?: (count: number) => void;
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
	let switching = $state(false);
	let stopping = false;
	let qualityBusy = $state(false);
	// Browsers block unmuted autoplay: viewer tiles start muted with an
	// explicit unmute toggle.
	let audioMuted = $state(true);

	function toggleAudio() {
		audioMuted = !audioMuted;
		if (videoEl) videoEl.muted = audioMuted;
		if (!audioMuted) void videoEl?.play();
	}

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

	function reportViewerCount() {
		if (!room) return;
		viewerCount = room.remoteParticipants.size;
		onViewerCount?.(viewerCount);
	}

	async function connect() {
		connecting = true;

		const r = new Room({
			adaptiveStream: true,
			dynacast: true
		});
		room = r;

		// Watchdog: ICE can stall indefinitely when the media path is
		// unreachable (e.g. firewall/NAT rules missing). Never leave a tile
		// spinning forever — tear down and (for presenters) end the share
		// so no source-less row lingers.
		const watchdog = setTimeout(() => {
			if (!live && !presentEnded && !streamEnded) {
				void failConnect('timeout');
			}
		}, 20000);

		r.on(RoomEvent.TrackSubscribed, (track: RemoteTrack) => {
			if (track.kind === Track.Kind.Video) track.attach(videoEl);
		});
		r.on(RoomEvent.TrackUnsubscribed, (track: RemoteTrack) => {
			track.detach(videoEl);
		});
		r.on(RoomEvent.ParticipantConnected, reportViewerCount);
		r.on(RoomEvent.ParticipantDisconnected, reportViewerCount);
		r.on(RoomEvent.ConnectionQualityChanged, (quality: ConnectionQuality) => {
			connQuality = quality;
		});
		r.on(RoomEvent.Disconnected, () => {
			if (mode === 'view') {
				streamEnded = true;
				onEnded?.('disconnected');
			}
		});
		r.on(RoomEvent.Reconnecting, () => {
			reconnecting = true;
		});
		r.on(RoomEvent.Reconnected, () => {
			reconnecting = false;
		});

		try {
			await r.connect(serverUrl, token);
		} catch {
			clearTimeout(watchdog);
			connecting = false;
			streamEnded = true;
			onEnded?.('error');
			return;
		}

		if (mode === 'present') {
			try {
				await publishScreen();
			} catch {
				clearTimeout(watchdog);
				// User cancelled the picker, or capture was denied — the
				// share ends before anyone saw anything.
				await r.disconnect();
				presentEnded = true;
				connecting = false;
				onEnded?.('stopped');
				return;
			}
		}

		clearTimeout(watchdog);
		connecting = false;
		live = true;
		reportViewerCount();
	}

	async function failConnect(_reason: 'timeout') {
		stopping = true;
		try {
			await onPresenterStop?.();
		} catch {
			// best-effort
		}
		if (room && room.state !== 'disconnected') {
			try {
				await room.disconnect();
			} catch {
				// already gone
			}
		}
		connecting = false;
		live = false;
		streamEnded = true;
		onEnded?.('error');
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
			// The browser ends the captured track itself when the user hits
			// the native "Stop sharing" bar or closes the shared surface —
			// treat that exactly like pressing our Stop button.
			track.mediaStreamTrack.addEventListener('ended', handleCaptureEnded);
		}
	}

	function handleCaptureEnded() {
		// Track endings we caused ourselves (Stop, or swapping the surface)
		// already run their own teardown. `stopping` is set synchronously
		// in stopSharing so its own unpublished track cannot re-enter.
		if (switching || stopping || presentEnded || !live) return;
		void stopSharing();
	}

	/** Re-pick the captured screen/window/tab while staying live. */
	async function switchSurface() {
		if (!room || switching) return;
		switching = true;
		try {
			await room.localParticipant.setScreenShareEnabled(false);
			await publishScreen();
		} catch {
			// Picker cancelled — keep sharing nothing is worse than the
			// previous state, but the track is already unpublished; end
			// locally and let the presenter restart if they want to.
			await stopSharing();
		} finally {
			switching = false;
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

	async function stopSharing() {
		if (stopping) return;
		stopping = true;
		// Settle the UI first: after this the wall clears the tile and the
		// row is either ended below or picked up by lazy GC. Every async
		// step is best-effort — the capture may already be dead when the
		// browser-native stop fired it (source track 'ended'), and
		// livekit-client can reject cleanup calls on a dead track.
		live = false;
		presentEnded = true;
		onEnded?.('stopped');
		try {
			await onPresenterStop?.();
		} catch {
			// Server-side end failed; lazy GC closes the row.
		}
		if (room) {
			try {
				await room.localParticipant.setScreenShareEnabled(false);
			} catch {
				// already unpublished/ended
			}
			try {
				await room.disconnect();
			} catch {
				// already disconnected
			}
		}
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

<div class="relative flex aspect-video w-full items-center justify-center overflow-hidden rounded-lg border border-[var(--color-border)] bg-black">
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

	{#if (streamEnded || presentEnded) && !connecting}
		<div class="absolute inset-0 flex flex-col items-center justify-center gap-1 bg-black/80 text-white">
			<p class="text-sm font-medium">{m['shares.ended']()}</p>
		</div>
	{/if}
</div>
