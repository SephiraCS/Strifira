<script lang="ts">
	import * as Dialog from '$lib/components/ui/dialog';
	import { Button } from '$lib/components/ui/button';
	import Avatar from '$lib/components/shared/Avatar.svelte';
	import { m } from '$lib/paraglide/messages.js';
	import { authState } from '$lib/features/auth/auth.state.svelte';
	import { chatState } from './chat.state.svelte';

	let {
		open = $bindable(false),
		onselected
	}: {
		open: boolean;
		onselected: (channelId: string) => void;
	} = $props();

	let submittingId = $state('');

	$effect(() => {
		if (open) submittingId = '';
	});

	async function startDM(userId: string) {
		if (submittingId) return;
		submittingId = userId;
		const channelId = await chatState.startDM(userId);
		submittingId = '';
		if (channelId) {
			open = false;
			onselected(channelId);
		}
	}
</script>

<Dialog.Root bind:open>
	<Dialog.Content
		class="sm:max-w-[420px] border-[var(--app-border)] bg-[var(--color-bg-secondary)] p-0 overflow-hidden rounded-xl"
	>
		<div class="px-5 pt-5 pb-3">
			<h2 class="text-base font-semibold text-[var(--color-text-primary)]">
				{m['chat.new_dm']()}
			</h2>
			<p class="mt-0.5 text-xs text-[var(--color-text-tertiary)]">{m['chat.pick_member']()}</p>
		</div>

		<div class="max-h-72 space-y-0.5 overflow-y-auto px-2 pb-2">
			{#each chatState.members.filter((member) => member.user_id !== authState.user?.id) as member (member.user_id)}
				<button
					type="button"
					class="flex w-full items-center gap-2.5 rounded-md px-2.5 py-2 text-left hover:bg-[var(--color-bg-hover)] disabled:opacity-50"
					onclick={() => startDM(member.user_id)}
					disabled={!!submittingId}
				>
					<Avatar name={member.name} avatarUrl={member.avatar_url} size="sm" />
					<span class="min-w-0 flex-1">
						<span class="block truncate text-sm text-[var(--color-text-primary)]">
							{member.name}
						</span>
						<span class="block truncate text-xs text-[var(--color-text-tertiary)]">
							{member.email}
						</span>
					</span>
					<span
						class="h-2 w-2 shrink-0 rounded-full {chatState.isOnline(member.user_id)
							? 'bg-[var(--color-success)]'
							: 'bg-[var(--app-border)]'}"
					></span>
				</button>
			{/each}
		</div>

		<div class="flex justify-end border-t border-[var(--app-border)] px-5 py-3">
			<Button variant="ghost" size="sm" onclick={() => (open = false)}>
				{m['chat.cancel']()}
			</Button>
		</div>
	</Dialog.Content>
</Dialog.Root>
