<script lang="ts">
	import { onMount } from 'svelte';
	import Avatar from '$lib/components/shared/Avatar.svelte';
	import ConfirmDialog from '$lib/components/shared/ConfirmDialog.svelte';
	import Pencil from '@lucide/svelte/icons/pencil';
	import Trash2 from '@lucide/svelte/icons/trash-2';
	import LoaderCircle from '@lucide/svelte/icons/loader-circle';
	import { m } from '$lib/paraglide/messages.js';
	import { authState } from '$lib/features/auth/auth.state.svelte';
	import { chatState, MESSAGE_GROUP_WINDOW_MS } from './chat.state.svelte';
	import type { ChatChannel, ChatMessage } from '$lib/types/chat';

	let { channel }: { channel: ChatChannel } = $props();

	let scrollHost: HTMLElement | null = null;
	let stickToBottom = $state(true);
	let editingId = $state<string | null>(null);
	let editingBody = $state('');
	let pendingDelete = $state<ChatMessage | null>(null);
	let deleting = $state(false);

	const messages = $derived(chatState.messages.get(channel.id) ?? []);
	const hasMore = $derived(chatState.hasMore.get(channel.id) ?? false);
	let loadingOlder = $state(false);

	// Keep the view pinned to the newest message while the reader is at the
	// bottom; scrolling up (history reading) disables the stickiness.
	$effect(() => {
		void messages.length;
		if (stickToBottom) scrollToBottom();
	});

	onMount(() => {
		scrollToBottom();
	});

	function scrollToBottom() {
		if (scrollHost) scrollHost.scrollTop = scrollHost.scrollHeight;
	}

	function handleScroll() {
		if (!scrollHost) return;
		const distance = scrollHost.scrollHeight - scrollHost.scrollTop - scrollHost.clientHeight;
		stickToBottom = distance < 80;
	}

	async function loadOlder() {
		if (loadingOlder || !scrollHost) return;
		loadingOlder = true;
		const previousHeight = scrollHost.scrollHeight;
		const prepended = await chatState.loadOlder(channel.id);
		loadingOlder = false;
		if (prepended) {
			// Keep the viewport anchored on the message the reader was on.
			requestAnimationFrame(() => {
				if (scrollHost) scrollHost.scrollTop = scrollHost.scrollHeight - previousHeight;
			});
		}
	}

	function authorName(message: ChatMessage): string {
		return message.author?.display_name || message.author?.name || '—';
	}

	function isOwn(message: ChatMessage): boolean {
		return message.user_id === authState.user?.id;
	}

	function sameGroup(previous: ChatMessage | undefined, message: ChatMessage): boolean {
		if (!previous) return false;
		if (previous.user_id !== message.user_id) return false;
		const gap =
			new Date(message.created_at).getTime() - new Date(previous.created_at).getTime();
		return gap < MESSAGE_GROUP_WINDOW_MS;
	}

	function dayLabel(message: ChatMessage): string | null {
		const day = new Date(message.created_at);
		const today = new Date();
		const yesterday = new Date(today);
		yesterday.setDate(today.getDate() - 1);
		const sameDay = (a: Date, b: Date) =>
			a.getFullYear() === b.getFullYear() && a.getMonth() === b.getMonth() && a.getDate() === b.getDate();
		if (sameDay(day, today)) return m['chat.today']();
		if (sameDay(day, yesterday)) return m['chat.yesterday']();
		return day.toLocaleDateString(undefined, { month: 'long', day: 'numeric', year: 'numeric' });
	}

	function isDayStart(previous: ChatMessage | undefined, message: ChatMessage): boolean {
		if (!previous) return true;
		return (
			new Date(previous.created_at).toDateString() !== new Date(message.created_at).toDateString()
		);
	}

	function timeOf(message: ChatMessage): string {
		return new Date(message.created_at).toLocaleTimeString(undefined, {
			hour: '2-digit',
			minute: '2-digit'
		});
	}

	function startEdit(message: ChatMessage) {
		editingId = message.id;
		editingBody = message.body;
	}

	async function saveEdit() {
		if (!editingId) return;
		const messageId = editingId;
		const body = editingBody;
		editingId = null;
		editingBody = '';
		const original = messages.find((msg) => msg.id === messageId)?.body;
		if (body.trim() && body !== original) {
			await chatState.edit(channel.id, messageId, body);
		}
	}

	function cancelEdit() {
		editingId = null;
		editingBody = '';
	}

	async function confirmDelete() {
		if (!pendingDelete) return;
		deleting = true;
		await chatState.remove(channel.id, pendingDelete.id);
		deleting = false;
		pendingDelete = null;
	}
</script>

<div
	class="min-h-0 flex-1 overflow-y-auto px-4 py-3"
	bind:this={scrollHost}
	onscroll={handleScroll}
