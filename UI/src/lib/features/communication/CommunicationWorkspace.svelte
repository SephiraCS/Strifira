<script lang="ts">
	import type { Snippet } from 'svelte';
	import { page } from '$app/state';
	import ShareWall from '$lib/features/shares/ShareWall.svelte';
	import { sharesState } from '$lib/features/shares/shares.state.svelte';
	import { presenterSession } from '$lib/features/shares/presenter.session.svelte';
	import ChatChannelList from '$lib/features/chat/ChatChannelList.svelte';
	import * as Sheet from '$lib/components/ui/sheet';
	import List from '@lucide/svelte/icons/list';
	import { voiceState } from '$lib/features/chat/voice/voice.state.svelte';
	import MessagesSquare from '@lucide/svelte/icons/messages-square';
	import Headphones from '@lucide/svelte/icons/headphones';
	import MonitorPlay from '@lucide/svelte/icons/monitor-play';
	import { m } from '$lib/paraglide/messages.js';

	let { slug, screenSharing, children }: { slug: string; screenSharing: boolean; children: Snippet } = $props();
	const ownShare = $derived(presenterSession.share && presenterSession.belongsToWorkspace(slug) ? presenterSession.share : null);
	const screenCount = $derived(
		(sharesState.loadedFor === slug ? sharesState.active.filter((share) => share.id !== ownShare?.id).length : 0) +
			(ownShare ? 1 : 0)
	);
	let channelsOpen = $state(false);
	const inVoice = $derived(voiceState.connected && voiceState.session?.slug === slug);
</script>

<svelte:head><title>{m['communication.title']()} · {slug}</title></svelte:head>

<div data-testid="communication-workspace" class="flex h-full min-h-0 flex-col bg-[var(--color-bg)]">
	<header class="flex min-h-14 shrink-0 flex-wrap items-center justify-between gap-2 border-b border-[var(--app-border)] px-4 py-3">
		<h1 class="flex items-center gap-2 text-sm font-semibold">
			<MessagesSquare size={17} class="text-[var(--app-accent)]" />
			{m['communication.title']()}
		</h1>
		<div class="flex flex-wrap items-center gap-2 text-xs">
			<Sheet.Root bind:open={channelsOpen}>
				<Sheet.Trigger class="flex items-center gap-1.5 rounded-md border border-[var(--app-border)] px-2.5 py-1.5 hover:bg-[var(--color-bg-hover)] lg:hidden">
					<List size={14} />{m['chat.channels']()}
				</Sheet.Trigger>
				<Sheet.Content side="left" class="max-w-xs gap-0 p-0">
					<Sheet.Header class="sr-only"><Sheet.Title>{m['chat.channels']()}</Sheet.Title></Sheet.Header>
					<ChatChannelList {slug} activeChannelId={page.params.channelId ?? null} onselect={() => (channelsOpen = false)} />
				</Sheet.Content>
			</Sheet.Root>
			<div class="flex flex-wrap items-center gap-2" aria-live="polite">
				{#if inVoice}
					<span class="flex items-center gap-1.5 rounded-full bg-[var(--color-success)]/10 px-2.5 py-1 text-[var(--color-success)]">
						<Headphones size={13} />{m['voice.in_room']()}
					</span>
				{/if}
				{#if screenSharing && screenCount > 0}
					<span class="flex items-center gap-1.5 rounded-full bg-[var(--app-accent)]/10 px-2.5 py-1 text-[var(--app-accent)]">
						<MonitorPlay size={13} />{m['communication.live_screens']({ count: screenCount })}
					</span>
				{/if}
			</div>
		</div>
	</header>
	<div class="flex min-h-0 flex-1">
		<aside class="hidden w-48 shrink-0 lg:block" aria-label={m['chat.channels']()} data-testid="communication-channels">
			<ChatChannelList {slug} activeChannelId={page.params.channelId ?? null} />
		</aside>
		<div class="communication-body">
			<div class="communication-layout" class:with-screens={screenSharing && screenCount > 0} class:chat-only={!screenSharing}>
				{#if screenSharing}
					<section aria-label={m['shares.title']()} class="communication-screen-panel">
						<ShareWall {slug} variant="embedded" />
					</section>
				{/if}
				<section
					aria-label={m['sidebar.chat']()}
					data-testid="communication-conversation"
					class="min-h-0 min-w-0 overflow-hidden"
				>
					{@render children()}
				</section>
			</div>
		</div>
	</div>
</div>

<style>
	.communication-body {
		container: communication / inline-size;
		min-width: 0;
		min-height: 0;
		flex: 1;
		display: flex;
	}

	.communication-layout {
		display: grid;
		grid-template-rows: auto minmax(0, 1fr);
		flex: 1;
		min-width: 0;
		min-height: 0;
		overflow-y: auto;
	}

	.communication-layout.chat-only {
		grid-template-rows: minmax(0, 1fr);
	}

	.communication-screen-panel {
		min-width: 0;
		min-height: 0;
		border-bottom: 1px solid var(--app-border);
	}

	.communication-layout.with-screens {
		grid-template-rows: minmax(10rem, 36%) minmax(18rem, 1fr);
	}

	@container communication (min-width: 48rem) {
		.communication-layout.with-screens {
			grid-template-columns: minmax(0, 1.15fr) minmax(20rem, 1fr);
			grid-template-rows: minmax(0, 1fr);
		}

		.with-screens .communication-screen-panel {
			border-right: 1px solid var(--app-border);
			border-bottom: 0;
		}
	}
</style>
