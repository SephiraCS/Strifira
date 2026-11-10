<script lang="ts">
	import { goto } from '$app/navigation';
	import Hash from '@lucide/svelte/icons/hash';
	import Lock from '@lucide/svelte/icons/lock';
	import Plus from '@lucide/svelte/icons/plus';
	import MessageSquare from '@lucide/svelte/icons/message-square';
	import Waveform from '@lucide/svelte/icons/audio-waveform';
	import Avatar from '$lib/components/shared/Avatar.svelte';
	import { m } from '$lib/paraglide/messages.js';
	import { chatState } from './chat.state.svelte';
	import { voiceState } from './voice/voice.state.svelte';
	import CreateChannelDialog from './CreateChannelDialog.svelte';
	import NewDMDialog from './NewDMDialog.svelte';
	import type { ChatChannel } from '$lib/types/chat';

	let {
		slug,
		activeChannelId
	}: {
		slug: string;
		activeChannelId: string | null;
	} = $props();

	let showCreateChannel = $state(false);
	let showNewDM = $state(false);

	const channels = $derived(chatState.channels.filter((c) => c.type !== 'dm'));
	const dms = $derived(chatState.channels.filter((c) => c.type === 'dm'));

	function channelHref(channel: ChatChannel): string {
		return `/${slug}/chat/${channel.id}`;
	}

	function displayName(channel: ChatChannel): string {
		if (channel.type === 'dm') {
			return channel.dm_peer?.display_name || channel.dm_peer?.name || '—';
		}
		return channel.name;
	}

	function voiceCount(channelId: string): number {
		return voiceState.roomActivity(channelId);
	}
</script>

<div class="flex h-full flex-col border-r border-[var(--app-border)] bg-[var(--color-bg-secondary)]">
	<div class="flex h-12 shrink-0 items-center justify-between border-b border-[var(--app-border)] px-3">
		<span class="flex items-center gap-2 text-sm font-semibold text-[var(--color-text-primary)]">
			<MessageSquare size={16} />
			{m['sidebar.chat']()}
		</span>
	</div>

	<div class="flex-1 overflow-y-auto px-2 py-2">
		<!-- Channels -->
		<div class="flex items-center justify-between px-1.5 pb-1 pt-2">
			<span class="text-[11px] font-semibold uppercase tracking-wide text-[var(--color-text-tertiary)]">
				{m['chat.channels']()}
			</span>
			<button
				type="button"
				class="rounded p-0.5 text-[var(--color-text-tertiary)] hover:bg-[var(--color-bg-hover)] hover:text-[var(--color-text-primary)]"
				title={m['chat.create_channel']()}
				onclick={() => (showCreateChannel = true)}
			>
				<Plus size={14} />
			</button>
		</div>
		<div class="space-y-px">
			{#each channels as channel (channel.id)}
				<a
					href={channelHref(channel)}
					class="group flex items-center gap-1.5 rounded-md px-1.5 py-1 text-sm {channel.id === activeChannelId
						? 'bg-[var(--color-bg-hover)]/60 text-[var(--color-text-primary)]'
						: 'text-[var(--color-text-secondary)] hover:bg-[var(--color-bg-hover)] hover:text-[var(--color-text-primary)]'}"
				>
					{#if channel.type === 'private'}
						<Lock size={13} class="shrink-0 text-[var(--color-text-tertiary)]" />
					{:else}
						<Hash size={14} class="shrink-0 text-[var(--color-text-tertiary)]" />
					{/if}
					<span class="min-w-0 flex-1 truncate {chatState.unread.get(channel.id)
						? 'font-semibold text-[var(--color-text-primary)]'
						: ''}">
						{displayName(channel)}
					</span>
					{#if chatState.unread.get(channel.id)}
						<span
							class="flex h-4 min-w-4 shrink-0 items-center justify-center rounded-full bg-[var(--app-accent)] px-1 text-[10px] font-medium text-[var(--app-accent-foreground)]"
						>
							{Math.min(chatState.unread.get(channel.id) ?? 0, 99)}
						</span>
					{/if}
					{#if voiceCount(channel.id) > 0}
						<span
							class="flex shrink-0 items-center gap-0.5 rounded-full bg-[var(--color-success)]/15 px-1.5 py-0.5 text-[10px] font-medium text-[var(--color-success)]"
							title={m['voice.rail_count']({ count: voiceCount(channel.id) })}
							data-testid="channel-voice-count"
						>
							<Waveform size={10} />
							{voiceCount(channel.id)}
						</span>
					{/if}
				</a>
			{/each}
		</div>

		<!-- Direct messages -->
		<div class="flex items-center justify-between px-1.5 pb-1 pt-3">
			<span class="text-[11px] font-semibold uppercase tracking-wide text-[var(--color-text-tertiary)]">
				{m['chat.direct_messages']()}
			</span>
			<button
				type="button"
				class="rounded p-0.5 text-[var(--color-text-tertiary)] hover:bg-[var(--color-bg-hover)] hover:text-[var(--color-text-primary)]"
				title={m['chat.new_dm']()}
				onclick={() => (showNewDM = true)}
			>
				<Plus size={14} />
			</button>
		</div>
		<div class="space-y-px">
			{#each dms as channel (channel.id)}
				{@const peer = channel.dm_peer}
				<a
					href={channelHref(channel)}
					class="flex items-center gap-2 rounded-md px-1.5 py-1 text-sm {channel.id === activeChannelId
						? 'bg-[var(--color-bg-hover)]/60 text-[var(--color-text-primary)]'
						: 'text-[var(--color-text-secondary)] hover:bg-[var(--color-bg-hover)] hover:text-[var(--color-text-primary)]'}"
				>
					<span class="relative shrink-0">
						<Avatar name={displayName(channel)} avatarUrl={peer?.avatar_url} size="2xs" />
						<span
							class="absolute -right-0.5 -bottom-0.5 h-2 w-2 rounded-full ring-1 ring-[var(--color-bg-secondary)] {peer &&
							chatState.isOnline(peer.id)
								? 'bg-[var(--color-success)]'
								: 'bg-[var(--app-border)]'}"
						></span>
					</span>
					<span class="min-w-0 flex-1 truncate {chatState.unread.get(channel.id)
						? 'font-semibold text-[var(--color-text-primary)]'
						: ''}">
						{displayName(channel)}
					</span>
					{#if chatState.unread.get(channel.id)}
						<span
							class="flex h-4 min-w-4 shrink-0 items-center justify-center rounded-full bg-[var(--app-accent)] px-1 text-[10px] font-medium text-[var(--app-accent-foreground)]"
						>
							{Math.min(chatState.unread.get(channel.id) ?? 0, 99)}
						</span>
					{/if}
				</a>
			{:else}
				<p class="px-1.5 py-1 text-xs text-[var(--color-text-tertiary)]">
					{m['chat.no_dms']()}
				</p>
			{/each}
		</div>
	</div>

	<CreateChannelDialog
		bind:open={showCreateChannel}
		oncreated={(channelId) => {
			void chatState.openChannel(channelId);
			void goto(`/${slug}/chat/${channelId}`);
		}}
	/>
	<NewDMDialog
		bind:open={showNewDM}
		onselected={(channelId) => {
			void chatState.openChannel(channelId);
			void goto(`/${slug}/chat/${channelId}`);
		}}
	/>
</div>
