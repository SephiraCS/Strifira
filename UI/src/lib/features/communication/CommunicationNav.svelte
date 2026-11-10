<script lang="ts">
	import { page } from '$app/state';
	import MessageSquare from '@lucide/svelte/icons/message-square';
	import MonitorPlay from '@lucide/svelte/icons/monitor-play';
	import { chatState } from '$lib/features/chat/chat.state.svelte';
	import { sharesState } from '$lib/features/shares/shares.state.svelte';
	import { m } from '$lib/paraglide/messages.js';

	let { slug, screenSharing }: { slug: string; screenSharing: boolean } = $props();
	const inChat = $derived(page.url.pathname.startsWith(`/${slug}/chat`));
</script>

<nav
	aria-label={m['communication.title']()}
	data-testid="communication-nav"
	class="flex shrink-0 flex-wrap items-center gap-x-4 gap-y-1 border-b border-[var(--app-border)] px-3 py-2"
>
	<span class="text-xs font-semibold text-[var(--color-text-tertiary)]">{m['communication.title']()}</span>
	<div class="flex min-w-0 flex-wrap gap-1">
		<a
			href="/{slug}/chat"
			aria-current={inChat ? 'page' : undefined}
			class="flex items-center gap-1.5 rounded-md px-2 py-1 text-xs {inChat
				? 'bg-[var(--color-bg-hover)] text-[var(--color-text-primary)]'
				: 'text-[var(--color-text-secondary)] hover:bg-[var(--color-bg-hover)]'}"
		>
			<MessageSquare size={14} />
			{m['communication.chat_voice']()}
			{#if chatState.loadedFor === slug && chatState.channelById(chatState.activeChannelId)}
				{@const channel = chatState.channelById(chatState.activeChannelId)!}
				<span class="max-w-28 truncate text-[var(--color-text-tertiary)]"
					>{channel.type === 'dm' ? channel.dm_peer?.display_name || channel.dm_peer?.name : `#${channel.name}`}</span
				>
			{/if}
		</a>
		{#if screenSharing}
			<a
				href="/{slug}/shares"
				aria-current={!inChat ? 'page' : undefined}
				class="flex items-center gap-1.5 rounded-md px-2 py-1 text-xs {!inChat
					? 'bg-[var(--color-bg-hover)] text-[var(--color-text-primary)]'
					: 'text-[var(--color-text-secondary)] hover:bg-[var(--color-bg-hover)]'}"
			>
				<MonitorPlay size={14} />
				{m['sidebar.screen_shares']()}
				{#if sharesState.active.length > 0}<span
						class="rounded-full bg-[var(--app-accent)] px-1.5 text-[var(--app-accent-foreground)]"
						>{sharesState.active.length}</span
					>{/if}
			</a>
		{/if}
	</div>
</nav>
