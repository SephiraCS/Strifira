<script lang="ts">
	import { untrack } from 'svelte';
	import { goto } from '$app/navigation';
	import { delegateDevBoxJob, listDevBoxes } from '$lib/api/dev-boxes';
	import type { DevBox, DevBoxProviderId } from '$lib/types/dev-box';
	import type { Issue } from '$lib/types/issue';
	import * as Dialog from '$lib/components/ui/dialog';
	import * as Select from '$lib/components/ui/select';
	import { Button } from '$lib/components/ui/button';
	import { Input } from '$lib/components/ui/input';
	import { Textarea } from '$lib/components/ui/textarea';
	import { appToast } from '$lib/features/toast/toast';
	import { m } from '$lib/paraglide/messages.js';
	import { parseTestCommand, providerName } from './dev-boxes';
	import { validateAcceptanceCriteria } from './acceptance-criteria';

	let { open = $bindable(false), slug, issue }: { open: boolean; slug: string; issue: Issue } = $props();
	let boxes = $state<DevBox[]>([]);
	let selectedBoxId = $state('');
	let provider = $state<DevBoxProviderId | ''>('');
	let extraInstructions = $state('');
	let criteria = $state('');
	let testCommand = $state('');
	let runtimeMinutes = $state(60);
	let loading = $state(false);
	let failed = $state(false);
	let busy = $state(false);
	let validationError = $state('');
	let criteriaError = $state('');
	let formVersion = 0;
	const availableBoxes = $derived(boxes.filter((box) => box.enabled && box.online && box.providers.length > 0));
	const selectedBox = $derived(availableBoxes.find((box) => box.id === selectedBoxId));
	const providers = $derived(selectedBox?.providers ?? []);
	const canSubmit = $derived(
		!loading && !busy && !!selectedBox && providers.some((item) => item.id === provider) && !!testCommand.trim()
	);

	$effect(() => {
		const currentOpen = open;
		const currentSlug = slug;
		const currentIssueId = issue.id;
		untrack(() => {
			const version = ++formVersion;
			boxes = [];
			selectedBoxId = '';
			provider = '';
			extraInstructions = '';
			criteria = '';
			testCommand = '';
			runtimeMinutes = 60;
			busy = false;
			failed = false;
			validationError = '';
			criteriaError = '';
			if (currentOpen) void loadBoxes(currentSlug, currentIssueId, version);
		});
		return () => {
			formVersion++;
		};
	});

	async function loadBoxes(currentSlug = slug, currentIssueId = issue.id, version = formVersion) {
		loading = true;
		failed = false;
		try {
			const result = await listDevBoxes(currentSlug);
			if (version !== formVersion || !open || slug !== currentSlug || issue.id !== currentIssueId) return;
			boxes = result ?? [];
			selectBox(boxes.find((box) => box.enabled && box.online && box.providers.length > 0)?.id ?? '');
		} catch (error) {
			if (version !== formVersion) return;
			failed = true;
			appToast.apiError(error, m['dev_boxes.failed_load']());
		} finally {
			if (version === formVersion) loading = false;
		}
	}

	function selectBox(value: string) {
		selectedBoxId = value;
		provider = boxes.find((box) => box.id === value)?.providers[0]?.id ?? '';
	}

	async function submit(event: SubmitEvent) {
		event.preventDefault();
		if (!canSubmit || !provider) return;
		validationError = '';
		criteriaError = '';
		const validatedCriteria = validateAcceptanceCriteria(criteria);
		if (validatedCriteria.error) {
			criteriaError =
				validatedCriteria.error === 'count'
					? m['dev_boxes.criteria_count_invalid']()
					: validatedCriteria.error === 'length'
						? m['dev_boxes.criteria_length_invalid']({ line: validatedCriteria.line })
						: m['dev_boxes.criteria_nul_invalid']({ line: validatedCriteria.line });
			return;
		}
		let command: string[];
		try {
			command = parseTestCommand(testCommand);
		} catch {
			validationError = m['dev_boxes.test_command_invalid']();
			return;
		}
		if (!Number.isInteger(runtimeMinutes) || runtimeMinutes < 1 || runtimeMinutes > 1440) {
			validationError = m['dev_boxes.runtime_invalid']();
			return;
		}
		const version = formVersion;
		const currentSlug = slug;
		const issueId = issue.id;
		busy = true;
		try {
			const job = await delegateDevBoxJob(currentSlug, selectedBoxId, {
				issue_id: issueId,
				provider,
				prompt: extraInstructions.trim(),
				acceptance_criteria: validatedCriteria.criteria,
				test_command: command,
				max_runtime_seconds: runtimeMinutes * 60
			});
			if (version !== formVersion || !open || slug !== currentSlug || issue.id !== issueId) return;
			open = false;
			await goto(`/${currentSlug}/dev-box-jobs/${job.id}`);
		} catch (error) {
			if (version === formVersion) appToast.apiError(error, m['dev_boxes.failed_delegate']());
		} finally {
			if (version === formVersion) busy = false;
		}
	}
