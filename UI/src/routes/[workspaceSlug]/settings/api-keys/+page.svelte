<script lang="ts">
	import { onMount } from 'svelte';
	import { listAPIKeys, createAPIKey, revokeAPIKey, type APIKey } from '$lib/api/api-keys';
	import * as Dialog from '$lib/components/ui/dialog';
	import { Button } from '$lib/components/ui/button';
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';
	import { Badge } from '$lib/components/ui/badge';
	import { Switch } from '$lib/components/ui/switch';
	import EmptyState from '$lib/components/shared/EmptyState.svelte';
	import { appToast } from '$lib/features/toast/toast';
	import { m } from '$lib/paraglide/messages.js';
	import { getLocale } from '$lib/paraglide/runtime.js';
	import { formatRelativeTime } from '$lib/utils/format';
	import Plus from '@lucide/svelte/icons/plus';
	import Trash2 from '@lucide/svelte/icons/trash';
	import KeyRound from '@lucide/svelte/icons/key-round';
	import Copy from '@lucide/svelte/icons/copy';
	import Check from '@lucide/svelte/icons/check';

	let keys = $state<APIKey[]>([]);
	let loading = $state(true);
	let showCreate = $state(false);
	let showToken = $state(false);
	let createdToken = $state('');
	let copied = $state(false);

	let newName = $state('');
	let newReadOnly = $state(false);
	let newExpiresInDays = $state('');

	onMount(async () => {
		try {
			keys = await listAPIKeys();
		} finally {
			loading = false;
		}
	});

	function resetForm() {
		newName = '';
		newReadOnly = false;
		newExpiresInDays = '';
	}

	async function handleCreate(e: Event) {
		e.preventDefault();
		if (!newName.trim()) return;
		const expiresInDays = newExpiresInDays.trim() ? parseInt(newExpiresInDays, 10) : undefined;
		try {
			const key = await createAPIKey({
				name: newName.trim(),
				read_only: newReadOnly,
				expires_in_days: Number.isFinite(expiresInDays) ? expiresInDays : undefined
			});
			keys = [key, ...keys];
			showCreate = false;
			createdToken = key.token;
			copied = false;
			showToken = true;
			resetForm();
			appToast.success(m['settings.api_keys.created_toast']());
		} catch (err: any) {
			appToast.apiError(err, m['settings.api_keys.failed_create']());
		}
	}

	async function handleRevoke(key: APIKey) {
		try {
			await revokeAPIKey(key.id);
			keys = keys.map((k) => (k.id === key.id ? { ...k, revoked_at: k.revoked_at ?? new Date().toISOString() } : k));
			appToast.success(m['settings.api_keys.revoked_toast']());
		} catch (err: any) {
			appToast.apiError(err, m['settings.api_keys.failed_revoke']());
		}
	}

	async function copyToken() {
		try {
			await navigator.clipboard.writeText(createdToken);
			copied = true;
			setTimeout(() => (copied = false), 2000);
		} catch {
			// clipboard unavailable; the token stays selectable in the input
		}
	}

	function isExpired(key: APIKey): boolean {
		return !!key.expires_at && new Date(key.expires_at).getTime() < Date.now();
	}
</script>

