<script lang="ts">
	import * as Dialog from '$lib/components/ui/dialog';
	import { Button } from '$lib/components/ui/button';
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';
	import { Checkbox } from '$lib/components/ui/checkbox';
	import { m } from '$lib/paraglide/messages.js';
	import { chatState } from './chat.state.svelte';

	let {
		open = $bindable(false),
		oncreated
	}: {
		open: boolean;
		oncreated: (channelId: string) => void;
	} = $props();

	let name = $state('');
	let isPrivate = $state(false);
	let submitting = $state(false);
	let error = $state('');

	$effect(() => {
		if (open) {
			name = '';
			isPrivate = false;
			error = '';
			submitting = false;
		}
	});

	async function handleSubmit(e: Event) {
		e.preventDefault();
		const slug = name.trim().toLowerCase();
		if (!slug || submitting) return;
		submitting = true;
		error = '';
		const channel = await chatState.createChannel(slug, isPrivate ? 'private' : 'public');
		submitting = false;
		if (!channel) {
			error = m['chat.create_channel_failed']();
			return;
		}
		open = false;
		oncreated(channel.id);
	}
</script>

<Dialog.Root bind:open>
	<Dialog.Content
		class="sm:max-w-[420px] border-[var(--app-border)] bg-[var(--color-bg-secondary)] p-0 overflow-hidden rounded-xl"
	>
		<form onsubmit={handleSubmit}>
			<div class="px-5 pt-5 pb-4 space-y-4">
				<div>
					<h2 class="text-base font-semibold text-[var(--color-text-primary)]">
						{m['chat.create_channel']()}
					</h2>
					<p class="mt-0.5 text-xs text-[var(--color-text-tertiary)]">
						{m['chat.create_channel_desc']()}
					</p>
				</div>

				<div class="space-y-1.5">
					<Label class="text-xs text-[var(--color-text-secondary)]" for="chat-channel-name">
						{m['chat.channel_name']()}
					</Label>
					<div class="flex items-center gap-1.5">
						<span class="text-sm text-[var(--color-text-tertiary)]">#</span>
						<Input
							id="chat-channel-name"
							bind:value={name}
							placeholder={m['chat.channel_name_placeholder']()}
							pattern="[a-z0-9]+(-[a-z0-9]+)*"
							class="bg-[var(--color-bg)] border-[var(--app-border)] text-[var(--color-text-primary)]"
							required
						/>
					</div>
				</div>

				<label class="flex items-center gap-2 text-sm text-[var(--color-text-secondary)]">
					<Checkbox bind:checked={isPrivate} />
					{m['chat.private_channel']()}
				</label>

				{#if error}
					<p class="text-xs text-[var(--color-error)]">{error}</p>
				{/if}
			</div>

			<div class="flex justify-end gap-2 border-t border-[var(--app-border)] px-5 py-3">
				<Button type="button" variant="ghost" size="sm" onclick={() => (open = false)}>
					{m['chat.cancel']()}
				</Button>
				<Button type="submit" size="sm" disabled={submitting || !name.trim()}>
					{m['chat.create']()}
				</Button>
			</div>
		</form>
	</Dialog.Content>
</Dialog.Root>
