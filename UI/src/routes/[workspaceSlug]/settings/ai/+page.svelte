<script lang="ts">
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { appToast } from '$lib/features/toast/toast';
	import { m } from '$lib/paraglide/messages.js';
	import { getLocale, setLocale } from '$lib/paraglide/runtime.js';
	import { Button } from '$lib/components/ui/button';
	import * as Select from '$lib/components/ui/select';
	import { getAISettings, updateAISettings } from '$lib/api/ai-settings';
	import { getWorkspace } from '$lib/api/workspaces';
	import { authState } from '$lib/features/auth/auth.state.svelte';
	import { aiSettingsState } from '$lib/features/ai/ai-settings.state.svelte';
	import type { AISettings } from '$lib/types/ai-settings';

	const slug = $derived(page.params.workspaceSlug ?? '');

	let settings = $state<AISettings | null>(null);
	let isOwner = $state(false);
	let provider = $state('openai_compatible');
	let baseUrl = $state('');
	let model = $state('');
	let apiKey = $state('');
	let prompt = $state('');
	let issueCopyPrompt = $state('');
	let saving = $state(false);

	onMount(async () => {
		try {
			settings = await getAISettings(slug);
			provider = settings.provider;
			baseUrl = settings.base_url;
			model = settings.model;
			prompt = settings.description_expand_prompt;
			issueCopyPrompt = settings.issue_copy_prompt;
			aiSettingsState.markConfigured(
				slug,
				!!settings.base_url && !!settings.model && settings.has_api_key
			);
		} catch (err: any) {
			appToast.apiError(err, m['settings.ai.failed_load']());
			return;
		}
		try {
			// Members can read the configuration; only the owner can edit it.
			const workspace = await getWorkspace(slug);
			isOwner =
				workspace.current_user_role === 'owner' ||
				(workspace.owner_id && authState.user?.id === workspace.owner_id) ||
				false;
		} catch {
			isOwner = false;
		}
	});

	async function saveSettings() {
		saving = true;
		try {
			const payload: any = {
				provider,
				base_url: baseUrl.trim(),
				model: model.trim(),
				description_expand_prompt: prompt.trim(),
				issue_copy_prompt: issueCopyPrompt.trim()
			};
			if (apiKey.trim()) payload.api_key = apiKey.trim();
			settings = await updateAISettings(slug, payload);
			provider = settings.provider;
			baseUrl = settings.base_url;
			model = settings.model;
			prompt = settings.description_expand_prompt;
			issueCopyPrompt = settings.issue_copy_prompt;
			apiKey = '';
			aiSettingsState.markConfigured(
				slug,
				!!settings.base_url && !!settings.model && settings.has_api_key
			);
			appToast.success(m['settings.ai.updated']());
		} catch (err: any) {
			appToast.apiError(err, m['settings.ai.failed_update']());
		} finally {
			saving = false;
		}
	}

	function resetPrompt() {
		if (settings) prompt = settings.default_prompt;
	}

	function resetIssueCopyPrompt() {
		if (settings) issueCopyPrompt = settings.default_issue_copy_prompt;
	}
</script>

