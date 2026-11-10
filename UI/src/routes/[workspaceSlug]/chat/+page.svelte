<script lang="ts">
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { chatState } from '$lib/features/chat/chat.state.svelte';
	import LoadingState from '$lib/components/shared/LoadingState.svelte';
	import EmptyState from '$lib/components/shared/EmptyState.svelte';
	import { m } from '$lib/paraglide/messages.js';

	const slug = $derived(page.params.workspaceSlug ?? '');
	const ready = $derived(chatState.loadedFor === slug);

	// The chat entry picks the most recent channel; the rail lives in the
	// [channelId] page. Redirect as soon as the channel list is available —
	// whether this page triggered the load or the workspace layout already
	// did.
	$effect(() => {
		if (!slug) return;
		if (ready) {
			const first = chatState.channels[0];
			if (first) goto(`/${slug}/chat/${first.id}`, { replaceState: true });
			return;
		}
		void chatState.ensureWorkspace(slug);
	});
</script>

<div class="flex h-full">
	<div class="hidden w-60 shrink-0 md:block">
		<LoadingState />
	</div>
	<div class="flex min-w-0 flex-1 items-center justify-center">
		{#if ready && chatState.channels.length === 0}
			<EmptyState title={m['chat.empty_workspace_title']()} description={m['chat.empty_workspace_desc']()} />
		{:else}
			<LoadingState />
		{/if}
	</div>
</div>
