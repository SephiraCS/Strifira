<script lang="ts">
	import { m } from '$lib/paraglide/messages.js';
	import { mailAccountMessage } from './autoconfig.messages';
	import { createMailAccount, discoverMailAccount, updateMailAccount, type MailDiscovery } from '$lib/api/mail';
	import { mailState } from '$lib/features/mail/mail.state.svelte';
	import { authState } from '$lib/features/auth/auth.state.svelte';
	import { appToast, getErrorMessage } from '$lib/features/toast/toast';
	import { page } from '$app/state';
	import type { MailAccount } from '$lib/types/mail';
	import * as Dialog from '$lib/components/ui/dialog';
	import { Button } from '$lib/components/ui/button';
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';

	let { open, account, onclosed }: { open: boolean; account: MailAccount | null; onclosed: () => void } = $props();
	const slug = $derived(page.params.workspaceSlug ?? '');
	let emailAddress = $state('');
	let displayName = $state('');
	let password = $state('');
	let signature = $state('');
	let submitting = $state(false);
	let error = $state('');
	let discovery = $state<MailDiscovery | null>(null);
	let requestSerial = 0;
	const changedAddress = $derived(!!account && emailAddress.trim() !== account.email_address);
	const needsDiscovery = $derived(!discovery && (!account || changedAddress));
	const needsOAuth = $derived(discovery?.authentication === 'oauth2');

	$effect(() => {
		if (open) {
			emailAddress = account?.email_address ?? authState.user?.email ?? '';
			displayName = account?.display_name ?? '';
			signature = account?.signature ?? '';
			password = '';
			submitting = false;
			discovery = null;
			error = '';
		}
	});

	function addressChanged(): void {
		discovery = null;
		password = '';
		error = '';
	}

	function close(): void {
		// Discard credentials when dismissing the dialog, including Escape.
		requestSerial++;
		password = '';
		onclosed();
	}

	async function submit(event: SubmitEvent): Promise<void> {
		event.preventDefault();
		if (!slug || submitting) return;
		const serial = ++requestSerial;
		submitting = true;
		error = '';
		try {
			if (needsDiscovery || needsOAuth) {
				const result = await discoverMailAccount(slug, emailAddress.trim());
				if (!open || serial !== requestSerial) return;
				discovery = result;
				emailAddress = result.email_address;
				return;
			}
			const body = {
				email_address: emailAddress.trim(),
				display_name: displayName.trim(),
				password: password || undefined,
				signature
			};
			if (account) {
				await updateMailAccount(slug, account.id, body);
				appToast.success(m['mail.toast.account_updated']());
			} else {
				await createMailAccount(slug, body);
				appToast.success(m['mail.toast.account_created']());
			}
			if (serial === requestSerial) password = '';
			await mailState.reload();
			if (serial === requestSerial) close();
		} catch (err) {
			// Keep the address and discovery result so authentication can be retried.
			if (serial === requestSerial) error = getErrorMessage(err, m['mail.toast.account_failed']());
		} finally {
			if (serial === requestSerial) submitting = false;
		}
	}
</script>

<Dialog.Root {open} onOpenChange={(v) => (v ? undefined : close())}>
	<Dialog.Content
		class="max-h-[90vh] overflow-auto border-[var(--app-border)] bg-[var(--color-bg-secondary)] sm:max-w-[520px]"
	>
		<Dialog.Header>
			<Dialog.Title>{account ? m['mail.account.edit']() : m['mail.account.add']()}</Dialog.Title>
			<Dialog.Description>{mailAccountMessage('description')}</Dialog.Description>
		</Dialog.Header>
		<form class="flex flex-col gap-4 px-4 pb-2" onsubmit={submit}>
			<div class="flex flex-col gap-1.5">
				<Label for="mail-account-email">{m['mail.account.email']()}</Label>
				<Input
					id="mail-account-email"
					type="email"
					bind:value={emailAddress}
					oninput={addressChanged}
					autocomplete="email"
					placeholder="voce@exemplo.com"
					required
					disabled={submitting}
				/>
			</div>
			{#if discovery}
				<div class="rounded-md border border-[var(--app-border)] p-3 text-sm" aria-live="polite">
					<p class="font-medium">{mailAccountMessage('found', discovery.provider)}</p>
					{#if needsOAuth}
						<p class="mt-2 text-[var(--color-text-secondary)]" role="alert">
							{mailAccountMessage('oauth_required')}
						</p>
					{:else}
						<p class="mt-1 text-[var(--color-text-secondary)]">{mailAccountMessage('secure')}</p>
						{#if discovery.authentication === 'app_password'}
							<p class="mt-2 text-[var(--color-text-secondary)]">{mailAccountMessage('app_password')}</p>
							{#if discovery.help_url}
								<a
									class="mt-2 inline-block text-[var(--app-accent)] underline"
									href={discovery.help_url}
									target="_blank"
									rel="noopener noreferrer">{mailAccountMessage('provider_help')}</a
								>
							{/if}
						{/if}
					{/if}
				</div>
			{/if}
			{#if !needsDiscovery && !needsOAuth}
				<div class="flex flex-col gap-1.5">
					<Label for="mail-account-password"
						>{account && !changedAddress
							? m['mail.account.password.keep']()
							: discovery?.authentication === 'app_password'
								? mailAccountMessage('password_label')
								: m['mail.account.password']()}</Label
					>
					<Input
						id="mail-account-password"
						type="password"
						bind:value={password}
						autocomplete="new-password"
						required={!account || changedAddress}
						disabled={submitting}
						maxlength={1024}
					/>
					<p class="text-xs text-[var(--color-text-tertiary)]">{mailAccountMessage('credential_hint')}</p>
				</div>
				<div class="flex flex-col gap-1.5">
					<Label for="mail-account-name">{m['mail.account.display_name']()}</Label>
					<Input id="mail-account-name" bind:value={displayName} maxlength={120} disabled={submitting} />
				</div>
				<div class="flex flex-col gap-1.5">
					<Label for="mail-account-signature">{m['mail.account.signature']()}</Label>
					<Input
						id="mail-account-signature"
						bind:value={signature}
						maxlength={4000}
						placeholder={m['mail.account.signature_hint']()}
						disabled={submitting}
					/>
				</div>
			{/if}
			{#if error}<p class="text-sm text-[var(--color-error)]" role="alert">{error}</p>{/if}
			<Dialog.Footer class="gap-2 px-0">
				<Button type="button" variant="outline" onclick={close} disabled={submitting}
					>{m['sharedComponents.confirm_dialog.cancel']()}</Button
				>
				<Button type="submit" disabled={submitting}>
					{submitting
						? needsDiscovery || needsOAuth
							? mailAccountMessage('discovering')
							: mailAccountMessage('validating')
						: needsOAuth
							? mailAccountMessage('retry')
							: needsDiscovery
								? mailAccountMessage('continue')
								: account
									? m['mail.account.save']()
									: mailAccountMessage('connect')}
				</Button>
			</Dialog.Footer>
		</form>
	</Dialog.Content>
</Dialog.Root>
