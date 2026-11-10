<script lang="ts">
	import { page } from '$app/state';
	import { onMount } from 'svelte';
	import { mailAccountMessage } from '$lib/features/mail/autoconfig.messages';
	import { m } from '$lib/paraglide/messages.js';
	import { mailState } from '$lib/features/mail/mail.state.svelte';
	import ComposeDialog from '$lib/features/mail/ComposeDialog.svelte';
	import AccountDialog from '$lib/features/mail/AccountDialog.svelte';
	import EmptyState from '$lib/components/shared/EmptyState.svelte';
	import LoadingState from '$lib/components/shared/LoadingState.svelte';
	import ConfirmDialog from '$lib/components/shared/ConfirmDialog.svelte';
	import { Button } from '$lib/components/ui/button';
	import { Input } from '$lib/components/ui/input';
	import * as DropdownMenu from '$lib/components/ui/dropdown-menu';
	import { downloadMailAttachment } from '$lib/api/mail';
	import { appToast } from '$lib/features/toast/toast';
	import type { MailAccount, MailMessageDetail } from '$lib/types/mail';

	import Inbox from '@lucide/svelte/icons/inbox';
	import Send from '@lucide/svelte/icons/send';
	import Star from '@lucide/svelte/icons/star';
	import Archive from '@lucide/svelte/icons/archive';
	import ShieldAlert from '@lucide/svelte/icons/shield-alert';
	import Mail from '@lucide/svelte/icons/mail';
	import MailPlus from '@lucide/svelte/icons/mail-plus';
	import Search from '@lucide/svelte/icons/search';
	import Paperclip from '@lucide/svelte/icons/paperclip';
	import Reply from '@lucide/svelte/icons/reply';
	import ReplyAll from '@lucide/svelte/icons/reply-all';
	import Forward from '@lucide/svelte/icons/forward';
	import Trash2 from '@lucide/svelte/icons/trash-2';
	import MailOpen from '@lucide/svelte/icons/mail-open';
	import Settings2 from '@lucide/svelte/icons/settings-2';
	import RefreshCw from '@lucide/svelte/icons/refresh-cw';
	import Plus from '@lucide/svelte/icons/plus';
	import Download from '@lucide/svelte/icons/download';
	import MoreVertical from '@lucide/svelte/icons/ellipsis-vertical';
	import ChevronLeft from '@lucide/svelte/icons/chevron-left';

	const slug = $derived(page.params.workspaceSlug ?? '');

	let showCompose = $state(false);
	let showAccountDialog = $state(false);
	let editingAccount = $state<MailAccount | null>(null);
	let deletingAccount = $state<MailAccount | null>(null);
	let listQuery = $state('');
	onMount(() => {
		if (page.url.searchParams.get('connect') === '1') showAccountDialog = true;
	});

	const folders = $derived([
		{ id: 'inbox' as const, label: m['mail.folder.inbox'](), icon: Inbox, count: mailState.unreadInboxCount },
		{ id: 'sent' as const, label: m['mail.folder.sent'](), icon: Send, count: 0 },
		{ id: 'starred' as const, label: m['mail.folder.starred'](), icon: Star, count: 0 },
		{ id: 'archived' as const, label: m['mail.folder.archived'](), icon: Archive, count: 0 },
		{ id: 'spam' as const, label: m['mail.folder.spam'](), icon: ShieldAlert, count: 0 }
	]);

	const folderFilters = $derived<Record<string, { archived?: boolean; spam?: boolean; starred?: boolean }>>({
		inbox: { archived: false, spam: false },
		sent: {},
		starred: { starred: true },
		archived: { archived: true, spam: false },
		spam: { spam: true }
	});

	function addressName(name: string, address: string): string {
		return name || address;
	}

	function formatTime(iso: string): string {
		const date = new Date(iso);
		const now = new Date();
		const sameDay = date.toDateString() === now.toDateString();
		if (sameDay) {
			return date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
		}
		return date.toLocaleDateString([], { month: 'short', day: 'numeric' });
	}

	function formatSize(bytes: number): string {
		if (bytes < 1024) return `${bytes} B`;
		if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
		return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
	}

	function openReply(detail: MailMessageDetail, all: boolean): void {
		const selfAddresses = mailState.accounts.map((a) => a.email_address.toLowerCase());
		const isSelf = (address: string) => selfAddresses.includes(address.toLowerCase());
		const to = all
			? [
					{ name: detail.from.name, address: detail.reply_to || detail.from.address },
					...detail.to.filter((a) => !isSelf(a.address)).map((a) => ({ name: a.name, address: a.address }))
				]
			: [{ name: detail.from.name, address: detail.reply_to || detail.from.address }];
		const cc = all ? (detail.cc ?? []).map((a) => ({ name: a.name, address: a.address })) : [];
		const quoted = detail.body_text
			.split('\n')
			.map((line) => `> ${line}`)
			.join('\n');
		showCompose = true;
		composeContext = {
			to,
			cc,
			bcc: [],
			subject: detail.subject,
			body: `\n\n${m['mail.compose.quoted_header']()}\n${quoted}`,
			in_reply_to_message_id: detail.id,
			forward_from_message_id: undefined
		};
	}

	function openForward(detail: MailMessageDetail): void {
		const quoted = detail.body_text
			.split('\n')
			.map((line) => `> ${line}`)
			.join('\n');
		showCompose = true;
		composeContext = {
			to: [],
			cc: [],
			bcc: [],
			subject: detail.subject,
			body: `\n\n${m['mail.compose.forwarded_header']()}\n${quoted}`,
			in_reply_to_message_id: undefined,
			forward_from_message_id: detail.id
		};
	}

	let composeContext = $state<{
		to: { name: string; address: string }[];
		cc: { name: string; address: string }[];
		bcc: { name: string; address: string }[];
		subject: string;
		body: string;
		in_reply_to_message_id?: string;
		forward_from_message_id?: string;
	} | null>(null);

	// Load accounts + cached messages for this workspace (re-runs on route
	// change; ensureWorkspace resets state when the workspace differs).
	$effect(() => {
		if (slug) void mailState.ensureWorkspace(slug);
	});

	function newCompose(): void {
		composeContext = null;
		showCompose = true;
	}

	function onComposeClosed(): void {
		showCompose = false;
		composeContext = null;
	}

	async function downloadAttachment(attachmentId: string, filename: string): Promise<void> {
		try {
			const { blob } = await downloadMailAttachment(slug, attachmentId);
			const url = URL.createObjectURL(blob);
			const link = document.createElement('a');
			link.href = url;
			link.download = filename;
			link.click();
			URL.revokeObjectURL(url);
		} catch (error) {
			appToast.apiError(error, m['mail.toast.download_failed']());
		}
	}

	function confirmDeleteAccount(): void {
		const account = deletingAccount;
		deletingAccount = null;
		if (!account) return;
		void mailState
			.removeAccount(account.id)
			.then(() => {
				appToast.success(m['mail.toast.account_deleted']());
			})
			.catch((err) => appToast.apiError(err, m['mail.toast.account_failed']()));
	}