<div class="mx-auto max-w-2xl px-8 py-10">
	<div class="flex items-center justify-between">
		<h1 class="text-2xl font-semibold text-[var(--color-text-primary)]">{m['settings.api_keys.title']()}</h1>
		<button
			onclick={() => { resetForm(); showCreate = true; }}
			class="flex items-center gap-1 rounded-md bg-[var(--app-accent)] px-3 py-1.5 text-sm text-[var(--app-accent-foreground)] hover:bg-[var(--app-accent-hover)]"
		>
			<Plus size={14} />
			{m['settings.api_keys.new']()}
		</button>
	</div>
	<p class="mt-2 text-xs text-[var(--color-text-tertiary)]">{m['settings.api_keys.subtitle']()}</p>

	<div class="mt-8">
		{#if loading}
			<div class="flex h-64 items-center justify-center">
			</div>
		{:else if keys.length === 0}
			<EmptyState
				title={m['settings.api_keys.no_keys']()}
				description={m['settings.api_keys.no_keys_desc']()}
				action={{ label: m['settings.api_keys.new'](), onclick: () => { resetForm(); showCreate = true; } }}
			/>
		{:else}
			<div class="rounded-lg border border-[var(--app-border)] bg-[var(--color-bg-secondary)]">
				{#each keys as key, i}
					<div class="flex items-center gap-4 px-5 py-4 {i > 0 ? 'border-t border-[var(--app-border)]' : ''}">
						<div class="flex-1 min-w-0">
							<div class="flex items-center gap-2">
								<KeyRound size={14} class="shrink-0 text-[var(--color-text-tertiary)]" />
								<span class="truncate text-sm font-medium text-[var(--color-text-primary)]">{key.name}</span>
								<span class="rounded bg-[var(--color-bg)] px-1.5 py-0.5 font-mono text-[11px] text-[var(--color-text-tertiary)]">{key.key_prefix}&hellip;</span>
								{#if key.revoked_at}
									<Badge variant="outline" class="text-[10px]">{m['settings.api_keys.revoked']()}</Badge>
								{:else if isExpired(key)}
									<Badge variant="outline" class="text-[10px]">{m['settings.api_keys.expired']()}</Badge>
								{/if}
							</div>
							<div class="mt-1 flex flex-wrap items-center gap-1.5">
								{#each key.scopes as scope}
									<Badge variant="secondary" class="text-[10px]">{scope}</Badge>
								{/each}
							</div>
							<p class="mt-1 text-xs text-[var(--color-text-tertiary)]">
								{#if key.last_used_at}
									{m['settings.api_keys.last_used']({ time: formatRelativeTime(key.last_used_at, getLocale()) })}
								{:else}
									{m['settings.api_keys.never_used']()}
								{/if}
								{#if key.expires_at}
									&middot; {m['settings.api_keys.expires']({ time: formatRelativeTime(key.expires_at, getLocale()) })}
								{/if}
							</p>
						</div>
						<div class="flex items-center gap-2">
							<Button
								variant="ghost"
								size="icon-sm"
								onclick={() => handleRevoke(key)}
								disabled={!!key.revoked_at}
								class="text-[var(--color-text-tertiary)] hover:text-[var(--color-error)]"
							>
								<Trash2 size={14} />
							</Button>
						</div>
					</div>
				{/each}
			</div>
		{/if}
	</div>
</div>

<Dialog.Root bind:open={showCreate}>
	<Dialog.Content class="sm:max-w-[480px] border-[var(--app-border)] bg-[var(--color-bg-secondary)] p-0 overflow-hidden rounded-xl">
		<form onsubmit={handleCreate}>
			<div class="px-5 pt-5 pb-4 space-y-4">
				<div>
					<h2 class="text-base font-semibold text-[var(--color-text-primary)]">{m['settings.api_keys.create_title']()}</h2>
					<p class="mt-0.5 text-xs text-[var(--color-text-tertiary)]">{m['settings.api_keys.create_desc']()}</p>
				</div>

				<div class="space-y-1.5">
					<Label class="text-xs text-[var(--color-text-secondary)]">{m['settings.api_keys.name']()}</Label>
					<Input
						bind:value={newName}
						placeholder={m['settings.api_keys.name_placeholder']()}
						required
						maxlength={100}
						class="bg-[var(--color-bg)] border-[var(--app-border)] text-[var(--color-text-primary)]"
					/>
				</div>

				<div class="space-y-1.5">
					<Label class="text-xs text-[var(--color-text-secondary)]">{m['settings.api_keys.expires_in_days']()}</Label>
					<Input
						bind:value={newExpiresInDays}
						type="number"
						min="1"
						max="3650"
						placeholder={m['settings.api_keys.expires_placeholder']()}
						class="bg-[var(--color-bg)] border-[var(--app-border)] text-[var(--color-text-primary)]"
					/>
				</div>

				<div class="flex items-start gap-3 rounded-md border border-[var(--app-border)] px-3 py-3">
					<Switch id="api-key-read-only" bind:checked={newReadOnly} />
					<div>
						<Label for="api-key-read-only" class="text-xs text-[var(--color-text-secondary)]">{m['settings.api_keys.read_only']()}</Label>
						<p class="mt-0.5 text-[10px] text-[var(--color-text-tertiary)]">{m['settings.api_keys.read_only_desc']()}</p>
					</div>
				</div>
			</div>

			<div class="flex justify-end gap-2 border-t border-[var(--app-border)] px-5 py-3">
				<Button variant="outline" size="sm" type="button" onclick={() => (showCreate = false)}>{m['settings.api_keys.cancel']()}</Button>
				<Button size="sm" type="submit" disabled={!newName.trim()}>{m['settings.api_keys.create_button']()}</Button>
			</div>
		</form>
	</Dialog.Content>
</Dialog.Root>

<Dialog.Root bind:open={showToken}>
	<Dialog.Content class="sm:max-w-[560px] border-[var(--app-border)] bg-[var(--color-bg-secondary)] p-0 overflow-hidden rounded-xl">
		<div class="px-5 pt-5 pb-4 space-y-4">
			<div>
				<h2 class="text-base font-semibold text-[var(--color-text-primary)]">{m['settings.api_keys.token_title']()}</h2>
				<p class="mt-0.5 text-xs text-[var(--color-error)]">{m['settings.api_keys.token_shown_once']()}</p>
			</div>
			<div class="flex items-center gap-2">
				<Input
					value={createdToken}
					readonly
					class="flex-1 font-mono text-xs bg-[var(--color-bg)] border-[var(--app-border)] text-[var(--color-text-primary)]"
				/>
				<Button variant="outline" size="sm" onclick={copyToken}>
					{#if copied}
						<Check size={14} />
					{:else}
						<Copy size={14} />
					{/if}
					{copied ? m['settings.api_keys.copied']() : m['settings.api_keys.copy']()}
				</Button>
			</div>
			<p class="text-[10px] text-[var(--color-text-tertiary)]">{m['settings.api_keys.mcp_hint']()}</p>
		</div>
		<div class="flex justify-end border-t border-[var(--app-border)] px-5 py-3">
			<Button size="sm" onclick={() => (showToken = false)}>{m['settings.api_keys.done']()}</Button>
		</div>
	</Dialog.Content>
</Dialog.Root>
