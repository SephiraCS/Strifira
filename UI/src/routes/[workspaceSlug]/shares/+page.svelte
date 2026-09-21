<script lang="ts">
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { sharesState } from '$lib/features/shares/shares.state.svelte';
	import { createShare } from '$lib/api/shares';
	import type { ScreenShare } from '$lib/types/share';
	import ShareTile from '$lib/features/shares/ShareTile.svelte';
	import { Button } from '$lib/components/ui/button';
	import EmptyState from '$lib/components/shared/EmptyState.svelte';
	import { appToast } from '$lib/features/toast/toast';
	import { MonitorUp, MonitorPlay, Loader2 } from 'lucide-svelte';
	import { m } from '$lib/paraglide/messages.js';

	const slug = $derived(page.params.workspaceSlug ?? '');
	const activeShares = $derived(sharesState.active);

	// The presenter's own share renders in present mode from the create
	// response with its captured stream; every other active share mounts a
	// viewer tile that negotiates its own peer connection.
	let starting = $state(false);
	let presenterJoin = $state<{ share: ScreenShare; stream: MediaStream } | null>(null);

	function presenterName(share: ScreenShare): string {
		return share.started_by_user?.display_name || share.started_by_user?.name || '';
	}

	// Capture FIRST, inside the click's user-activation window: Chrome
	// denies getDisplayMedia (usually silently — no picker at all) once
	// the gesture has expired behind the share-creation round-trip
	// (2026-09-17 Chrome-on-Linux report). Only after a surface is chosen
	// do we create the share.
	async function startShare() {
		if (starting) return;
		starting = true;
		let stream: MediaStream;
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
			stream = await navigator.mediaDevices.getDisplayMedia(captureOptions);
		} catch (err) {
			appToast.apiError(err, m['shares.capture_failed']());
			return;
		} finally {
			starting = false;
		}
		try {
			const joined = await createShare(slug, {});
			presenterJoin = { share: joined.share, stream };
			void sharesState.reload();
		} catch (err) {
			stream.getTracks().forEach((t) => t.stop());
			appToast.apiError(err, m['shares.start_failed']());
		}
	}

	function handlePresenterEnded() {
		presenterJoin = null;
		void sharesState.reload();
	}

	async function handlePresenterStop() {
		if (presenterJoin) {
			const { endShare } = await import('$lib/api/shares');
			await endShare(slug, presenterJoin.share.id);
		}
	}

	onMount(() => {
		void sharesState.load(slug);
	});
</script>

<svelte:head>
	<title>{m['shares.page_title']()} · {slug}</title>
</svelte:head>

<div class="flex h-full flex-col">
	<header class="flex items-center justify-between border-b border-[var(--color-border)] px-6 py-4">
		<div class="flex items-center gap-2">
			<MonitorPlay size={18} class="text-[var(--color-text-secondary)]" />
			<h1 class="text-base font-semibold">{m['shares.title']()}</h1>
		</div>
		{#if !presenterJoin}
			<Button onclick={startShare} disabled={starting}>
				{#if starting}
					<Loader2 size={14} class="animate-spin" />
				{:else}
					<MonitorUp size={14} />
				{/if}
				{m['shares.start']()}
			</Button>
		{/if}
	</header>

	<div class="flex-1 overflow-y-auto px-6 py-4">
		{#if activeShares.length === 0 && !presenterJoin}
			<EmptyState
				title={m['shares.empty_active_title']()}
				description={m['shares.empty_active_description']()}
			/>
		{:else}
			<div class="grid gap-4 md:grid-cols-1 xl:grid-cols-2 2xl:grid-cols-3">
				{#if presenterJoin}
					<div class="flex flex-col gap-2" data-share-id={presenterJoin.share.id}>
						<ShareTile
							shareId={presenterJoin.share.id}
							mode="present"
							presenterName={presenterName(presenterJoin.share)}
							presenterUserId={presenterJoin.share.started_by}
							sourceStream={presenterJoin.stream}
							onEnded={handlePresenterEnded}
							onPresenterStop={handlePresenterStop}
						/>
					</div>
				{/if}
				{#each activeShares as share (share.id)}
					{#if share.id !== presenterJoin?.share.id}
						<div class="flex flex-col gap-2" data-share-id={share.id}>
							<ShareTile
								shareId={share.id}
								mode="view"
								presenterName={presenterName(share)}
								presenterUserId={share.started_by}
							/>
						</div>
					{/if}
				{/each}
			</div>
		{/if}
	</div>
</div>
