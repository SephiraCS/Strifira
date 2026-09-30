<script lang="ts">
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { sharesState } from '$lib/features/shares/shares.state.svelte';
	import { presenterSession } from '$lib/features/shares/presenter.session.svelte';
	import ShareTile from '$lib/features/shares/ShareTile.svelte';
	import { Button } from '$lib/components/ui/button';
	import EmptyState from '$lib/components/shared/EmptyState.svelte';
	import MonitorUp from '@lucide/svelte/icons/monitor-up';
	import MonitorPlay from '@lucide/svelte/icons/monitor-play';
	import Loader2 from '@lucide/svelte/icons/loader-circle';
	import { m } from '$lib/paraglide/messages.js';

	const slug = $derived(page.params.workspaceSlug ?? '');
	const activeShares = $derived(sharesState.active);

	// The wall fills the viewport: one share takes the whole page, two split
	// it, more arrange in the squarest grid that fits (columns capped so
	// tiles stay watchable; extra rows scroll).
	let compact = $state(false);
	onMount(() => {
		const mq = window.matchMedia('(max-width: 767px)');
		const update = () => (compact = mq.matches);
		update();
		mq.addEventListener('change', update);
		return () => mq.removeEventListener('change', update);
	});

	// The presenter's share is the persistent session store — it survives
	// navigation to issues and back; the wall only renders its view. Every
	// other active share mounts a viewer tile that negotiates its own peer
	// connection.
	const tileCount = $derived(
		activeShares.length +
			(presenterSession.share && !activeShares.some((s) => s.id === presenterSession.share?.id) ? 1 : 0)
	);
	const gridCols = $derived.by(() => {
		if (compact || tileCount <= 1) return 1;
		if (tileCount === 2) return 2;
		return Math.min(4, Math.ceil(Math.sqrt(tileCount)));
	});
	const gridRows = $derived(Math.ceil(tileCount / gridCols));

	function presenterName(share: NonNullable<typeof presenterSession.share>): string {
		return share.started_by_user?.display_name || share.started_by_user?.name || '';
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
		{#if !presenterSession.share}
			<Button onclick={() => void presenterSession.start(slug)} disabled={presenterSession.starting}>
				{#if presenterSession.starting}
					<Loader2 size={14} class="animate-spin" />
				{:else}
					<MonitorUp size={14} />
				{/if}
				{m['shares.start']()}
			</Button>
		{/if}
	</header>

	<div class="min-h-0 flex-1 overflow-y-auto px-6 py-4">
		{#if activeShares.length === 0 && !presenterSession.share}
			<EmptyState
				title={m['shares.empty_active_title']()}
				description={m['shares.empty_active_description']()}
			/>
		{:else}
			<div
				class="grid h-full min-h-0 gap-4"
				style="grid-template-columns: repeat({gridCols}, minmax(0, 1fr)); grid-template-rows: repeat({gridRows}, minmax(0, 1fr));"
				data-tile-count={tileCount}
			>
				{#if presenterSession.share}
					<div class="min-h-0 min-w-0" data-share-id={presenterSession.share.id}>
						<ShareTile
							shareId={presenterSession.share.id}
							mode="present"
							presenterName={presenterName(presenterSession.share)}
						/>
					</div>
				{/if}
				{#each activeShares as share (share.id)}
					{#if share.id !== presenterSession.share?.id}
						<div class="min-h-0 min-w-0" data-share-id={share.id}>
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