</script>

<Dialog.Root bind:open>
	<Dialog.Content
		class="max-h-[85vh] overflow-y-auto sm:max-w-xl"
		showCloseButton={!busy}
		escapeKeydownBehavior={busy ? 'ignore' : 'close'}
		interactOutsideBehavior={busy ? 'ignore' : 'close'}
	>
		<form onsubmit={submit} class="space-y-4">
			<Dialog.Header
				><Dialog.Title>{m['dev_boxes.delegate']()}</Dialog.Title><Dialog.Description
					>{m['dev_boxes.delegate_description']({ issue: issue.identifier })}</Dialog.Description
				></Dialog.Header
			>
			{#if loading}
				<p class="text-sm text-muted-foreground">{m['dev_boxes.loading']()}</p>
			{:else if failed}
				<Button type="button" variant="outline" onclick={() => loadBoxes()}>{m['dev_boxes.refresh']()}</Button>
			{:else if availableBoxes.length === 0}
				<p class="text-sm text-muted-foreground">{m['dev_boxes.no_ready_boxes']()}</p>
				<a class="text-sm text-primary underline" href="/{slug}/dev-boxes">{m['dev_boxes.manage']()}</a><Button
					type="button"
					variant="outline"
					onclick={() => loadBoxes()}>{m['dev_boxes.refresh']()}</Button
				>
			{:else}
				<div class="grid gap-3 sm:grid-cols-2">
					<div class="space-y-1">
						<label for="delegate-dev-box" class="text-sm font-medium">{m['dev_boxes.box']()}</label><Select.Root
							type="single"
							value={selectedBoxId}
							onValueChange={selectBox}
							><Select.Trigger id="delegate-dev-box" aria-label={m['dev_boxes.box']()} class="w-full"
								>{selectedBox?.name ?? ''}</Select.Trigger
							><Select.Content
								>{#each availableBoxes as box}<Select.Item value={box.id} label={box.name}>{box.name}</Select.Item
									>{/each}</Select.Content
							></Select.Root
						>
					</div>
					<div class="space-y-1">
						<label for="delegate-provider" class="text-sm font-medium">{m['dev_boxes.provider']()}</label><Select.Root
							type="single"
							value={provider}
							onValueChange={(value) => (provider = value as DevBoxProviderId)}
							><Select.Trigger id="delegate-provider" aria-label={m['dev_boxes.provider']()} class="w-full"
								>{provider ? providerName(provider) : ''}</Select.Trigger
							><Select.Content
								>{#each providers as item}<Select.Item value={item.id} label={providerName(item.id)}
										>{providerName(item.id)}</Select.Item
									>{/each}</Select.Content
							></Select.Root
						>
					</div>
				</div>
				<label class="block space-y-1"
					><span class="text-sm font-medium">{m['dev_boxes.instructions']()}</span><Textarea
						bind:value={extraInstructions}
						rows={4}
						maxlength={16000}
					/></label
				>
				<label class="block space-y-1"
					><span class="text-sm font-medium">{m['dev_boxes.acceptance_criteria']()}</span><Textarea
						id="delegate-criteria"
						bind:value={criteria}
						oninput={() => (criteriaError = '')}
						aria-invalid={!!criteriaError}
						aria-describedby={criteriaError ? 'delegate-criteria-error' : 'delegate-criteria-help'}
						rows={3}
					/><span id="delegate-criteria-help" class="block text-xs text-muted-foreground"
						>{m['dev_boxes.criteria_help']()}</span
					></label
				>
				{#if criteriaError}<p id="delegate-criteria-error" role="alert" class="text-sm text-destructive">
						{criteriaError}
					</p>{/if}
				<label class="block space-y-1"
					><span class="text-sm font-medium">{m['dev_boxes.test_command']()}</span><Input
						bind:value={testCommand}
						required
						placeholder={JSON.stringify(['go', 'test', './...'])}
						aria-invalid={!!validationError}
					/><span class="block text-xs text-muted-foreground">{m['dev_boxes.test_command_help']()}</span></label
				>
				<label class="block space-y-1"
					><span class="text-sm font-medium">{m['dev_boxes.runtime_minutes']()}</span><Input
						bind:value={runtimeMinutes}
						type="number"
						required
						min={1}
						max={1440}
						step={1}
					/></label
				>
				<p class="text-xs text-muted-foreground">{m['dev_boxes.review_only']()}</p>
			{/if}
			{#if validationError}<p role="alert" class="text-sm text-destructive">{validationError}</p>{/if}
			<div class="flex justify-end gap-2 border-t pt-4">
				<Button type="button" variant="outline" disabled={busy} onclick={() => (open = false)}
					>{m['common.cancel']()}</Button
				><Button type="submit" disabled={!canSubmit}
					>{busy ? m['dev_boxes.delegating']() : m['dev_boxes.delegate_action']()}</Button
				>
			</div>
		</form>
	</Dialog.Content>
</Dialog.Root>
