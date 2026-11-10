<script lang="ts">
	import { tick, type Snippet } from 'svelte';
	import { page } from '$app/state';
	import ShareWall from '$lib/features/shares/ShareWall.svelte';
	import { sharesState } from '$lib/features/shares/shares.state.svelte';
	import { presenterSession } from '$lib/features/shares/presenter.session.svelte';
	import ChatChannelList from '$lib/features/chat/ChatChannelList.svelte';
	import { chatState } from '$lib/features/chat/chat.state.svelte';
	import * as Dialog from '$lib/components/ui/dialog';
	import X from '@lucide/svelte/icons/x';
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
	const presenting = $derived(screenSharing && screenCount > 0);
	let channelsOpen = $state(false);
	let chatOpen = $state(false);
	let chatDialogEl = $state<HTMLDivElement | null>(null);
	const selectedChannel = $derived(chatState.channelById(page.params.channelId ?? chatState.activeChannelId ?? ''));
	const channelLabel = $derived(selectedChannel?.type === 'dm'
		? (selectedChannel.dm_peer?.display_name || selectedChannel.dm_peer?.name || '')
		: `#${selectedChannel?.name ?? ''}`);
	const draft = $derived(selectedChannel ? chatState.drafts.get(selectedChannel.id) ?? '' : '');

	$effect(() => {
		if (!presenting) chatOpen = false;
	});

	async function focusComposer() {
		await tick();
		chatDialogEl?.querySelector<HTMLTextAreaElement>('textarea')?.focus();
	}
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
				<Sheet.Trigger class="flex items-center gap-1.5 rounded-md border border-[var(--app-border)] px-2.5 py-1.5 hover:bg-[var(--color-bg-hover)] {presenting ? '' : 'lg:hidden'}">
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
		<aside class="w-48 shrink-0 {presenting ? 'hidden' : 'hidden lg:block'}" aria-label={m['chat.channels']()} data-testid="communication-channels">
			<ChatChannelList {slug} activeChannelId={page.params.channelId ?? null} />
		</aside>
		<div class="communication-body">
			<div class="communication-layout" class:with-screens={presenting} class:chat-only={!screenSharing}>
				{#if screenSharing}
					<section aria-label={m['shares.title']()} class="communication-screen-panel">
						<ShareWall {slug} variant="embedded" />
					</section>
				{/if}
				{#if presenting}
					<Dialog.Root bind:open={chatOpen}>
						<div class="communication-chat-launcher shrink-0 border-b border-[var(--app-border)] px-4 py-3">
							<Dialog.Trigger
								data-testid="communication-chat-trigger"
								aria-label={m['communication.open_chat']()}
								class="flex min-h-11 w-full items-center gap-3 rounded-lg border border-[var(--app-border)] bg-[var(--color-bg)] px-3 py-2 text-left text-sm text-[var(--color-text-tertiary)] hover:border-[var(--app-accent)] focus-visible:outline-2 focus-visible:outline-[var(--app-accent)]"
							>
								<span class="min-w-0 flex-1 truncate">{draft || m['chat.message_placeholder']({ name: channelLabel })}</span>
								<MessagesSquare size={17} class="shrink-0" />
							</Dialog.Trigger>
						</div>
						<Dialog.Content
							bind:ref={chatDialogEl}
							data-testid="communication-chat-dialog"
							class="top-1/2 flex h-[calc(100dvh-2rem)] max-h-[48rem] -translate-y-1/2 flex-col gap-0 p-0 sm:max-w-4xl"
							showCloseButton={false}
							aria-describedby={undefined}
							onOpenAutoFocus={() => void focusComposer()}
						>
							<div class="flex h-12 shrink-0 items-center justify-between border-b border-[var(--app-border)] px-4">
								<Dialog.Title class="flex items-center gap-2 text-sm font-semibold"><MessagesSquare size={16} />{m['sidebar.chat']()}</Dialog.Title>
								<Dialog.Close class="flex h-8 w-8 items-center justify-center rounded-md hover:bg-[var(--color-bg-hover)] focus-visible:outline-2 focus-visible:outline-[var(--app-accent)]" aria-label={m['common.close']()}><X size={17} /></Dialog.Close>
							</div>
							<section aria-label={m['sidebar.chat']()} data-testid="communication-conversation" class="min-h-0 min-w-0 flex-1 overflow-hidden">
								{@render children()}
							</section>
						</Dialog.Content>
					</Dialog.Root>
				{:else}
					<section aria-label={m['sidebar.chat']()} data-testid="communication-conversation" class="communication-inline-chat min-h-0 min-w-0 overflow-hidden">
						{@render children()}
					</section>
				{/if}
			</div>
		</div>
	</div>
</div>

<style>
	.communication-body {
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
		grid-template-rows: auto minmax(0, 1fr);
	}

	.communication-chat-launcher {
		grid-row: 1;
	}

	.with-screens .communication-screen-panel {
		grid-row: 2;
		border-bottom: 0;
	}

	@media (min-width: 64rem) {
		.communication-inline-chat :global(.communication-channel-switcher) {
			display: none;
		}
	}
</style>
