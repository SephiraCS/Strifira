<script lang="ts">
	import * as Dialog from '$lib/components/ui/dialog';
	import { Button } from '$lib/components/ui/button';
	import Crop from '@lucide/svelte/icons/crop';
	import RefreshCw from '@lucide/svelte/icons/refresh-cw';
	import Download from '@lucide/svelte/icons/download';
	import FolderOpen from '@lucide/svelte/icons/folder-open';
	import MessageSquarePlus from '@lucide/svelte/icons/message-square-plus';
	import Search from '@lucide/svelte/icons/search';
	import { m } from '$lib/paraglide/messages.js';
	import { page } from '$app/state';
	import type { Issue } from '$lib/types/issue';
	import { listIssues } from '$lib/api/issues';
	import { screenshotCapture, type CaptureDestination } from './screenshot.state.svelte';
	import CropStage from './CropStage.svelte';

	type DestinationOption = CaptureDestination | 'another-issue';

	let destination = $state<DestinationOption>('current-issue');
	let searchQuery = $state('');
	let searchResults = $state<Issue[]>([]);
	let searching = $state(false);
	let searchTouched = $state(false);
	let targetIssue = $state<Issue | null>(null);

	const currentIssue = $derived(screenshotCapture.currentIssue());
	const slug = $derived(page.params?.workspaceSlug ?? '');

	$effect(() => {
		// Reset the choice each time a new capture reaches the destination stage.
		if (screenshotCapture.stage === 'destination') {
			destination = currentIssue ? 'current-issue' : 'files';
			targetIssue = null;
			searchQuery = '';
			searchResults = [];
			searchTouched = false;
		}
	});

	async function searchIssues() {
		if (!slug || !searchQuery.trim()) {
			searchResults = [];
			return;
		}
		searching = true;
		try {
			const results = await listIssues(slug, { per_page: '50' });
			const q = searchQuery.toLowerCase();
			searchResults = results.data
				.filter(
					(issue) =>
						issue.title.toLowerCase().includes(q) || issue.identifier.toLowerCase().includes(q)
				)
				.slice(0, 10);
		} catch {
			searchResults = [];
		} finally {
			searching = false;
		}
	}

	function choose(option: DestinationOption) {
		destination = option;
		if (option !== 'another-issue') {
			targetIssue = null;
			searchQuery = '';
			searchResults = [];
		}
	}

	async function confirm() {
		if (!screenshotCapture.image) return;
		if (destination === 'current-issue' && currentIssue) {
			await screenshotCapture.submit('current-issue');
		} else if (destination === 'another-issue' && targetIssue) {
			await screenshotCapture.submit('current-issue', targetIssue.identifier);
		} else if (destination === 'files') {
			await screenshotCapture.submit('files');
		} else if (destination === 'download') {
			screenshotCapture.download();
		}
	}

	const confirmDisabled = $derived(
		screenshotCapture.submitting ||
			(destination === 'another-issue' && !targetIssue) ||
			(destination === 'current-issue' && !currentIssue)
	);
</script>

