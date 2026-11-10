<script lang="ts">
	import { m } from '$lib/paraglide/messages.js';
	import { createMailAccount, updateMailAccount } from '$lib/api/mail';
	import { mailState } from '$lib/features/mail/mail.state.svelte';
	import { appToast } from '$lib/features/toast/toast';
	import { page } from '$app/state';
	import type { MailAccount } from '$lib/types/mail';
	import * as Dialog from '$lib/components/ui/dialog';
	import { Button } from '$lib/components/ui/button';
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';
	import * as Select from '$lib/components/ui/select';

	let {
		open,
		account,
		onclosed
	}: {
		open: boolean;
		account: MailAccount | null;
		onclosed: () => void;
	} = $props();

	const slug = $derived(page.params.workspaceSlug ?? '');

	let emailAddress = $state('');
	let displayName = $state('');
	let imapHost = $state('');
	let imapPort = $state('993');
	let imapSecurity = $state<'none' | 'starttls' | 'tls'>('tls');
	let smtpHost = $state('');
	let smtpPort = $state('587');
	let smtpSecurity = $state<'none' | 'starttls' | 'tls'>('starttls');
	let username = $state('');
	let password = $state('');
	let signature = $state('');
	let submitting = $state(false);
	let error = $state('');

	$effect(() => {
		if (open) {
			emailAddress = account?.email_address ?? '';
			displayName = account?.display_name ?? '';
			imapHost = account?.imap_host ?? '';
			imapPort = String(account?.imap_port ?? 993);
			imapSecurity = account?.imap_security ?? 'tls';
			smtpHost = account?.smtp_host ?? '';
			smtpPort = String(account?.smtp_port ?? 587);
			smtpSecurity = account?.smtp_security ?? 'starttls';
			username = account?.username ?? '';
			password = '';
			signature = account?.signature ?? '';
			submitting = false;
			error = '';
		}
	});

	async function submit(event: SubmitEvent): Promise<void> {
		event.preventDefault();
		if (!slug) return;
		submitting = true;
		error = '';
		try {
			const common = {
				email_address: emailAddress.trim(),
				display_name: displayName.trim(),
				imap_host: imapHost.trim(),
				imap_port: Number(imapPort),
				imap_security: imapSecurity,
				smtp_host: smtpHost.trim(),
				smtp_port: Number(smtpPort),
				smtp_security: smtpSecurity,
				username,
				signature
			};
			if (account) {
				await updateMailAccount(slug, account.id, {
					...common,
					// Blank password keeps the stored one.
					password: password || undefined
				});
				appToast.success(m['mail.toast.account_updated']());
			} else {
				await createMailAccount(slug, { ...common, password });
				appToast.success(m['mail.toast.account_created']());
			}
			await mailState.reload();
			onclosed();
		} catch (err) {
			error = err instanceof Error ? err.message : m['mail.toast.account_failed']();
		} finally {
			submitting = false;
		}
	}
</script>

