<script lang="ts">
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { chatState } from '$lib/features/chat/chat.state.svelte';
	import { voiceState } from '$lib/features/chat/voice/voice.state.svelte';
	import ChatChannelList from '$lib/features/chat/ChatChannelList.svelte';
	import ChatMessages from '$lib/features/chat/ChatMessages.svelte';
	import ChatComposer from '$lib/features/chat/ChatComposer.svelte';
	import LoadingState from '$lib/components/shared/LoadingState.svelte';
	import Avatar from '$lib/components/shared/Avatar.svelte';
	import Hash from '@lucide/svelte/icons/hash';
	import Lock from '@lucide/svelte/icons/lock';
	import Headphones from '@lucide/svelte/icons/headphones';
	import { m } from '$lib/paraglide/messages.js';

	const slug = $derived(page.params.workspaceSlug ?? '');
	const channelId = $derived(page.params.channelId ?? '');
	const channel = $derived(chatState.channelById(channelId));
	const ready = $derived(chatState.loadedFor === slug);

	// Keep the state aligned with the workspace and open the routed channel
	// (marks it read, loads its timeline).
	$effect(() => {
		if (!slug || !channelId) return;
		void chatState.ensureWorkspace(slug);
		// The header shows the live voice count even on mobile (the rail is
		// hidden there), so the rosters load with the page too.
		void voiceState.ensureRooms();
		void chatState.ensureWorkspace(slug).then(() => {
			if (chatState.loadedFor !== slug) return;
			if (!chatState.channelById(channelId)) {
				// Unknown channel (fresh workspace, stale link): fall back to the
				// most recent one.
				const first = chatState.channels[0];
				if (first && first.id !== channelId) {
					goto(`/${slug}/chat/${first.id}`, { replaceState: true });
				}
				return;
			}
			void chatState.openChannel(channelId);
		});
	});

	function channelTitle(): string {
		if (!channel) return '';
		if (channel.type === 'dm') {
			return channel.dm_peer?.display_name || channel.dm_peer?.name || '—';
		}
		return `#${channel.name}`;
	}

	// Mobile channel switching: the rail is hidden on small screens, so the
	// header carries a compact select.
	function handleMobileSwitch(e: Event) {
		const value = (e.currentTarget as HTMLSelectElement).value;
		if (value) goto(`/${slug}/chat/${value}`);
	}

	function presenceTitle(): string {
		if (!channel || channel.type !== 'dm' || !channel.dm_peer) return '';
		return chatState.isOnline(channel.dm_peer.id)
			? m['chat.online']()
			: m['chat.offline']();
	}

	// Voice room of this channel: join from here, or see how many are in.
	const voiceCount = $derived(channel ? voiceState.roomActivity(channel.id) : 0);
	const inThisRoom = $derived(channel ? voiceState.session?.channelId === channel.id : false);

	async function handleVoiceButton() {
		if (!channel) return;
		if (inThisRoom) return;
		await voiceState.join(channel.id);
	}
</script>

<div class="flex h-full min-h-0">
	<div class="hidden w-60 shrink-0 md:block">
		<ChatChannelList {slug} activeChannelId={channelId} />
	</div>

	<div class="flex min-w-0 flex-1 flex-col">
		{#if !ready || !channel}
			<div class="flex h-full items-center justify-center">
				<LoadingState />
			</div>
		{:else}
			<div
				class="flex h-12 shrink-0 items-center gap-2 border-b border-[var(--app-border)] px-4"
			>
				<span class="flex items-center gap-1.5 text-sm font-semibold text-[var(--color-text-primary)]">
					{#if channel.type === 'private'}
						<Lock size={14} class="text-[var(--color-text-tertiary)]" />
					{:else if channel.type !== 'dm'}
						<Hash size={15} class="text-[var(--color-text-tertiary)]" />
					{/if}
					{channelTitle()}
				</span>
				{#if channel.type === 'dm' && channel.dm_peer}
					<span class="flex items-center gap-1.5 text-xs text-[var(--color-text-tertiary)]">
						<Avatar
							name={channelTitle()}
							avatarUrl={channel.dm_peer.avatar_url}
							size="2xs"
						/>
						<span
							class="h-2 w-2 rounded-full {chatState.isOnline(channel.dm_peer.id)
								? 'bg-[var(--color-success)]'
								: 'bg-[var(--app-border)]'}"
							title={presenceTitle()}
						></span>
						{presenceTitle()}
					</span>
				{:else if channel.topic}
					<span class="min-w-0 truncate text-xs text-[var(--color-text-tertiary)]">
						{channel.topic}
					</span>
				{/if}

				<!-- Voice room: live participant count and the join control. -->
				{#if !inThisRoom}
					<button
						type="button"
						class="ml-auto flex shrink-0 items-center gap-1.5 rounded-md border border-[var(--app-border)] px-2 py-1 text-xs text-[var(--color-text-secondary)] hover:bg-[var(--color-bg-hover)] hover:text-[var(--color-text-primary)] md:ml-2"
						data-testid="voice-join"
						onclick={handleVoiceButton}
					>
						<Headphones size={13} />
						{#if voiceCount > 0}
							{m['voice.join_count']({ count: voiceCount })}
						{:else}
							{m['voice.join']()}
						{/if}
					</button>
				{:else}
					<span
						class="ml-auto flex shrink-0 items-center gap-1.5 rounded-md border border-[var(--color-success)]/40 px-2 py-1 text-xs text-[var(--color-success)] md:ml-2"
						data-testid="voice-connected-badge"
					>
						<Headphones size={13} />
						{m['voice.in_room']()}
					</span>
				{/if}

				<!-- Mobile-only channel switcher (the rail is md+) -->
				<select
					class="rounded-md border border-[var(--app-border)] bg-[var(--color-bg)] px-2 py-1 text-xs text-[var(--color-text-primary)] md:hidden {voiceCount > 0 || inThisRoom ? '' : 'ml-auto'}"
					value={channelId}
					onchange={handleMobileSwitch}
					aria-label={m['chat.switch_channel']()}
				>
					{#each chatState.channels as ch (ch.id)}
						<option value={ch.id}>
							{ch.type === 'dm'
								? (ch.dm_peer?.display_name || ch.dm_peer?.name || '—')
								: `#${ch.name}`}
						</option>
					{/each}
				</select>
			</div>

			<ChatMessages {channel} />
			<ChatComposer {channel} />
		{/if}
	</div>
</div>
