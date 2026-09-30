<script lang="ts">
	import { page } from '$app/state';
	import { appToast } from '$lib/features/toast/toast';
	import { m } from '$lib/paraglide/messages.js';
	import { Button } from '$lib/components/ui/button';
	import * as Select from '$lib/components/ui/select';
	import { getAISettings, updateAISettings, testAIConnection } from '$lib/api/ai-settings';
	import { getWorkspace } from '$lib/api/workspaces';
	import { authState } from '$lib/features/auth/auth.state.svelte';
	import { aiSettingsState } from '$lib/features/ai/ai-settings.state.svelte';
	import type { AISettings, UpdateAISettingsRequest } from '$lib/types/ai-settings';

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
	let testing = $state(false);
	let removeKey = $state(false);
	let loadFailed = $state(false);
	let connectionResult = $state<{ model: string; latency_ms: number } | null>(null);
	let requestVersion = 0;
	const connectionChanged = $derived(
		!!settings &&
			(baseUrl.trim() !== settings.base_url ||
				model.trim() !== settings.model ||
				provider !== settings.provider ||
				!!apiKey.trim() ||
				removeKey)
	);
	const canTest = $derived(
		isOwner &&
			!!settings?.base_url &&
			!!settings?.model &&
			settings.has_api_key &&
			!connectionChanged &&
			!saving &&
			!testing
	);

	$effect(() => {
		const currentSlug = slug;
		const version = ++requestVersion;
		settings = null;
		isOwner = false;
		apiKey = '';
		removeKey = false;
		saving = false;
		testing = false;
		connectionResult = null;
		loadFailed = false;
		if (currentSlug) void loadSettings(currentSlug, version);
		return () => {
			if (requestVersion === version) requestVersion++;
		};
	});

	async function loadSettings(currentSlug: string, version: number) {
		try {
			const loaded = await getAISettings(currentSlug);
			if (version !== requestVersion) return;
			settings = loaded;
			provider = settings.provider;
			baseUrl = settings.base_url;
			model = settings.model;
			prompt = settings.description_expand_prompt;
			issueCopyPrompt = settings.issue_copy_prompt;
			aiSettingsState.markConfigured(currentSlug, !!settings.base_url && !!settings.model && settings.has_api_key);
		} catch (err: any) {
			if (version !== requestVersion) return;
			loadFailed = true;
			appToast.apiError(err, m['settings.ai.failed_load']());
			return;
		}
		try {
			// Members can read the configuration; only the owner can edit it.
			const workspace = await getWorkspace(currentSlug);
			if (version !== requestVersion) return;
			isOwner =
				workspace.current_user_role === 'owner' ||
				(workspace.owner_id && authState.user?.id === workspace.owner_id) ||
				false;
		} catch {
			if (version === requestVersion) isOwner = false;
		}
	}

	async function saveSettings() {
		if (!isOwner || saving || testing) return;
		const currentSlug = slug;
		const version = requestVersion;
		saving = true;
		try {
			const payload: UpdateAISettingsRequest = {
				provider,
				base_url: baseUrl.trim(),
				model: model.trim(),
				description_expand_prompt: prompt.trim(),
				issue_copy_prompt: issueCopyPrompt.trim()
			};
			if (removeKey) payload.api_key = null;
			else if (apiKey.trim()) payload.api_key = apiKey.trim();
			const updated = await updateAISettings(currentSlug, payload);
			if (version !== requestVersion) return;
			settings = updated;
			provider = settings.provider;
			baseUrl = settings.base_url;
			model = settings.model;
			prompt = settings.description_expand_prompt;
			issueCopyPrompt = settings.issue_copy_prompt;
			apiKey = '';
			removeKey = false;
			connectionResult = null;
			aiSettingsState.markConfigured(currentSlug, !!settings.base_url && !!settings.model && settings.has_api_key);
			appToast.success(m['settings.ai.updated']());
		} catch (err: any) {
			if (version === requestVersion) appToast.apiError(err, m['settings.ai.failed_update']());
		} finally {
			if (version === requestVersion) saving = false;
		}
	}

	async function testConnection() {
		if (!canTest) return;
		const currentSlug = slug;
		const version = requestVersion;
		testing = true;
		connectionResult = null;
		try {
			const result = await testAIConnection(currentSlug);
			if (version === requestVersion && !connectionChanged) connectionResult = result;
		} catch (err: unknown) {
			if (version === requestVersion) appToast.apiError(err, m['settings.ai.test_failed']());
		} finally {
			if (version === requestVersion) testing = false;
		}
	}

	function resetPrompt() {
		if (settings) prompt = settings.default_prompt;
	}

	function resetIssueCopyPrompt() {
		if (settings) issueCopyPrompt = settings.default_issue_copy_prompt;
	}