</script>

<div class="flex h-full min-h-0">
	{#if mailState.loading && mailState.messages.length === 0 && mailState.accounts.length === 0}
		<div class="flex flex-1 items-center justify-center">
			<LoadingState />
		</div>
	{:else}
		<!-- Folder rail -->
		<div class="hidden w-56 shrink-0 flex-col border-r border-[var(--app-border)] md:flex">
			<div class="p-3">
				<Button class="w-full justify-start gap-2" onclick={newCompose}>
					<MailPlus size={16} />
					{m['mail.compose']()}
				</Button>
			</div>
			<nav class="flex flex-col gap-0.5 px-3">
				{#each folders as folder (folder.id)}
					<button
						type="button"
						class="flex items-center gap-2 rounded-md px-2 py-1.5 text-sm {mailState.folder === folder.id
							? 'bg-[var(--color-bg-hover)] text-[var(--color-text-primary)]'
							: 'text-[var(--color-text-secondary)] hover:bg-[var(--color-bg-hover)]/60'}"
						onclick={() => mailState.setFolder(folder.id)}
					>
						<folder.icon size={16} class="shrink-0" />
						<span class="truncate">{folder.label}</span>
						{#if folder.count > 0}
							<span
								class="ml-auto rounded-full bg-[var(--app-accent)] px-1.5 text-xs font-medium text-[var(--app-accent-foreground)]"
							>
								{folder.count > 99 ? '99+' : folder.count}
							</span>
						{/if}
					</button>
				{/each}
			</nav>
			<div class="mt-4 flex min-h-0 flex-1 flex-col">
				<div class="flex items-center justify-between px-4 pb-1">
					<span class="text-xs font-medium uppercase tracking-wide text-[var(--color-text-tertiary)]">
						{m['mail.accounts']()}
					</span>
					<button
						type="button"
						class="rounded p-1 text-[var(--color-text-secondary)] hover:bg-[var(--color-bg-hover)]"
						title={m['mail.account.add']()}
						onclick={() => {
							editingAccount = null;
							showAccountDialog = true;
						}}
					>
						<Plus size={14} />
					</button>
				</div>
				<div class="min-h-0 flex-1 overflow-auto px-3 pb-3">
					{#each mailState.accounts as account (account.id)}
						<div
							class="group flex items-center gap-1 rounded-md px-2 py-1.5 text-sm {mailState.selectedAccountId ===
							account.id
								? 'bg-[var(--color-bg-hover)] text-[var(--color-text-primary)]'
								: 'text-[var(--color-text-secondary)] hover:bg-[var(--color-bg-hover)]/60'}"
						>
							<button
								type="button"
								class="flex min-w-0 flex-1 items-center gap-2 text-left"
								onclick={() => mailState.setAccount(mailState.selectedAccountId === account.id ? null : account.id)}
							>
								<Mail size={14} class="shrink-0" />
								<span class="truncate">{account.email_address}</span>
							</button>
							<button
								type="button"
								class="hidden rounded p-1 text-[var(--color-text-secondary)] hover:text-[var(--color-text-primary)] group-hover:block"
								title={m['mail.sync']()}
								disabled={mailState.syncing}
								onclick={() => void mailState.syncAccount(account.id)}
							>
								<RefreshCw size={13} class={mailState.syncing ? 'animate-spin' : ''} />
							</button>
							<button
								type="button"
								class="hidden rounded p-1 text-[var(--color-text-secondary)] hover:text-[var(--color-text-primary)] group-hover:block"
								title={m['mail.account.settings']()}
								onclick={() => {
									editingAccount = account;
									showAccountDialog = true;
								}}
							>
								<Settings2 size={13} />
							</button>
						</div>
					{/each}
				</div>
			</div>
		</div>

		<!-- Thread list -->
		<div class="flex w-full min-w-0 flex-col border-r border-[var(--app-border)] md:w-[380px] md:shrink-0">
			<div class="flex h-12 shrink-0 items-center gap-2 border-b border-[var(--app-border)] px-3">
				<div class="relative min-w-0 flex-1">
					<Search size={14} class="absolute top-1/2 left-2.5 -translate-y-1/2 text-[var(--color-text-tertiary)]" />
					<Input
						class="h-8 pl-8 text-sm"
						placeholder={m['mail.search_placeholder']()}
						bind:value={listQuery}
						oninput={() => mailState.setSearch(listQuery)}
					/>
				</div>
			</div>
			<div class="min-h-0 flex-1 overflow-auto">
				{#if mailState.threads.length === 0}
					<div class="p-6">
						<EmptyState
							title={m['mail.empty.list_title']()}
							description={mailState.search ? m['mail.empty.search_hint']() : m['mail.empty.list_hint']()}
						/>
					</div>
				{:else}
					{#each mailState.threads as thread (thread.key)}
						<button
							type="button"
							class="flex w-full flex-col gap-0.5 border-b border-[var(--app-border)]/60 px-3 py-2.5 text-left {mailState
								.selectedMessage?.thread_key === thread.key
								? 'bg-[var(--color-bg-hover)]/60'
								: 'hover:bg-[var(--color-bg-hover)]/40'}"
							onclick={() => void mailState.selectMessage(thread.last.id)}
						>
							<div class="flex items-center gap-2">
								{#if thread.unreadCount > 0}
									<span class="h-2 w-2 shrink-0 rounded-full bg-[var(--app-accent)]"></span>
								{:else}
									<span class="h-2 w-2 shrink-0"></span>
								{/if}
								<span
									class="min-w-0 flex-1 truncate text-sm {thread.unreadCount > 0
										? 'font-semibold text-[var(--color-text-primary)]'
										: 'text-[var(--color-text-secondary)]'}"
								>
									{mailState.folder === 'sent'
										? addressName(thread.last.to[0]?.name ?? '', thread.last.to[0]?.address ?? '')
										: addressName(thread.last.from.name, thread.last.from.address)}
								</span>
								{#if thread.messages.length > 1}
									<span
										class="shrink-0 rounded bg-[var(--color-bg-hover)] px-1.5 text-xs text-[var(--color-text-tertiary)]"
									>
										{thread.messages.length}
									</span>
								{/if}
								{#if thread.hasAttachments}
									<Paperclip size={12} class="shrink-0 text-[var(--color-text-tertiary)]" />
								{/if}
								<span class="shrink-0 text-xs text-[var(--color-text-tertiary)]">{formatTime(thread.last.sent_at)}</span
								>
							</div>
							<div class="flex items-center gap-2 pl-4">
								<span
									class="min-w-0 flex-1 truncate text-sm {thread.unreadCount > 0
										? 'text-[var(--color-text-primary)]'
										: 'text-[var(--color-text-secondary)]'}"
								>
									{thread.last.subject || m['mail.no_subject']()}
								</span>
								{#if thread.last.starred}
									<Star size={12} class="shrink-0 fill-current text-amber-400" />
								{/if}
							</div>
							<p class="line-clamp-1 pl-4 text-xs text-[var(--color-text-tertiary)]">{thread.last.snippet}</p>
						</button>
					{/each}
				{/if}
			</div>
		</div>

		<!-- Reading pane -->
		<div class="hidden min-w-0 flex-1 flex-col md:flex">
			{#if mailState.selectedMessage}
				{@const detail = mailState.selectedMessage}
				<div class="flex h-12 shrink-0 items-center gap-1 border-b border-[var(--app-border)] px-3">
					<Button variant="ghost" size="xs" onclick={() => openReply(detail, false)} title={m['mail.reply']()}>
						<Reply size={14} class="mr-1" />
						{m['mail.reply']()}
					</Button>
					<Button variant="ghost" size="xs" onclick={() => openReply(detail, true)} title={m['mail.reply_all']()}>
						<ReplyAll size={14} class="mr-1" />
						{m['mail.reply_all']()}
					</Button>
					<Button variant="ghost" size="xs" onclick={() => openForward(detail)} title={m['mail.forward']()}>
						<Forward size={14} class="mr-1" />
						{m['mail.forward']()}
					</Button>
					<div class="flex-1"></div>
					<Button
						variant="ghost"
						size="icon-sm"
						title={detail.starred ? m['mail.unstar']() : m['mail.star']()}
						onclick={() => void mailState.toggleStar(detail.id)}
					>
						<Star size={14} class={detail.starred ? 'fill-current text-amber-400' : ''} />
					</Button>
					<DropdownMenu.Root>
						<DropdownMenu.Trigger>
							<Button variant="ghost" size="icon-sm" title={m['mail.more']()}>
								<MoreVertical size={14} />
							</Button>
						</DropdownMenu.Trigger>
						<DropdownMenu.Content align="end" class="w-48">
							<DropdownMenu.Item onclick={() => void mailState.setArchived(detail.id, !detail.archived)}>
								<Archive size={14} class="mr-2" />
								{detail.archived ? m['mail.unarchive']() : m['mail.archive']()}
							</DropdownMenu.Item>
							<DropdownMenu.Item onclick={() => void mailState.setSpam(detail.id, !detail.spam)}>
								<ShieldAlert size={14} class="mr-2" />
								{detail.spam ? m['mail.not_spam']() : m['mail.spam']()}
							</DropdownMenu.Item>
							<DropdownMenu.Item onclick={() => void mailState.markUnread(detail.id)}>
								<MailOpen size={14} class="mr-2" />
								{m['mail.mark_unread']()}
							</DropdownMenu.Item>
							<DropdownMenu.Item
								class="text-[var(--color-error)]"
								onclick={() => void mailState.deleteMessage(detail.id)}
							>
								<Trash2 size={14} class="mr-2" />
								{m['mail.delete']()}
							</DropdownMenu.Item>
						</DropdownMenu.Content>
					</DropdownMenu.Root>
				</div>
				<div class="min-h-0 flex-1 overflow-auto px-6 py-4">
					<h2 class="text-lg font-semibold text-[var(--color-text-primary)]">
						{detail.subject || m['mail.no_subject']()}
					</h2>
					<div class="mt-3 flex items-start gap-3">
						<div class="min-w-0 flex-1">
							<p class="text-sm font-medium text-[var(--color-text-primary)]">
								{addressName(detail.from.name, detail.from.address)}
							</p>
							<p class="truncate text-xs text-[var(--color-text-tertiary)]">
								{m['mail.to']()}
								{detail.to.map((a) => a.address).join(', ')}
								{#if detail.cc?.length}
									&nbsp;·&nbsp;{m['mail.cc']()}
									{detail.cc.map((a) => a.address).join(', ')}
								{/if}
							</p>
						</div>
						<span class="shrink-0 text-xs text-[var(--color-text-tertiary)]">
							{new Date(detail.sent_at).toLocaleString()}
						</span>
					</div>
					{#if detail.attachments.length > 0}
						<div class="mt-3 flex flex-wrap gap-2">
							{#each detail.attachments as attachment (attachment.id)}
								<button
									type="button"
									class="flex items-center gap-2 rounded-md border border-[var(--app-border)] px-2.5 py-1.5 text-xs text-[var(--color-text-secondary)] hover:bg-[var(--color-bg-hover)]"
									onclick={() => void downloadAttachment(attachment.id, attachment.filename)}
								>
									<Paperclip size={12} />
									<span class="max-w-48 truncate">{attachment.filename}</span>
									<span class="text-[var(--color-text-tertiary)]">{formatSize(attachment.size_bytes)}</span>
									<Download size={12} />
								</button>
							{/each}
						</div>
					{/if}
					<div class="mt-5">
						{#if detail.body_html}
							<div class="prose-mail max-w-none text-sm text-[var(--color-text-primary)]">
								<!-- Server-side sanitized (bluemonday) before storage. -->
								{@html detail.body_html}
							</div>
						{:else}
							<pre
								class="max-w-none font-sans text-sm whitespace-pre-wrap text-[var(--color-text-primary)]">{detail.body_text}</pre>
						{/if}
					</div>
				</div>
			{:else}
				<div class="flex flex-1 items-center justify-center">
					<EmptyState title={m['mail.empty.reader_title']()} description={m['mail.empty.reader_hint']()} />
				</div>
			{/if}
		</div>

		<!-- Mobile reader overlay -->
		{#if mailState.selectedMessage}
			<div class="fixed inset-0 z-40 flex flex-col bg-[var(--color-bg)] md:hidden">
				<div class="flex h-12 shrink-0 items-center gap-2 border-b border-[var(--app-border)] px-2">
					<Button variant="ghost" size="icon-sm" onclick={() => mailState.clearSelection()}>
						<ChevronLeft size={16} />
					</Button>
					<span class="min-w-0 flex-1 truncate text-sm font-medium"
						>{mailState.selectedMessage.subject || m['mail.no_subject']()}</span
					>
					<Button variant="ghost" size="icon-sm" onclick={() => openReply(mailState.selectedMessage!, false)}>
						<Reply size={14} />
					</Button>
				</div>
				<div class="min-h-0 flex-1 overflow-auto px-4 py-3">
					<p class="text-sm font-medium">
						{addressName(mailState.selectedMessage.from.name, mailState.selectedMessage.from.address)}
					</p>
					{#if mailState.selectedMessage.body_html}
						<div class="mt-3 text-sm">
							{@html mailState.selectedMessage.body_html}
						</div>
					{:else}
						<pre class="mt-3 font-sans text-sm whitespace-pre-wrap">{mailState.selectedMessage.body_text}</pre>
					{/if}
				</div>
			</div>
		{/if}
	{/if}
</div>

<ComposeDialog open={showCompose} context={composeContext} onclosed={onComposeClosed} />

<AccountDialog
	open={showAccountDialog}
	account={editingAccount}
	onclosed={() => {
		showAccountDialog = false;
		editingAccount = null;
	}}
/>

{#if deletingAccount}
	<ConfirmDialog
		title={m['mail.account.delete_confirm_title']()}
		message={m['mail.account.delete_confirm_body']({ address: deletingAccount.email_address }) +
			(deletingAccount.auth_method === 'oauth2' ? ` ${mailAccountMessage('disconnect')}` : '')}
		confirmLabel={m['mail.account.delete']()}
		variant="danger"
		onconfirm={confirmDeleteAccount}
		oncancel={() => (deletingAccount = null)}
	/>
{/if}
