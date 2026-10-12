<script lang="ts">
	import * as Dialog from '$lib/components/ui/dialog';
	import { Button } from '$lib/components/ui/button';
	import { listRevisions, restoreRevision } from '$lib/api/docs';
	import type { DocRevision } from '$lib/types/docs';
	import { appToast } from '$lib/features/toast/toast';
	import { m } from '$lib/paraglide/messages.js';
	import History from '@lucide/svelte/icons/history';
	import { onMount } from 'svelte';

	let {
		open = $bindable(false),
		slug,
		docId,
		onrestored
	}: { open?: boolean; slug: string; docId: string; onrestored?: () => void } = $props();

	let revisions = $state<DocRevision[]>([]);
	let selected = $state<DocRevision | null>(null);
	let restoring = $state(false);

	onMount(() => {
		$effect(() => {
			if (open) void load();
		});
	});

	async function load() {
		try {
			revisions = await listRevisions(slug, docId);
			selected = revisions[0] ?? null;
		} catch {
			revisions = [];
			selected = null;
		}
	}

	async function restore() {
		if (!selected || restoring) return;
		restoring = true;
		try {
			await restoreRevision(slug, docId, selected.id);
			appToast.success(m['docs.history.restored']());
			open = false;
			onrestored?.();
		} catch (err) {
			appToast.apiError(err, m['docs.history.failed']());
		} finally {
			restoring = false;
		}
	}
</script>

<Dialog.Root bind:open>
	<Dialog.Content class="flex max-h-[85vh] flex-col sm:max-w-[760px] border-[var(--app-border)] bg-[var(--color-bg-secondary)]">
		<Dialog.Header>
			<Dialog.Title class="flex items-center gap-2">
				<History size={16} />
				{m['docs.history.title']()}
			</Dialog.Title>
			<Dialog.Description>{m['docs.history.description']()}</Dialog.Description>
		</Dialog.Header>

		<div class="flex min-h-0 flex-1 gap-3">
			<div class="w-52 shrink-0 space-y-1 overflow-y-auto rounded-md border border-[var(--app-border)] p-1">
				{#each revisions as revision (revision.id)}
					<button
						type="button"
						class="w-full rounded px-2 py-1.5 text-left text-xs {selected?.id === revision.id
							? 'bg-[var(--color-bg-hover)] text-[var(--color-text-primary)]'
							: 'text-[var(--color-text-secondary)] hover:bg-[var(--color-bg-hover)]'}"
						onclick={() => (selected = revision)}
					>
						<span class="block font-medium">{new Date(revision.created_at).toLocaleString()}</span>
						<span class="block truncate text-[var(--color-text-tertiary)]">
							{revision.title || m['docs.untitled']()}
						</span>
					</button>
				{:else}
					<p class="px-2 py-3 text-xs text-[var(--color-text-tertiary)]">{m['docs.history.empty']()}</p>
				{/each}
			</div>
			<div class="min-w-0 flex-1 overflow-y-auto rounded-md border border-[var(--app-border)] p-3">
				{#if selected}
					<p class="mb-2 text-sm font-medium text-[var(--color-text-primary)]">
						{selected.title || m['docs.untitled']()}
					</p>
					<!-- eslint-disable-next-line svelte/no-at-html-tags -- revision HTML is server-sanitized -->
					<div class="prose prose-invert prose-sm max-w-none text-[var(--color-text-secondary)]">
						{@html selected.content_html}
					</div>
				{:else}
					<p class="text-sm text-[var(--color-text-tertiary)]">{m['docs.history.empty']()}</p>
				{/if}
			</div>
		</div>

		<Dialog.Footer>
			<Button variant="outline" onclick={() => (open = false)}>{m['docs.cancel']()}</Button>
			<Button onclick={restore} disabled={!selected || restoring}>
				{restoring ? m['docs.working']() : m['docs.history.restore']()}
			</Button>
		</Dialog.Footer>
	</Dialog.Content>
</Dialog.Root>