{#if screenshotCapture.stage === 'crop' && screenshotCapture.image}
	<CropStage
		src={screenshotCapture.image.url}
		naturalWidth={screenshotCapture.image.width}
		naturalHeight={screenshotCapture.image.height}
		processing={screenshotCapture.busy}
		onapply={(rect) => void screenshotCapture.applyCrop(rect)}
		oncancel={() => screenshotCapture.cancelCrop()}
	/>
{:else if screenshotCapture.stage === 'preview' && screenshotCapture.image}
	<Dialog.Root
		open
		onOpenChange={(openState) => {
			if (!openState) screenshotCapture.dismiss();
		}}
	>
		<Dialog.Content class="sm:max-w-2xl">
			<Dialog.Header>
				<Dialog.Title>{m['screenshot.preview.title']()}</Dialog.Title>
				<Dialog.Description class="text-xs">
					{screenshotCapture.image.width} × {screenshotCapture.image.height} px
				</Dialog.Description>
			</Dialog.Header>
			<div class="flex max-h-[55vh] items-center justify-center overflow-hidden rounded-md border border-[var(--app-border)] bg-black/30">
				<img
					src={screenshotCapture.image.url}
					alt={m['screenshot.preview.title']()}
					class="max-h-[55vh] w-auto max-w-full object-contain"
				/>
			</div>
			<div class="mt-4 flex flex-wrap items-center justify-between gap-2">
				<div class="flex items-center gap-2">
					<Button variant="outline" size="sm" onclick={() => screenshotCapture.recapture()}>
						<RefreshCw size={14} />
						{m['screenshot.preview.recapture']()}
					</Button>
					<Button variant="outline" size="sm" onclick={() => screenshotCapture.openCrop()}>
						<Crop size={14} />
						{m['screenshot.preview.crop']()}
					</Button>
				</div>
				<div class="flex items-center gap-2">
					<Button variant="ghost" size="sm" onclick={() => screenshotCapture.dismiss()}>
						{m['screenshot.preview.cancel']()}
					</Button>
					<Button size="sm" onclick={() => screenshotCapture.continueToDestination()}>
						{m['screenshot.preview.continue']()}
					</Button>
				</div>
			</div>
		</Dialog.Content>
	</Dialog.Root>
{:else if screenshotCapture.stage === 'destination' && screenshotCapture.image}
	<Dialog.Root
		open
		onOpenChange={(openState) => {
			if (!openState && !screenshotCapture.submitting) screenshotCapture.dismiss();
		}}
	>
		<Dialog.Content class="sm:max-w-lg">
			<Dialog.Header>
				<Dialog.Title>{m['screenshot.destination.title']()}</Dialog.Title>
				<Dialog.Description class="text-xs">
					{screenshotCapture.image.width} × {screenshotCapture.image.height} px
				</Dialog.Description>
			</Dialog.Header>
			<div class="flex flex-col gap-1">
				{#if currentIssue}
					<button
						type="button"
						onclick={() => choose('current-issue')}
						class="flex w-full items-center gap-2.5 rounded-md border px-3 py-2 text-left text-sm {destination ===
						'current-issue'
							? 'border-[var(--app-accent)] bg-[var(--color-bg-hover)]'
							: 'border-[var(--app-border)] hover:bg-[var(--color-bg-hover)]'}"
					>
						<MessageSquarePlus size={14} class="shrink-0 text-[var(--color-text-tertiary)]" />
						{m['screenshot.destination.current_issue']({ identifier: currentIssue.identifier })}
					</button>
				{/if}
				<button
					type="button"
					onclick={() => choose('another-issue')}
					class="flex w-full items-center gap-2.5 rounded-md border px-3 py-2 text-left text-sm {destination ===
					'another-issue'
						? 'border-[var(--app-accent)] bg-[var(--color-bg-hover)]'
						: 'border-[var(--app-border)] hover:bg-[var(--color-bg-hover)]'}"
				>
					<Search size={14} class="shrink-0 text-[var(--color-text-tertiary)]" />
					{m['screenshot.destination.another_issue']()}
				</button>
				<button
					type="button"
					onclick={() => choose('files')}
					class="flex w-full items-center gap-2.5 rounded-md border px-3 py-2 text-left text-sm {destination ===
					'files'
						? 'border-[var(--app-accent)] bg-[var(--color-bg-hover)]'
						: 'border-[var(--app-border)] hover:bg-[var(--color-bg-hover)]'}"
				>
					<FolderOpen size={14} class="shrink-0 text-[var(--color-text-tertiary)]" />
					{m['screenshot.destination.files']()}
				</button>
				<button
					type="button"
					onclick={() => choose('download')}
					class="flex w-full items-center gap-2.5 rounded-md border px-3 py-2 text-left text-sm {destination ===
					'download'
						? 'border-[var(--app-accent)] bg-[var(--color-bg-hover)]'
						: 'border-[var(--app-border)] hover:bg-[var(--color-bg-hover)]'}"
				>
					<Download size={14} class="shrink-0 text-[var(--color-text-tertiary)]" />
					{m['screenshot.destination.download']()}
				</button>
			</div>

			{#if destination === 'another-issue'}
				<div class="relative mt-3">
					<Search size={14} class="absolute left-2.5 top-1/2 -translate-y-1/2 text-[var(--color-text-tertiary)]" />
					<input
						type="text"
						bind:value={searchQuery}
						oninput={() => {
							searchTouched = true;
							void searchIssues();
						}}
						placeholder={m['screenshot.destination.search_placeholder']()}
						class="w-full rounded-md border border-[var(--app-border)] bg-[var(--color-bg-secondary)] py-1.5 pl-8 pr-3 text-sm text-[var(--color-text-primary)] outline-none placeholder:text-[var(--color-text-tertiary)] focus:border-[var(--app-accent)]"
					/>
				</div>
				{#if searchTouched && !searching && searchResults.length === 0}
					<p class="mt-2 text-xs text-[var(--color-text-tertiary)]">
						{m['screenshot.destination.search_empty']()}
					</p>
				{/if}
				{#if searchResults.length > 0}
					<div class="mt-2 max-h-44 overflow-y-auto rounded-md border border-[var(--app-border)]">
						{#each searchResults as issue (issue.id)}
							<button
								type="button"
								onclick={() => (targetIssue = issue)}
								class="flex w-full items-center gap-2 px-3 py-1.5 text-left text-sm hover:bg-[var(--color-bg-hover)] {targetIssue?.id ===
								issue.id
									? 'bg-[var(--color-bg-hover)]'
									: ''}"
							>
								<span class="shrink-0 text-xs text-[var(--color-text-tertiary)]">{issue.identifier}</span>
								<span class="truncate text-[var(--color-text-primary)]">{issue.title}</span>
							</button>
						{/each}
					</div>
				{/if}
				{#if targetIssue}
					<p class="mt-2 text-xs text-[var(--color-text-tertiary)]">
						{m['screenshot.destination.selected_issue']({ identifier: targetIssue.identifier })}
					</p>
				{/if}
			{/if}

			<div class="mt-4 flex items-center justify-end gap-2">
				<Button
					variant="ghost"
					size="sm"
					onclick={() => screenshotCapture.backToPreview()}
					disabled={screenshotCapture.submitting}
				>
					{m['screenshot.destination.back']()}
				</Button>
				<Button size="sm" onclick={() => void confirm()} disabled={confirmDisabled}>
					{#if screenshotCapture.submitting}
						<div class="h-3.5 w-3.5 animate-spin rounded-full border-2 border-current/30 border-t-current" aria-hidden="true"></div>
					{/if}
					{m['screenshot.destination.confirm']()}
				</Button>
			</div>
		</Dialog.Content>
	</Dialog.Root>
{/if}