<div class="mx-auto max-w-2xl px-8 py-10">
	<h1 class="text-2xl font-semibold text-[var(--color-text-primary)]">{m['settings.ai.title']()}</h1>
	<p class="mt-1 text-sm text-[var(--color-text-tertiary)]">{m['settings.ai.desc']()}</p>

	{#if settings}
		{#if !isOwner}
			<div class="mt-6 rounded-lg border border-[var(--app-border)] bg-[var(--color-bg-secondary)] px-5 py-3 text-sm text-[var(--color-text-secondary)]">
				{m['settings.ai.owner_only']()}
			</div>
		{/if}
		<div class="mt-8 rounded-lg border border-[var(--app-border)] bg-[var(--color-bg-secondary)]">
			<div class="flex items-center justify-between gap-4 px-5 py-4">
				<div>
					<p class="text-sm font-medium text-[var(--color-text-primary)]">{m['settings.ai.provider']()}</p>
					<p class="text-xs text-[var(--color-text-tertiary)]">{m['settings.ai.provider_desc']()}</p>
				</div>
				<Select.Root type="single" value={provider} onValueChange={(v) => v && isOwner && (provider = v)}>
					<Select.Trigger size="sm" disabled={!isOwner} class="w-[190px]">{m['settings.ai.provider_label']()}</Select.Trigger>
					<Select.Content>
						<Select.Item value="openai_compatible">{m['settings.ai.provider_label']()}</Select.Item>
					</Select.Content>
				</Select.Root>
			</div>

			<div class="border-t border-[var(--app-border)]"></div>

			<div class="flex items-center justify-between gap-4 px-5 py-4">
				<div>
					<p class="text-sm font-medium text-[var(--color-text-primary)]">{m['settings.ai.base_url']()}</p>
					<p class="text-xs text-[var(--color-text-tertiary)]">{m['settings.ai.base_url_example']()}</p>
				</div>
				<input
					type="url"
					bind:value={baseUrl}
					disabled={!isOwner}
					placeholder={m['settings.ai.base_url_placeholder']()}
					class="w-[300px] rounded-md border border-[var(--app-border)] bg-[var(--color-bg)] px-3 py-1.5 text-sm text-[var(--color-text-primary)] outline-none focus:border-[var(--app-accent)] disabled:opacity-60"
				/>
			</div>

			<div class="border-t border-[var(--app-border)]"></div>

			<div class="flex items-center justify-between gap-4 px-5 py-4">
				<div>
					<p class="text-sm font-medium text-[var(--color-text-primary)]">{m['settings.ai.model']()}</p>
					<p class="text-xs text-[var(--color-text-tertiary)]">{m['settings.ai.model_desc']()}</p>
				</div>
				<input
					type="text"
					bind:value={model}
					disabled={!isOwner}
					placeholder={m['settings.ai.model_placeholder']()}
					class="w-[240px] rounded-md border border-[var(--app-border)] bg-[var(--color-bg)] px-3 py-1.5 text-sm text-[var(--color-text-primary)] outline-none focus:border-[var(--app-accent)] disabled:opacity-60"
				/>
			</div>

			<div class="border-t border-[var(--app-border)]"></div>

			<div class="flex items-center justify-between gap-4 px-5 py-4">
				<div>
					<p class="text-sm font-medium text-[var(--color-text-primary)]">{m['settings.ai.api_key']()}</p>
					<p class="text-xs text-[var(--color-text-tertiary)]">
						{settings.has_api_key ? m['settings.ai.key_configured']() : m['settings.ai.key_not_configured']()}
					</p>
				</div>
				<input
					type="password"
					bind:value={apiKey}
					disabled={!isOwner}
					placeholder={settings.has_api_key ? m['settings.ai.key_configured_placeholder']() : m['settings.ai.key_placeholder']()}
					class="w-[240px] rounded-md border border-[var(--app-border)] bg-[var(--color-bg)] px-3 py-1.5 text-sm text-[var(--color-text-primary)] outline-none focus:border-[var(--app-accent)] disabled:opacity-60"
				/>
			</div>
		</div>

		<div class="mt-8">
			<div class="flex items-center justify-between gap-4">
				<div>
					<h2 class="text-sm font-medium text-[var(--color-text-primary)]">{m['settings.ai.desc_prompt']()}</h2>
					<p class="text-xs text-[var(--color-text-tertiary)]">{m['settings.ai.desc_prompt_desc']()}</p>
				</div>
				{#if isOwner}
					<Button variant="outline" size="sm" onclick={resetPrompt}>{m['settings.ai.reset']()}</Button>
				{/if}
			</div>
			<textarea
				bind:value={prompt}
				rows="8"
				disabled={!isOwner}
				class="mt-3 w-full rounded-lg border border-[var(--app-border)] bg-[var(--color-bg-secondary)] px-3 py-2 text-sm text-[var(--color-text-primary)] outline-none focus:border-[var(--app-accent)] disabled:opacity-60"
			></textarea>
		</div>

		<div class="mt-8">
			<div class="flex items-center justify-between gap-4">
				<div>
					<h2 class="text-sm font-medium text-[var(--color-text-primary)]">{m['settings.ai.copy_prompt']()}</h2>
					<p class="text-xs text-[var(--color-text-tertiary)]">
						{m['settings.ai.copy_prompt_desc']({ title: '{title}', description: '{description}', identifier: '{identifier}' })}
					</p>
				</div>
				{#if isOwner}
					<Button variant="outline" size="sm" onclick={resetIssueCopyPrompt}>{m['settings.ai.reset']()}</Button>
				{/if}
			</div>
			<textarea
				bind:value={issueCopyPrompt}
				rows="8"
				disabled={!isOwner}
				class="mt-3 w-full rounded-lg border border-[var(--app-border)] bg-[var(--color-bg-secondary)] px-3 py-2 font-mono text-sm text-[var(--color-text-primary)] outline-none focus:border-[var(--app-accent)] disabled:opacity-60"
			></textarea>
		</div>

		{#if isOwner}
			<div class="mt-4 flex justify-end">
				<Button onclick={saveSettings} disabled={saving}>{saving ? m['settings.saving']() : m['settings.ai.save_settings']()}</Button>
			</div>
		{/if}
	{:else}
		<div class="mt-8 flex justify-center py-8">
			<div class="h-5 w-5 animate-spin rounded-full border-2 border-[var(--color-text-tertiary)] border-t-transparent"></div>
		</div>
	{/if}
</div>