<Dialog.Root {open} onOpenChange={(v) => (v ? undefined : onclosed())}>
	<Dialog.Content class="max-h-[90vh] overflow-auto border-[var(--app-border)] bg-[var(--color-bg-secondary)] sm:max-w-[520px]">
		<Dialog.Header>
			<Dialog.Title>
				{account ? m['mail.account.edit']() : m['mail.account.add']()}
			</Dialog.Title>
			<Dialog.Description>{m['mail.account.description']()}</Dialog.Description>
		</Dialog.Header>

		<form class="flex flex-col gap-3 px-4 pb-2" onsubmit={submit}>
			<div class="grid grid-cols-2 gap-3">
				<div class="flex flex-col gap-1.5">
					<Label for="mail-account-email">{m['mail.account.email']()}</Label>
					<Input id="mail-account-email" type="email" bind:value={emailAddress} placeholder="voce@exemplo.com" required />
				</div>
				<div class="flex flex-col gap-1.5">
					<Label for="mail-account-name">{m['mail.account.display_name']()}</Label>
					<Input id="mail-account-name" bind:value={displayName} placeholder="Ana Silva" />
				</div>
			</div>

			<div class="rounded-md border border-[var(--app-border)] p-3">
				<p class="mb-2 text-xs font-medium uppercase tracking-wide text-[var(--color-text-tertiary)]">
					{m['mail.account.incoming']()}
				</p>
				<div class="grid grid-cols-3 gap-2">
					<div class="col-span-2 flex flex-col gap-1.5">
						<Label for="mail-imap-host">{m['mail.account.host']()}</Label>
						<Input id="mail-imap-host" bind:value={imapHost} placeholder="imap.exemplo.com" required />
					</div>
					<div class="flex flex-col gap-1.5">
						<Label for="mail-imap-port">{m['mail.account.port']()}</Label>
						<Input id="mail-imap-port" type="number" min="1" max="65535" bind:value={imapPort} required />
					</div>
				</div>
				<div class="mt-2 flex flex-col gap-1.5">
					<Label>{m['mail.account.security']()}</Label>
					<Select.Root type="single" value={imapSecurity} onValueChange={(value) => value && (imapSecurity = value as 'none' | 'starttls' | 'tls')}>
						<Select.Trigger class="w-full">
							{imapSecurity === 'tls' ? m['mail.account.security.tls']() : imapSecurity === 'starttls' ? m['mail.account.security.starttls']() : m['mail.account.security.none']()}
						</Select.Trigger>
						<Select.Content>
							<Select.Item value="tls" label={m['mail.account.security.tls']()}>{m['mail.account.security.tls']()}</Select.Item>
							<Select.Item value="starttls" label={m['mail.account.security.starttls']()}>{m['mail.account.security.starttls']()}</Select.Item>
							<Select.Item value="none" label={m['mail.account.security.none']()}>{m['mail.account.security.none']()}</Select.Item>
						</Select.Content>
					</Select.Root>
				</div>
			</div>

			<div class="rounded-md border border-[var(--app-border)] p-3">
				<p class="mb-2 text-xs font-medium uppercase tracking-wide text-[var(--color-text-tertiary)]">
					{m['mail.account.outgoing']()}
				</p>
				<div class="grid grid-cols-3 gap-2">
					<div class="col-span-2 flex flex-col gap-1.5">
						<Label for="mail-smtp-host">{m['mail.account.host']()}</Label>
						<Input id="mail-smtp-host" bind:value={smtpHost} placeholder="smtp.exemplo.com" required />
					</div>
					<div class="flex flex-col gap-1.5">
						<Label for="mail-smtp-port">{m['mail.account.port']()}</Label>
						<Input id="mail-smtp-port" type="number" min="1" max="65535" bind:value={smtpPort} required />
					</div>
				</div>
				<div class="mt-2 flex flex-col gap-1.5">
					<Label>{m['mail.account.security']()}</Label>
					<Select.Root type="single" value={smtpSecurity} onValueChange={(value) => value && (smtpSecurity = value as 'none' | 'starttls' | 'tls')}>
						<Select.Trigger class="w-full">
							{smtpSecurity === 'tls' ? m['mail.account.security.tls']() : smtpSecurity === 'starttls' ? m['mail.account.security.starttls']() : m['mail.account.security.none']()}
						</Select.Trigger>
						<Select.Content>
							<Select.Item value="tls" label={m['mail.account.security.tls']()}>{m['mail.account.security.tls']()}</Select.Item>
							<Select.Item value="starttls" label={m['mail.account.security.starttls']()}>{m['mail.account.security.starttls']()}</Select.Item>
							<Select.Item value="none" label={m['mail.account.security.none']()}>{m['mail.account.security.none']()}</Select.Item>
						</Select.Content>
					</Select.Root>
				</div>
			</div>

			<div class="grid grid-cols-2 gap-3">
				<div class="flex flex-col gap-1.5">
					<Label for="mail-account-username">{m['mail.account.username']()}</Label>
					<Input id="mail-account-username" bind:value={username} autocomplete="off" required />
				</div>
				<div class="flex flex-col gap-1.5">
					<Label for="mail-account-password">
						{account ? m['mail.account.password.keep']() : m['mail.account.password']()}
					</Label>
					<Input id="mail-account-password" type="password" bind:value={password} autocomplete="new-password" required={!account} />
				</div>
			</div>

			<div class="flex flex-col gap-1.5">
				<Label for="mail-account-signature">{m['mail.account.signature']()}</Label>
				<Input id="mail-account-signature" bind:value={signature} placeholder={m['mail.account.signature_hint']()} />
			</div>

			{#if error}
				<p class="text-sm text-[var(--color-error)]">{error}</p>
			{/if}

			<Dialog.Footer class="gap-2 px-0">
				<Button type="button" variant="outline" onclick={onclosed}>{m['sharedComponents.confirm_dialog.cancel']()}</Button>
				<Button type="submit" disabled={submitting}>
					{submitting ? m['mail.account.testing']() : m['mail.account.save']()}
				</Button>
			</Dialog.Footer>
		</form>
	</Dialog.Content>
</Dialog.Root>
