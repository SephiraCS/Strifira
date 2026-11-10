<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { completeMailOAuth } from '$lib/api/mail';
	import { getErrorMessage } from '$lib/features/toast/toast';
	import { mailOAuthStorageKey } from '$lib/features/mail/oauth';
	import { mailAccountMessage } from '$lib/features/mail/autoconfig.messages';
	import { Button } from '$lib/components/ui/button';

	let error = $state('');
	let workspace = $state('');
	let retryAddress = '';
	onMount(() => {
		const params = new URLSearchParams(window.location.search);
		// Remove the authorization code from the address bar before requests,
		// navigation or rendering any error text. Never display vendor details.
		window.history.replaceState(null, '', window.location.pathname);
		void finish(params);
	});

	async function finish(params: URLSearchParams): Promise<void> {
		try {
			const saved = JSON.parse(sessionStorage.getItem(mailOAuthStorageKey) ?? 'null');
			if (!saved || !saved.workspace || saved.state !== params.get('state'))
				throw new Error(mailAccountMessage('oauth_failed'));
			workspace = saved.workspace;
			retryAddress = saved.email_address;
			sessionStorage.removeItem(mailOAuthStorageKey);
			await completeMailOAuth(workspace, {
				state: saved.state,
				code: params.get('code') ?? '',
				provider_error: params.get('error') ?? ''
			});
			await goto(`/${encodeURIComponent(workspace)}/mail`, { replaceState: true });
		} catch (err) {
			if (workspace)
				sessionStorage.setItem(
					`${mailOAuthStorageKey}:retry`,
					JSON.stringify({ workspace, email_address: retryAddress })
				);
			error = getErrorMessage(err, mailAccountMessage('oauth_failed'));
		}
	}

	function retry(): void {
		void goto(workspace ? `/${encodeURIComponent(workspace)}/mail?connect=1` : '/', { replaceState: true });
	}
</script>

<svelte:head>
	<title>{mailAccountMessage('authorize')} · Strifira</title>
	<meta name="referrer" content="no-referrer" />
</svelte:head>

<main class="mx-auto flex min-h-screen max-w-lg flex-col justify-center gap-4 px-6">
	<h1 class="text-lg font-semibold">{mailAccountMessage('authorize')}</h1>
	{#if error}
		<p role="alert" class="text-sm text-[var(--color-error)]">{error}</p>
		<p class="text-sm text-[var(--color-text-secondary)]">{mailAccountMessage('oauth_failed')}</p>
		<Button onclick={retry}>{mailAccountMessage('back_to_mail')}</Button>
	{:else}
		<p role="status">{mailAccountMessage('validating')}</p>
	{/if}
</main>
