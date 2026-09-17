<script lang="ts">
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { authState } from '$lib/features/auth/auth.state.svelte';
	import { sharesState } from '$lib/features/shares/shares.state.svelte';
	import { createShare } from '$lib/api/shares';
	import type { ScreenShare } from '$lib/types/share';
	import { Button } from '$lib/components/ui/button';
	import EmptyState from '$lib/components/shared/EmptyState.svelte';
	import { appToast } from '$lib/features/toast/toast';
	import { MonitorUp, MonitorPlay, Loader2, Radio, History } from 'lucide-svelte';
	import { m } from '$lib/paraglide/messages.js';

	const slug = $derived(page.params.workspaceSlug ?? '');
	const activeShares = $derived(sharesState.active);
	const recentShares = $derived(sharesState.recent);

	let starting = $state(false);
	let titleDialogOpen = $state(false);
	let pendingTitle = $state('');

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
			const joined = await createShare(slug, pendingTitle.trim() ? { title: pendingTitle.trim() } : {});
			titleDialogOpen = false;
			pendingTitle = '';
			goto(`/${slug}/shares/${joined.share.id}`);
		} catch (err) {
			appToast.apiError(err, m['shares.start_failed']());
		} finally {
			starting = false;
		}
	}

	function joinShare(share: ScreenShare) {
		goto(`/${slug}/shares/${share.id}`);
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
		<Button onclick={() => (titleDialogOpen = true)} disabled={starting}>
			{#if starting}
				<Loader2 size={14} class="animate-spin" />
			{:else}
				<MonitorUp size={14} />
			{/if}
			{m['shares.start']()}
		</Button>
	</header>

	<div class="flex-1 overflow-y-auto px-6 py-4">
		<section>
			<h2 class="mb-3 flex items-center gap-1.5 text-xs font-semibold uppercase tracking-wide text-[var(--color-text-secondary)]">
				<Radio size={13} />
				{m['shares.active_now']()}
			</h2>
			{#if activeShares.length === 0}
				<EmptyState
					title={m['shares.empty_active_title']()}
					description={m['shares.empty_active_description']()}
				/>
			{:else}
				<div class="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
					{#each activeShares as share (share.id)}
						<button
							class="group flex flex-col gap-3 rounded-lg border border-[var(--color-border)] bg-[var(--color-bg)] p-4 text-left transition-colors hover:border-[var(--app-accent)]"
							onclick={() => joinShare(share)}
						>
							<div class="flex items-start justify-between gap-2">
								<div class="flex min-w-0 items-center gap-2.5">
									<span class="flex h-8 w-8 shrink-0 items-center justify-center rounded-full bg-[var(--app-accent)]/15 text-xs font-semibold text-[var(--app-accent)]">
										{initials(share)}
									</span>
									<div class="min-w-0">
										<p class="truncate text-sm font-medium">
											{share.title || presenterName(share) || m['shares.untitled']()}
										</p>
										<p class="truncate text-xs text-[var(--color-text-secondary)]">{presenterName(share)}</p>
									</div>
								</div>
								<span class="flex shrink-0 items-center gap-1 rounded-full bg-red-500/10 px-2 py-0.5 text-[10px] font-semibold uppercase text-red-500">
									<span class="h-1.5 w-1.5 animate-pulse rounded-full bg-red-500"></span>
									{m['shares.live_label']()}
								</span>
							</div>
							<div class="flex items-center justify-between text-xs text-[var(--color-text-secondary)]">
								<span>{elapsed(share)}</span>
								<span>{m['shares.viewer_count']({ count: share.viewer_count })}</span>
							</div>
							<span class="text-xs font-medium text-[var(--app-accent)] opacity-0 transition-opacity group-hover:opacity-100">
								{m['shares.join']()} →
							</span>
						</button>
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
							<span class="min-w-0 truncate">{share.title || presenterName(share) || m['shares.untitled']()}</span>
							<span class="shrink-0 text-xs text-[var(--color-text-secondary)]">
								{presenterName(share)} · {duration(share)}
							</span>
						</div>
					{/each}
				</div>
			</section>
		{/if}
	</div>
</div>

{#if titleDialogOpen}
	<div class="fixed inset-0 z-50 flex items-center justify-center bg-black/50" role="presentation">
		<div class="w-full max-w-sm rounded-lg border border-[var(--color-border)] bg-[var(--color-bg)] p-5 shadow-xl">
			<h3 class="mb-1 text-sm font-semibold">{m['shares.start']()}</h3>
			<p class="mb-4 text-xs text-[var(--color-text-secondary)]">{m['shares.title_hint']()}</p>
			<input
				type="text"
				class="mb-4 w-full rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] px-3 py-2 text-sm outline-none focus:border-[var(--app-accent)]"
				placeholder={m['shares.title_placeholder']()}
				bind:value={pendingTitle}
				maxlength={200}
				onkeydown={(e) => {
					if (e.key === 'Enter') startShare();
					if (e.key === 'Escape') titleDialogOpen = false;
				}}
			/>
			<div class="flex justify-end gap-2">
				<Button variant="outline" size="sm" onclick={() => (titleDialogOpen = false)}>
					{m['shares.cancel']()}
				</Button>
				<Button size="sm" onclick={startShare} disabled={starting}>
					{#if starting}
						<Loader2 size={13} class="animate-spin" />
					{/if}
					<MonitorUp size={13} />
					{m['shares.start_confirm']()}
				</Button>
			</div>
		</div>
	</div>
{/if}
