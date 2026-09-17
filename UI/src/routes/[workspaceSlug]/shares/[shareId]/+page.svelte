<script lang="ts">
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { authState } from '$lib/features/auth/auth.state.svelte';
	import { sharesState } from '$lib/features/shares/shares.state.svelte';
	import { joinShare, endShare } from '$lib/api/shares';
	import type { ScreenShare, ScreenShareJoin } from '$lib/types/share';
	import ShareStage from '$lib/features/shares/ShareStage.svelte';
	import { Button } from '$lib/components/ui/button';
	import { appToast } from '$lib/features/toast/toast';
	import { ArrowLeft, Loader2, MonitorPlay } from 'lucide-svelte';
	import { m } from '$lib/paraglide/messages.js';

	const slug = $derived(page.params.workspaceSlug ?? '');
	const shareId = $derived(page.params.shareId ?? '');

	let share = $state<ScreenShare | null>(null);
	let joinInfo = $state<ScreenShareJoin | null>(null);
	let loading = $state(true);
	let ended = $state(false);
	let error = $state<string | null>(null);

	// The backend mints the publish token exactly for the share owner, so
	// presenter-vs-viewer follows identity, never a URL parameter.
	const isPresenter = $derived(share != null && share.started_by === authState.user?.id);
	const presenterName = $derived(
		share?.started_by_user?.display_name || share?.started_by_user?.name || ''
	);

	async function load() {
		loading = true;
		error = null;
		try {
			// Present flow arrives right after createShare with a token in
			// hand via the present param — rejoin through POST /join, which
			// mints the right (presenter) token for the share owner.
			joinInfo = await joinShare(slug, shareId);
			share = joinInfo.share;
			if (share.status !== 'active') {
				ended = true;
			}
		} catch (err) {
			const code = (err as { error?: { code?: string } })?.error?.code;
			if (code === 'NOT_FOUND') {
				ended = true;
			} else {
				error = m['shares.join_failed']();
			}
		} finally {
			loading = false;
		}
	}

	function handleStageEnded() {
		ended = true;
		void sharesState.reload();
	}

	// Presenter stop ends the share server-side so every viewer gets the
	// share.ended WS event immediately instead of waiting for lazy GC.
	async function handlePresenterStop() {
		if (isPresenter) {
			await endShare(slug, shareId);
		}
	}

	function backToShares() {
		goto(`/${slug}/shares`);
	}

	// Follow workspace-wide share.ended events so every open viewer page
	// closes even without its own LiveKit disconnect racing first.
	$effect(() => {
		const handler = (e: Event) => {
			const detail = (e as CustomEvent<{ id?: string }>).detail;
			if (detail?.id === shareId) {
				ended = true;
			}
		};
		window.addEventListener('ws:share.ended', handler as EventListener);
		return () => window.removeEventListener('ws:share.ended', handler as EventListener);
	});

	onMount(() => {
		void sharesState.load(slug);
		void load();
	});
</script>

<svelte:head>
	<title>{share?.title || m['shares.page_title']()} · {slug}</title>
</svelte:head>

<div class="flex h-full flex-col">
	<header class="flex items-center gap-3 border-b border-[var(--color-border)] px-6 py-3">
		<Button variant="ghost" size="sm" onclick={backToShares}>
			<ArrowLeft size={15} />
		</Button>
		<div class="min-w-0 flex-1">
			<h1 class="truncate text-sm font-semibold">
				{share?.title || presenterName || m['shares.untitled']()}
			</h1>
			{#if presenterName}
				<p class="truncate text-xs text-[var(--color-text-secondary)]">{presenterName}</p>
			{/if}
		</div>
		{#if share && !ended}
			<span class="flex items-center gap-1.5 rounded-full bg-red-500/10 px-2.5 py-1 text-[10px] font-semibold uppercase text-red-500">
				<span class="h-1.5 w-1.5 animate-pulse rounded-full bg-red-500"></span>
				{m['shares.live_label']()}
			</span>
		{/if}
	</header>

	<div class="flex flex-1 items-center justify-center p-4">
		{#if loading}
			<div class="flex flex-col items-center gap-2 text-[var(--color-text-secondary)]">
				<Loader2 size={24} class="animate-spin" />
				<p class="text-sm">{m['shares.connecting_viewer']()}</p>
			</div>
		{:else if error}
			<div class="flex flex-col items-center gap-3">
				<p class="text-sm text-[var(--color-text-primary)]">{error}</p>
				<Button size="sm" onclick={load}>{m['shares.retry']()}</Button>
			</div>
		{:else if ended}
			<div class="flex flex-col items-center gap-3">
				<MonitorPlay size={32} class="text-[var(--color-text-tertiary)]" />
				<p class="text-sm font-medium">{m['shares.ended']()}</p>
				<Button size="sm" variant="outline" onclick={backToShares}>{m['shares.back']()}</Button>
			</div>
		{:else if joinInfo}
			<div class="h-full w-full max-w-6xl">
				<ShareStage
					serverUrl={joinInfo.server_url}
					token={joinInfo.token}
					mode={isPresenter ? 'present' : 'view'}
					onEnded={handleStageEnded}
					onPresenterStop={handlePresenterStop}
				/>
			</div>
		{/if}
	</div>
</div>