>
	{#if hasMore}
		<div class="flex justify-center pb-3">
			<button
				type="button"
				class="flex items-center gap-1.5 rounded-md border border-[var(--app-border)] px-2.5 py-1 text-xs text-[var(--color-text-secondary)] hover:bg-[var(--color-bg-hover)]"
				onclick={loadOlder}
				disabled={loadingOlder}
			>
				{#if loadingOlder}
					<LoaderCircle size={12} class="animate-spin" />
				{/if}
				{m['chat.load_older']()}
			</button>
		</div>
	{/if}

	{#if messages.length === 0}
		<div class="flex h-full flex-col items-center justify-center gap-1 text-center">
			<p class="text-sm text-[var(--color-text-secondary)]">{m['chat.empty_channel']()}</p>
			<p class="text-xs text-[var(--color-text-tertiary)]">
				{channel.type === 'dm'
					? m['chat.empty_dm_desc']({ name: channel.dm_peer?.display_name || channel.dm_peer?.name || '' })
					: m['chat.empty_general_desc']({ name: channel.name })}
			</p>
		</div>
	{:else}
		{#each messages as message, index (message.id)}
			{@const previous = index > 0 ? messages[index - 1] : undefined}
			{@const grouped = !isDayStart(previous, message) && sameGroup(previous, message)}
			{#if isDayStart(previous, message)}
				<div class="my-3 flex items-center gap-3" role="separator">
					<span class="h-px flex-1 bg-[var(--app-border)]"></span>
					<span class="text-[11px] font-medium text-[var(--color-text-tertiary)]">
						{dayLabel(message)}
					</span>
					<span class="h-px flex-1 bg-[var(--app-border)]"></span>
				</div>
			{/if}			<div class="group flex gap-2.5 {grouped ? 'mt-0.5' : 'mt-3'}" data-message-id={message.id}>
				<div class="w-8 shrink-0">
					{#if !grouped}
						<Avatar name={authorName(message)} avatarUrl={message.author?.avatar_url} size="sm" />
					{/if}
				</div>
				<div class="min-w-0 flex-1">
					{#if !grouped}
						<div class="flex items-baseline gap-2">
							<span class="text-sm font-semibold text-[var(--color-text-primary)]">
								{authorName(message)}
							</span>
							<span class="text-[11px] text-[var(--color-text-tertiary)]">
								{timeOf(message)}
							</span>
						</div>
					{/if}
					{#if editingId === message.id}
						<div class="mt-0.5">
							<textarea
								bind:value={editingBody}
								class="w-full resize-none rounded-md border border-[var(--app-border)] bg-[var(--color-bg)] px-2.5 py-1.5 text-sm text-[var(--color-text-primary)] focus:outline-none"
								rows="2"
								onkeydown={(e) => {
									if (e.key === 'Enter' && !e.shiftKey) {
										e.preventDefault();
										void saveEdit();
									} else if (e.key === 'Escape') {
										e.preventDefault();
										cancelEdit();
									}
								}}
							></textarea>
							<div class="mt-1 flex items-center gap-2 text-[11px] text-[var(--color-text-tertiary)]">
								<button
									type="button"
									class="rounded px-1.5 py-0.5 hover:bg-[var(--color-bg-hover)] hover:text-[var(--color-text-primary)]"
									onclick={() => void saveEdit()}
								>
									{m['chat.save']()}
								</button>
								<button type="button" class="rounded px-1.5 py-0.5 hover:bg-[var(--color-bg-hover)]" onclick={cancelEdit}>
									{m['chat.cancel']()}
								</button>
							</div>
						</div>
					{:else}
						<div class="flex items-start gap-2">
							<p class="min-w-0 flex-1 whitespace-pre-wrap break-words text-sm leading-relaxed text-[var(--color-text-primary)]">
								{message.body}
								{#if message.edited_at}
									<span class="ml-1 text-[10px] text-[var(--color-text-tertiary)]">
										({m['chat.edited']()})
									</span>
								{/if}
							</p>
							{#if isOwn(message)}
								<div
									class="flex shrink-0 items-center gap-0.5 opacity-0 transition-opacity group-hover:opacity-100"
								>
									<button
										type="button"
										class="rounded p-1 text-[var(--color-text-tertiary)] hover:bg-[var(--color-bg-hover)] hover:text-[var(--color-text-primary)]"
										title={m['chat.edit']()}
										onclick={() => startEdit(message)}
									>
										<Pencil size={13} />
									</button>
									<button
										type="button"
										class="rounded p-1 text-[var(--color-text-tertiary)] hover:bg-[var(--color-bg-hover)] hover:text-[var(--color-error)]"
										title={m['chat.delete']()}
										onclick={() => (pendingDelete = message)}
									>
										<Trash2 size={13} />
									</button>
								</div>
							{/if}
						</div>
					{/if}
				</div>
			</div>
		{/each}
	{/if}
</div>

{#if pendingDelete}
	<ConfirmDialog
		title={m['chat.delete_confirm_title']()}
		message={m['chat.delete_confirm_body']()}
		confirmLabel={m['chat.delete']()}
		onconfirm={() => confirmDelete()}
		oncancel={() => (pendingDelete = null)}
	/>
{/if}

{#if chatState.typingUsers(channel.id).length > 0}
	<div class="px-4 pb-1 text-xs text-[var(--color-text-tertiary)]">
		{chatState.typingUsers(channel.id).length === 1
			? m['chat.typing']({ name: chatState.memberName(chatState.typingUsers(channel.id)[0] ?? '') || '…' })
			: m['chat.typing_multiple']({ count: chatState.typingUsers(channel.id).length })}
	</div>
{/if}
