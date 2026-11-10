<script lang="ts">
	import SendHorizontal from '@lucide/svelte/icons/send-horizontal';
	import { m } from '$lib/paraglide/messages.js';
	import { chatState } from './chat.state.svelte';
	import type { ChatChannel } from '$lib/types/chat';

	let { channel }: { channel: ChatChannel } = $props();

	let body = $state('');
	let sending = $state(false);

	function channelLabel(): string {
		if (channel.type === 'dm') {
			return channel.dm_peer?.display_name || channel.dm_peer?.name || '—';
		}
		return `#${channel.name}`;
	}

	async function submit() {
		const text = body;
		if (!text.trim() || sending) return;
		sending = true;
		body = '';
		const ok = await chatState.send(channel.id, text);
		sending = false;
		if (!ok) {
			// Restore the draft so nothing the user typed is lost.
			body = text;
		}
	}

	function handleKeydown(e: KeyboardEvent) {
		if (e.key === 'Enter' && !e.shiftKey) {
			e.preventDefault();
			void submit();
		}
	}

	function handleInput() {
		chatState.sendTyping(channel.id);
	}
</script>

<div class="shrink-0 border-t border-[var(--app-border)] px-4 py-3">
	<div
		class="flex items-end gap-2 rounded-lg border border-[var(--app-border)] bg-[var(--color-bg)] px-3 py-2 focus-within:border-[var(--app-accent)]"
	>
		<textarea
			bind:value={body}
			rows="1"
			placeholder={m['chat.message_placeholder']({ name: channelLabel() })}
			aria-label={m['chat.message_placeholder']({ name: channelLabel() })}
			class="max-h-40 min-h-[1.5rem] w-full flex-1 resize-none bg-transparent text-sm text-[var(--color-text-primary)] placeholder:text-[var(--color-text-tertiary)] focus:outline-none"
			onkeydown={handleKeydown}
			oninput={handleInput}
		></textarea>
		<button
			type="button"
			class="flex h-7 w-7 shrink-0 items-center justify-center rounded-md text-[var(--color-text-secondary)] transition-colors hover:bg-[var(--color-bg-hover)] hover:text-[var(--color-text-primary)] disabled:opacity-40"
			title={m['chat.send']()}
			aria-label={m['chat.send']()}
			onclick={() => void submit()}
			disabled={sending || !body.trim()}
		>
			<SendHorizontal size={16} />
		</button>
	</div>
	<p class="mt-1 text-[10px] text-[var(--color-text-tertiary)]">{m['chat.send_hint']()}</p>
</div>
