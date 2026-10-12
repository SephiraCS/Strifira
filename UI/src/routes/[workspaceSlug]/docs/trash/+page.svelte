<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { Button } from '$lib/components/ui/button';
	import EmptyState from '$lib/components/shared/EmptyState.svelte';
	import { listTrash, restoreDocument, purgeDocument } from '$lib/api/docs';
	import type { DocTreeNode } from '$lib/types/docs';
	import { docsState } from '$lib/features/docs/docs.state.svelte';
	import * as AlertDialog from '$lib/components/ui/alert-dialog';
	import { appToast } from '$lib/features/toast/toast';
	import { m } from '$lib/paraglide/messages.js';
	import ArrowLeft from '@lucide/svelte/icons/arrow-left';
	import RotateCcw from '@lucide/svelte/icons/rotate-ccw';
	import Trash2 from '@lucide/svelte/icons/trash-2';

	const slug = $derived(page.params.workspaceSlug ?? '');

	let items = $state<DocTreeNode[]>([]);
	let loading = $state(true);
	let purgeTarget = $state<DocTreeNode | null>(null);
	let purging = $state(false);

	onMount(async () => {
		try {
			items = await listTrash(slug);
		} catch {
			items = [];
		} finally {
			loading = false;
		}
	});

	async function restore(id: string) {
		try {
			await restoreDocument(slug, id);
			await Promise.all([docsState.load(slug, true), (async () => (items = await listTrash(slug)))()]);
			appToast.success(m['docs.trash.restored']());
		} catch (err) {
			appToast.apiError(err, m['docs.trash.restore_failed']());
		}
	}

	async function purge() {
		if (!purgeTarget || purging) return;
		purging = true;
		try {
			await purgeDocument(slug, purgeTarget.id);
			items = items.filter((i) => i.id !== purgeTarget?.id);
			appToast.success(m['docs.trash.purged']());
		} catch (err) {
			appToast.apiError(err, m['docs.trash.purge_failed']());
		} finally {
			purging = false;
			purgeTarget = null;
		}
	}
</script>

<div class="mx-auto w-full max-w-4xl px-6 py-8">
	<div class="mb-6 flex items-center gap-2">
		<Button variant="ghost" size="icon" onclick={() => goto(`/${slug}/docs`)} aria-label={m['docs.back_to_docs']()}>
			<ArrowLeft size={16} />
		</Button>
		<h1 class="text-xl font-semibold text-[var(--color-text-primary)]">{m['docs.trash.title']()}</h1>
	</div>

	{#if loading}
		<p class="py-12 text-center text-sm text-[var(--color-text-tertiary)]">{m['docs.loading']()}</p>
	{:else if items.length === 0}
		<EmptyState title={m['docs.trash.empty']()} />
	{:else}
		<ul class="space-y-1">
			{#each items as item (item.id)}
				<li class="flex items-center gap-2 rounded-md border border-[var(--app-border)] bg-[var(--color-bg-secondary)] px-3 py-2">
					<span>{item.icon || '📄'}</span>
					<span class="min-w-0 flex-1 truncate text-sm text-[var(--color-text-primary)]">
						{item.title || m['docs.untitled']()}
					</span>
					<Button variant="ghost" size="sm" onclick={() => void restore(item.id)}>
						<RotateCcw size={14} />
						{m['docs.trash.restore']()}
					</Button>
					<Button variant="ghost" size="sm" class="text-[var(--color-error)]" onclick={() => (purgeTarget = item)}>
						<Trash2 size={14} />
						{m['docs.trash.purge']()}
					</Button>
				</li>
			{/each}
		</ul>
	{/if}
</div>

<AlertDialog.Root open={purgeTarget !== null} onOpenChange={(o) => !o && (purgeTarget = null)}>
	<AlertDialog.Content>
		<AlertDialog.Header>
			<AlertDialog.Title>{m['docs.trash.purge_confirm_title']()}</AlertDialog.Title>
			<AlertDialog.Description>
				{m['docs.trash.purge_confirm']({ title: purgeTarget?.title || m['docs.untitled']() })}
			</AlertDialog.Description>
		</AlertDialog.Header>
		<AlertDialog.Footer>
			<AlertDialog.Cancel variant="outline" disabled={purging}>{m['docs.cancel']()}</AlertDialog.Cancel>
			<AlertDialog.Action variant="destructive" onclick={purge} disabled={purging}>
				{purging ? m['docs.working']() : m['docs.trash.purge']()}
			</AlertDialog.Action>
		</AlertDialog.Footer>
	</AlertDialog.Content>
</AlertDialog.Root>
