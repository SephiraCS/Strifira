<script lang="ts">
	import { untrack, onDestroy } from 'svelte';
	import { m } from '$lib/paraglide/messages.js';
	import { mailState } from '$lib/features/mail/mail.state.svelte';
	import { sendMailDraft, deleteMailDraft } from '$lib/api/mail';
	import { appToast } from '$lib/features/toast/toast';
	import { page } from '$app/state';
	import type { MailAttachmentMeta, MailDraft } from '$lib/types/mail';
	import * as Dialog from '$lib/components/ui/dialog';
	import { Button } from '$lib/components/ui/button';
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';
	import { Textarea } from '$lib/components/ui/textarea';
	import * as Select from '$lib/components/ui/select';
	import Paperclip from '@lucide/svelte/icons/paperclip';
	import X from '@lucide/svelte/icons/x';
	import Upload from '@lucide/svelte/icons/upload';

	let {
		open,
		context,
		onclosed
	}: {
		open: boolean;
		context: {
			to: { name: string; address: string }[];
			cc: { name: string; address: string }[];
			bcc: { name: string; address: string }[];
			subject: string;
			body: string;
			in_reply_to_message_id?: string;
			forward_from_message_id?: string;
		} | null;
		onclosed: () => void;
	} = $props();

	const slug = $derived(page.params.workspaceSlug ?? '');

	let accountId = $state('');
	let to = $state('');
	let cc = $state('');
	let bcc = $state('');
	let showCc = $state(false);
	let subject = $state('');
	let body = $state('');
	let draft = $state<MailDraft | null>(null);
	let attachments = $state<MailAttachmentMeta[]>([]);
	let sending = $state(false);
	let savingDraft = $state(false);
	let draftNotice = $state('');
	let fileInput = $state<HTMLInputElement | null>(null);
	let saveTimer: ReturnType<typeof setTimeout> | null = null;

	// Addresses as "Name <a@b.c>, plain@b.c" <-> API objects.
	function parseAddressList(raw: string): { name: string; address: string }[] {
		return raw
			.split(/[,;]+/)
			.map((entry) => entry.trim())
			.filter(Boolean)
			.map((entry) => {
				const match = entry.match(/^(.*?)\s*<\s*([^>]+)\s*>$/);
				if (match) return { name: match[1].trim().replace(/^"|"$/g, ''), address: match[2].trim() };
				return { name: '', address: entry };
			});
	}

	function formatAddressList(list: { name: string; address: string }[] | undefined): string {
		return (list ?? []).map((a) => (a.name ? `"${a.name}" <${a.address}>` : a.address)).join(', ');
	}

	$effect(() => {
		if (open) {
			accountId = untrack(() => mailState.accounts[0]?.id ?? '');
			to = formatAddressList(context?.to);
			cc = formatAddressList(context?.cc);
			bcc = formatAddressList(context?.bcc);
			showCc = Boolean(context?.cc?.length || context?.bcc?.length);
			subject = context?.subject ?? '';
			body = context?.body ?? '';
			draft = null;
			attachments = [];
			draftNotice = '';
			sending = false;
		}
	});

	$effect(() => {
		if (open && accountId && !mailState.accounts.some((a) => a.id === accountId)) {
			if (saveTimer) clearTimeout(saveTimer);
			draft = null;
			attachments = [];
			body = '';
			onclosed();
		}
	});
	onDestroy(() => {
		if (saveTimer) clearTimeout(saveTimer);
	});

	function resetTimer(): void {
		if (saveTimer) clearTimeout(saveTimer);
		saveTimer = setTimeout(() => void saveDraftNow(), 4000);
	}

	async function saveDraftCore(): Promise<void> {
		// Core save used by autosave AND send: no `sending` guard here, the
		// send flow must always persist before hitting /send.
		if (!slug) return;
		if (!to && !cc && !bcc && !subject && !body) return;
		savingDraft = true;
		try {
			const saved = await mailState.saveDraft(slug, draft, {
				account_id: accountId || undefined,
				to: parseAddressList(to),
				cc: parseAddressList(cc),
				bcc: parseAddressList(bcc),
				subject,
				body,
				in_reply_to_message_id: context?.in_reply_to_message_id,
				forward_from_message_id: context?.forward_from_message_id
			});
			if (!mailState.accounts.some((a) => a.id === saved.account_id)) return;
			draft = saved;
			draftNotice = m['mail.compose.draft_saved']();
		} catch (error) {
			appToast.apiError(error, m['mail.toast.draft_failed']());
		} finally {
			savingDraft = false;
		}
	}

	async function saveDraftNow(): Promise<void> {
		if (sending) return;
		await saveDraftCore();
	}

	async function stageFiles(files: FileList | null): Promise<void> {
		if (!slug || !files || files.length === 0) return;
		try {
			if (!draft) await saveDraftNow();
			if (!draft) return;
			const staged = await mailState.addDraftAttachments(slug, draft.id, Array.from(files));
			attachments = [...attachments, ...staged];
			draftNotice = m['mail.compose.draft_saved']();
		} catch (error) {
			appToast.apiError(error, m['mail.toast.attachment_failed']());
		}
	}

	async function removeAttachment(attachmentId: string): Promise<void> {
		if (!slug || !draft) return;
		try {
			await mailState.removeDraftAttachment(slug, draft.id, attachmentId);
			attachments = attachments.filter((a) => a.id !== attachmentId);
		} catch (error) {
			appToast.apiError(error, m['mail.toast.attachment_failed']());
		}
	}

	async function send(): Promise<void> {
		if (!slug) return;
		sending = true;
		try {
			await saveDraftCore();
			if (!draft) {
				appToast.error(m['mail.compose.error_no_content']());
				return;
			}
			await sendMailDraft(slug, draft.id);
			appToast.success(m['mail.toast.sent']());
			void mailState.reload();
			onclosed();
		} catch (error) {
			appToast.apiError(error, m['mail.toast.send_failed']());
		} finally {
			sending = false;
		}
	}

	async function discard(): Promise<void> {
		if (saveTimer) clearTimeout(saveTimer);
		if (draft && slug) {
			try {
				await deleteMailDraft(slug, draft.id);
			} catch {
				// Discard is best-effort; the draft stays in the drafts list.
			}
		}
		onclosed();
	}

	function onkeydown(event: KeyboardEvent): void {
		if ((event.metaKey || event.ctrlKey) && event.key === 'Enter') {
			event.preventDefault();
			void send();
		}
	}
