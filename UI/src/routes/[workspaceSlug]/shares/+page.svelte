<script lang="ts">
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { authState } from '$lib/features/auth/auth.state.svelte';
	import { sharesState } from '$lib/features/shares/shares.state.svelte';
	import { createShare, joinShare, endShare } from '$lib/api/shares';
	import type { ScreenShare } from '$lib/types/share';
	import ShareTile from '$lib/features/shares/ShareTile.svelte';
	import { Button } from '$lib/components/ui/button';
	import EmptyState from '$lib/components/shared/EmptyState.svelte';
	import { appToast } from '$lib/features/toast/toast';
	import { MonitorUp, MonitorPlay, Loader2, History } from 'lucide-svelte';
	import { m } from '$lib/paraglide/messages.js';

	const slug = $derived(page.params.workspaceSlug ?? '');
	const activeShares = $derived(sharesState.active);
	const recentShares = $derived(sharesState.recent);

	// Per-share join state for viewer tiles: one LiveKit token each,
	// fetched once per share id. The presenter's own share renders in
	// present mode from the create response (idempotent on re-entry).
	let starting = $state(false);
	let presenterJoin = $state<{ share: ScreenShare; token: string; server_url: string } | null>(null);
	let viewerJoins = $state<Record<string, { token: string; server_url: string }>>({});

	function presenterName(share: ScreenShare): string {
		return share.started_by_user?.display_name || share.started_by_user?.name || '';
	}

	function initials(share: ScreenShare): string {
		const name = presenterName(share);
		return name
			.split(/\s+/)
			.map((part) => part[0])
			.filter(Boolean)
			.slice(0, 2)
			.join('')
			.toUpperCase();
	}

	function elapsed(share: ScreenShare): string {
		const seconds = Math.max(0, Math.floor((Date.now() - new Date(share.created_at).getTime()) / 1000));
		const h = Math.floor(seconds / 3600);
		const min = Math.floor((seconds % 3600) / 60);
		if (h > 0) return `${h}h ${min}m`;
		return `${min}m`;
	}

	function duration(share: ScreenShare): string {
		if (!share.ended_at) return '';
		const seconds = Math.max(
			0,
			Math.floor((new Date(share.ended_at).getTime() - new Date(share.created_at).getTime()) / 1000)
		);
		const h = Math.floor(seconds / 3600);
		const min = Math.floor((seconds % 3600) / 60);
		if (h > 0) return `${h}h ${min}m`;
		return `${min}m`;
	}

	async function startShare() {
		if (starting) return;
		starting = true;
		try {
			const joined = await createShare(slug, {});
			presenterJoin = joined;
			void sharesState.reload();
		} catch (err) {
			appToast.apiError(err, m['shares.start_failed']());
		} finally {
			starting = false;
		}
	}

	function handlePresenterEnded() {
		presenterJoin = null;
		void sharesState.reload();
	}

	async function handlePresenterStop() {
		if (presenterJoin) {
			await endShare(slug, presenterJoin.share.id);
		}
	}

	// Fetch viewer tokens for every active share that is not mine.
	async function refreshViewerJoins() {
		if (!slug) return;
		const mine = presenterJoin?.share.id;
		const wanted = activeShares.filter((s) => s.id !== mine);
		const next: Record<string, { token: string; server_url: string }> = {};
		await Promise.all(
			wanted.map(async (share) => {
				if (viewerJoins[share.id]) {
					next[share.id] = viewerJoins[share.id];
					return;
				}
				try {
					const joined = await joinShare(slug, share.id);
					next[share.id] = { token: joined.token, server_url: joined.server_url };
				} catch {
					// Share may have ended mid-fetch; the next WS event retries.
				}
			})
		);
		viewerJoins = next;
	}

	// Rejoin presenter session after a reload: shares I own that are still
	// active come back through joinShare (which mints the presenter token
	// for the owner) so a refresh never orphans my own share.
	async function resumePresenterShare() {
		if (presenterJoin || !slug) return;
		const mine = activeShares.find((s) => s.started_by === authState.user?.id);
		if (!mine) return;
		try {
			const joined = await joinShare(slug, mine.id);
			presenterJoin = joined;
		} catch {
			// Ended between list and join; ignore.
		}
	}

	$effect(() => {
		void activeShares;
		void refreshViewerJoins();
	});

	$effect(() => {
		const handler = () => {
			void sharesState.reload();
		};
		window.addEventListener('ws:share.started', handler as EventListener);
		window.addEventListener('ws:share.ended', handler as EventListener);
		return () => {
			window.removeEventListener('ws:share.started', handler as EventListener);
			window.removeEventListener('ws:share.ended', handler as EventListener);
		};
	});

	onMount(() => {
		void sharesState.load(slug).then(() => {
			void resumePresenterShare();
		});
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
		<section>
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
								serverUrl={presenterJoin.server_url}
								token={presenterJoin.token}
								mode="present"
								presenterName={presenterName(presenterJoin.share)}
								onEnded={handlePresenterEnded}
								onPresenterStop={handlePresenterStop}
							/>
						</div>
					{/if}
					{#each activeShares as share (share.id)}
						{#if share.id !== presenterJoin?.share.id}
							{#if viewerJoins[share.id]}
								<div class="flex flex-col gap-2" data-share-id={share.id}>
									<ShareTile
										serverUrl={viewerJoins[share.id].server_url}
										token={viewerJoins[share.id].token}
										mode="view"
										presenterName={presenterName(share)}
									/>
								</div>
							{:else}
								<div
									class="flex aspect-video w-full items-center justify-center rounded-lg border border-[var(--color-border)] bg-black/80 text-white"
									data-share-id={share.id}
								>
									<Loader2 size={20} class="animate-spin" />
								</div>
							{/if}
						{/if}
					{/each}
				</div>
			{/if}
		</section>

		{#if recentShares.length > 0}
			<section class="mt-8">
				<h2 class="mb-3 flex items-center gap-1.5 text-xs font-semibold uppercase tracking-wide text-[var(--color-text-secondary)]">
					<History size={13} />
					{m['shares.recent']()}
				</h2>
				<div class="divide-y divide-[var(--color-border)] rounded-lg border border-[var(--color-border)]">
					{#each recentShares as share (share.id)}
						<div class="flex items-center justify-between gap-3 px-4 py-2.5 text-sm">
							<span class="flex min-w-0 items-center gap-2.5">
								<span class="flex h-6 w-6 shrink-0 items-center justify-center rounded-full bg-[var(--app-accent)]/15 text-[10px] font-semibold text-[var(--app-accent)]">
									{initials(share)}
								</span>
								<span class="min-w-0 truncate">{presenterName(share)}</span>
							</span>
							<span class="shrink-0 text-xs text-[var(--color-text-secondary)]">
								{share.status === 'active' ? elapsed(share) : duration(share)}
							</span>
						</div>
					{/each}
				</div>
			</section>
		{/if}
	</div>
</div>
