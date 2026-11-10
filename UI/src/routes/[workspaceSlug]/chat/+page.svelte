<script lang="ts">
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { chatState } from '$lib/features/chat/chat.state.svelte';
	import CommunicationError from '$lib/features/communication/CommunicationError.svelte';
	import ChatChannelList from '$lib/features/chat/ChatChannelList.svelte';
	import LoadingState from '$lib/components/shared/LoadingState.svelte';
	import EmptyState from '$lib/components/shared/EmptyState.svelte';
	import { m } from '$lib/paraglide/messages.js';

	const slug = $derived(page.params.workspaceSlug ?? '');
	const ready = $derived(chatState.loadedFor === slug);

	// Return to the selected channel while it is still accessible. A fresh
	// workspace falls back to its most recent channel.
	$effect(() => {
		if (!slug || chatState.loadError) return;
		if (ready) {
			const first = chatState.channelById(chatState.activeChannelId) ?? chatState.channels[0];
			if (first) goto(`/${slug}/chat/${first.id}`, { replaceState: true });
			return;
		}
		void chatState.ensureWorkspace(slug);
	});
</script>

<div class="flex h-full">
	<div class="hidden w-60 shrink-0 2xl:block">
		{#if ready}<ChatChannelList {slug} activeChannelId={null} />{:else}<LoadingState />{/if}
	</div>
	<div class="flex min-w-0 flex-1 items-center justify-center">
		{#if chatState.loadError}
			<CommunicationError onretry={() => void chatState.ensureWorkspace(slug)} />
		{:else if ready && chatState.channels.length === 0}
			<EmptyState title={m['chat.empty_workspace_title']()} description={m['chat.empty_workspace_desc']()} />
		{:else}
			<LoadingState />
		{/if}
	</div>
</div>