</script>

<Dialog.Root {open} onOpenChange={(v) => (v ? undefined : void discard())}>
	<Dialog.Content
		class="flex max-h-[85vh] flex-col gap-3 overflow-hidden border-[var(--app-border)] bg-[var(--color-bg-secondary)] sm:max-w-[640px]"
	>
		<Dialog.Header class="pb-0">
			<Dialog.Title>{m['mail.compose']()}</Dialog.Title>
			<Dialog.Description class="text-xs">
				{savingDraft ? m['mail.compose.saving']() : draftNotice || m['mail.compose.autosave_hint']()}
			</Dialog.Description>
		</Dialog.Header>

		<form
			class="flex min-h-0 flex-1 flex-col gap-2.5 overflow-hidden px-4"
			onsubmit={(e) => {
				e.preventDefault();
				void send();
			}}
		>
			<div class="flex flex-col gap-1.5">
				<Label for="mail-account">{m['mail.account.from']()}</Label>
				<Select.Root type="single" value={accountId} onValueChange={(value) => value && (accountId = value)}>
					<Select.Trigger class="w-full" disabled={mailState.accounts.length === 0}>
						{mailState.accounts.find((a) => a.id === accountId)?.email_address ?? m['mail.compose.no_account']()}
					</Select.Trigger>
					<Select.Content>
						{#each mailState.accounts as account (account.id)}
							<Select.Item value={account.id} label={account.email_address}>{account.email_address}</Select.Item>
						{/each}
					</Select.Content>
				</Select.Root>
			</div>

			<div class="flex flex-col gap-1.5">
				<Label for="mail-to">{m['mail.compose.to']()}</Label>
				<Input id="mail-to" bind:value={to} placeholder="ana@exemplo.com" required />
			</div>

			{#if showCc}
				<div class="flex flex-col gap-1.5">
					<Label for="mail-cc">{m['mail.compose.cc']()}</Label>
					<Input id="mail-cc" bind:value={cc} placeholder="bruno@exemplo.com" />
				</div>
				<div class="flex flex-col gap-1.5">
					<Label for="mail-bcc">{m['mail.compose.bcc']()}</Label>
					<Input id="mail-bcc" bind:value={bcc} placeholder="carla@exemplo.com" />
				</div>
			{:else}
				<button
					type="button"
					class="w-fit text-xs text-[var(--color-text-secondary)] hover:text-[var(--color-text-primary)]"
					onclick={() => (showCc = true)}
				>
					{m['mail.compose.show_cc']()}
				</button>
			{/if}

			<div class="flex flex-col gap-1.5">
				<Label for="mail-subject">{m['mail.compose.subject']()}</Label>
				<Input id="mail-subject" bind:value={subject} />
			</div>

			<div class="flex min-h-32 flex-1 flex-col gap-1.5">
				<Label for="mail-body">{m['mail.compose.body']()}</Label>
				<Textarea
					id="mail-body"
					class="min-h-32 flex-1 resize-none font-mono text-sm"
					bind:value={body}
					oninput={resetTimer}
					{onkeydown}
				/>
			</div>

			{#if attachments.length > 0}
				<div class="flex flex-wrap gap-2">
					{#each attachments as attachment (attachment.id)}
						<span class="flex items-center gap-1.5 rounded-md border border-[var(--app-border)] px-2 py-1 text-xs">
							<Paperclip size={12} />
							<span class="max-w-40 truncate">{attachment.filename}</span>
							<button
								type="button"
								class="text-[var(--color-text-tertiary)] hover:text-[var(--color-error)]"
								onclick={() => void removeAttachment(attachment.id)}
							>
								<X size={12} />
							</button>
						</span>
					{/each}
				</div>
			{/if}

			<input
				type="file"
				multiple
				class="hidden"
				bind:this={fileInput}
				onchange={(e) => {
					void stageFiles(e.currentTarget.files);
					e.currentTarget.value = '';
				}}
			/>

			<Dialog.Footer class="gap-2 px-0 pb-1">
				<Button type="button" variant="ghost" size="sm" onclick={() => fileInput?.click()}>
					<Upload size={14} class="mr-1" />
					{m['mail.compose.attach']()}
				</Button>
				<div class="flex-1"></div>
				<Button type="button" variant="outline" size="sm" onclick={() => void discard()}>
					{m['mail.compose.discard']()}
				</Button>
				<Button type="submit" size="sm" disabled={sending || mailState.accounts.length === 0}>
					{sending ? m['mail.compose.sending']() : m['mail.compose.send']()}
				</Button>
			</Dialog.Footer>
		</form>
	</Dialog.Content>
</Dialog.Root>