</script>

<div class="mx-auto max-w-2xl px-4 py-6 sm:px-8 sm:py-10">
	<h1 class="text-2xl font-semibold text-[var(--color-text-primary)]">{m['settings.ai.title']()}</h1>
	<p class="mt-1 text-sm text-[var(--color-text-tertiary)]">{m['settings.ai.desc']()}</p>

	{#if settings}
		{#if !isOwner}
			<div
				class="mt-6 rounded-lg border border-[var(--app-border)] bg-[var(--color-bg-secondary)] px-5 py-3 text-sm text-[var(--color-text-secondary)]"
			>
				{m['settings.ai.owner_only']()}
			</div>
		{/if}
		<div class="mt-8 rounded-lg border border-[var(--app-border)] bg-[var(--color-bg-secondary)]">
			<div class="flex flex-col items-stretch justify-between gap-4 px-5 py-4 sm:flex-row sm:items-center">
				<div>
					<p class="text-sm font-medium text-[var(--color-text-primary)]">{m['settings.ai.provider']()}</p>
					<p class="text-xs text-[var(--color-text-tertiary)]">{m['settings.ai.provider_desc']()}</p>
				</div>
				<Select.Root type="single" value={provider} onValueChange={(v) => v && isOwner && (provider = v)}>
					<Select.Trigger size="sm" disabled={!isOwner || saving || testing} class="w-[190px]"
						>{m['settings.ai.provider_label']()}</Select.Trigger
					>
					<Select.Content>
						<Select.Item value="openai_compatible">{m['settings.ai.provider_label']()}</Select.Item>
					</Select.Content>
				</Select.Root>
			</div>

			<div class="border-t border-[var(--app-border)]"></div>

			<div class="flex flex-col items-stretch justify-between gap-4 px-5 py-4 sm:flex-row sm:items-center">
				<div>
					<p class="text-sm font-medium text-[var(--color-text-primary)]">{m['settings.ai.base_url']()}</p>
					<p class="text-xs text-[var(--color-text-tertiary)]">{m['settings.ai.base_url_example']()}</p>
				</div>
				<input
					type="url"
					aria-label={m['settings.ai.base_url']()}
					bind:value={baseUrl}
					disabled={!isOwner || saving || testing}
					placeholder={m['settings.ai.base_url_placeholder']()}
					class="w-full sm:w-[300px] rounded-md border border-[var(--app-border)] bg-[var(--color-bg)] px-3 py-1.5 text-sm text-[var(--color-text-primary)] outline-none focus:border-[var(--app-accent)] disabled:opacity-60"
				/>
			</div>

			<div class="border-t border-[var(--app-border)]"></div>

			<div class="flex flex-col items-stretch justify-between gap-4 px-5 py-4 sm:flex-row sm:items-center">
				<div>
					<p class="text-sm font-medium text-[var(--color-text-primary)]">{m['settings.ai.model']()}</p>
					<p class="text-xs text-[var(--color-text-tertiary)]">{m['settings.ai.model_desc']()}</p>
				</div>
				<input
					type="text"
					aria-label={m['settings.ai.model']()}
					bind:value={model}
					disabled={!isOwner || saving || testing}
					placeholder={m['settings.ai.model_placeholder']()}
					class="w-full sm:w-[240px] rounded-md border border-[var(--app-border)] bg-[var(--color-bg)] px-3 py-1.5 text-sm text-[var(--color-text-primary)] outline-none focus:border-[var(--app-accent)] disabled:opacity-60"
				/>
			</div>

			<div class="border-t border-[var(--app-border)]"></div>

			<div class="flex flex-col items-stretch justify-between gap-4 px-5 py-4 sm:flex-row sm:items-center">
				<div>
					<p class="text-sm font-medium text-[var(--color-text-primary)]">{m['settings.ai.api_key']()}</p>
					<p class="text-xs text-[var(--color-text-tertiary)]">
						{settings.has_api_key ? m['settings.ai.key_configured']() : m['settings.ai.key_not_configured']()}
					</p>
				</div>
				<input
					type="password"
					aria-label={m['settings.ai.api_key']()}
					bind:value={apiKey}
					disabled={!isOwner || removeKey || saving || testing}
					placeholder={settings.has_api_key
						? m['settings.ai.key_configured_placeholder']()
						: m['settings.ai.key_placeholder']()}
					class="w-full sm:w-[240px] rounded-md border border-[var(--app-border)] bg-[var(--color-bg)] px-3 py-1.5 text-sm text-[var(--color-text-primary)] outline-none focus:border-[var(--app-accent)] disabled:opacity-60"
				/>
			</div>
			{#if isOwner && settings.has_api_key}
				<div class="flex flex-wrap items-center gap-3 px-5 pb-4">
					<Button
						variant="outline"
						size="sm"
						onclick={() => {
							removeKey = !removeKey;
							apiKey = '';
							connectionResult = null;
						}}
						disabled={saving || testing}
					>
						{removeKey ? m['settings.ai.keep_key']() : m['settings.ai.remove_key']()}
					</Button>
					{#if removeKey}<p class="text-xs text-[var(--color-text-tertiary)]">
							{m['settings.ai.remove_key_pending']()}
						</p>{/if}
				</div>
			{/if}
		</div>

		{#if isOwner}
			<div class="mt-4 flex flex-wrap items-center gap-3">
				<Button variant="outline" onclick={testConnection} disabled={!canTest}>
					{testing ? m['settings.ai.testing']() : m['settings.ai.test_connection']()}
				</Button>
				<p class="text-xs text-[var(--color-text-tertiary)]">{m['settings.ai.test_saved']()}</p>
			</div>
			{#if connectionResult && !connectionChanged}
				<p
					role="status"
					class="mt-3 rounded-lg border border-[var(--app-border)] px-4 py-3 text-sm text-[var(--color-text-primary)]"
				>
					{m['settings.ai.test_success']({ model: connectionResult.model, ms: connectionResult.latency_ms })}
				</p>
			{/if}
		{/if}

		<div class="mt-8">
			<div class="flex items-center justify-between gap-4">
				<div>
					<h2 class="text-sm font-medium text-[var(--color-text-primary)]">{m['settings.ai.desc_prompt']()}</h2>
					<p class="text-xs text-[var(--color-text-tertiary)]">{m['settings.ai.desc_prompt_desc']()}</p>
				</div>
				{#if isOwner}
					<Button variant="outline" size="sm" onclick={resetPrompt} disabled={saving || testing}
						>{m['settings.ai.reset']()}</Button
					>
				{/if}
			</div>
			<textarea
				bind:value={prompt}
				aria-label={m['settings.ai.desc_prompt']()}
				rows="8"
				disabled={!isOwner || saving || testing}
				class="mt-3 w-full rounded-lg border border-[var(--app-border)] bg-[var(--color-bg-secondary)] px-3 py-2 text-sm text-[var(--color-text-primary)] outline-none focus:border-[var(--app-accent)] disabled:opacity-60"
			></textarea>
		</div>

		<div class="mt-8">
			<div class="flex items-center justify-between gap-4">
				<div>
					<h2 class="text-sm font-medium text-[var(--color-text-primary)]">{m['settings.ai.copy_prompt']()}</h2>
					<p class="text-xs text-[var(--color-text-tertiary)]">
						{m['settings.ai.copy_prompt_variables']({
							identifier: '{{issue_identifier}}',
							title: '{{issue_title}}',
							teamKey: '{{team_key}}',
							teamName: '{{team_name}}',
							xml: '{{issue_xml}}'
						})}
					</p>
				</div>
				{#if isOwner}
					<Button variant="outline" size="sm" onclick={resetIssueCopyPrompt} disabled={saving || testing}
						>{m['settings.ai.reset']()}</Button
					>
				{/if}
			</div>
			<textarea
				bind:value={issueCopyPrompt}
				aria-label={m['settings.ai.copy_prompt']()}
				rows="8"
				disabled={!isOwner || saving || testing}
				class="mt-3 w-full rounded-lg border border-[var(--app-border)] bg-[var(--color-bg-secondary)] px-3 py-2 font-mono text-sm text-[var(--color-text-primary)] outline-none focus:border-[var(--app-accent)] disabled:opacity-60"
			></textarea>
		</div>

		{#if isOwner}
			<div class="mt-4 flex justify-end">
				<Button onclick={saveSettings} disabled={saving || testing}
					>{saving ? m['settings.saving']() : m['settings.ai.save_settings']()}</Button
				>
			</div>
		{/if}
	{:else if loadFailed}
		<p role="alert" class="mt-8 text-sm text-[var(--color-text-secondary)]">{m['settings.ai.failed_load']()}</p>
		<Button
			class="mt-3"
			variant="outline"
			onclick={() => {
				loadFailed = false;
				void loadSettings(slug, requestVersion);
			}}>{m['settings.ai.retry_load']()}</Button
		>
	{:else}
		<div class="mt-8 flex justify-center py-8">
			<div
				class="h-5 w-5 animate-spin rounded-full border-2 border-[var(--color-text-tertiary)] border-t-transparent"
			></div>
		</div>
	{/if}
</div>
