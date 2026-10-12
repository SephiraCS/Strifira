<script lang="ts">
	import * as Dialog from '$lib/components/ui/dialog';
	import { Button } from '$lib/components/ui/button';
	import { moveDocument } from '$lib/api/docs';
	import { docsState, type DocTreeEntry } from '$lib/features/docs/docs.state.svelte';
	import { appToast } from '$lib/features/toast/toast';
	import { m } from '$lib/paraglide/messages.js';
	import FileText from '@lucide/svelte/icons/file-text';
	import ChevronDown from '@lucide/svelte/icons/chevron-down';

	let {
		open = $bindable(false),
		slug,
		docId,
		onmoved
	}: { open?: boolean; slug: string; docId: string; onmoved?: () => void } = $props();

	let submitting = $state(false);
	let selected = $state<string | null>(null);
	let expanded = $state<Set<string>>(new Set());

	const currentParent = $derived(docsState.byId(docId)?.parent_id ?? null);

	function toggleExpand(id: string) {
		const next = new Set(expanded);
		if (next.has(id)) next.delete(id);
		else next.add(id);
		expanded = next;
	}

	async function move() {
		if (submitting) return;
		submitting = true;
		try {
			await moveDocument(slug, docId, selected);
			appToast.success(m['docs.move.moved']());
			open = false;
			selected = null;
			onmoved?.();
		} catch (err) {
			appToast.apiError(err, m['docs.move.failed']());
		} finally {
			submitting = false;
		}
	}
</script>

<Dialog.Root bind:open>
	<Dialog.Content class="sm:max-w-[440px] border-[var(--app-border)] bg-[var(--color-bg-secondary)]">
		<Dialog.Header>
			<Dialog.Title>{m['docs.move.title']()}</Dialog.Title>
			<Dialog.Description>{m['docs.move.description']()}</Dialog.Description>
		</Dialog.Header>
		<div class="max-h-72 overflow-y-auto rounded-md border border-[var(--app-border)] p-1">
			<button
				type="button"
				class="flex w-full items-center gap-2 rounded px-2 py-1.5 text-sm {selected === null
					? 'bg-[var(--color-bg-hover)] text-[var(--color-text-primary)]'
					: 'text-[var(--color-text-secondary)] hover:bg-[var(--color-bg-hover)]'}"
				onclick={() => (selected = null)}
			>
				<FileText size={14} />
				{m['docs.move.root']()}
			</button>
			{#snippet treeNode(node: DocTreeEntry, depth: number)}
				{#if node.id !== docId}
					<div class="flex items-center">
						<button
							type="button"
							class="flex min-w-0 flex-1 items-center gap-1.5 rounded px-2 py-1.5 text-sm {selected === node.id
								? 'bg-[var(--color-bg-hover)] text-[var(--color-text-primary)]'
								: 'text-[var(--color-text-secondary)] hover:bg-[var(--color-bg-hover)]'}"
							style="padding-left: {depth * 14 + 8}px"
							onclick={() => {
								selected = node.id;
								toggleExpand(node.id);
							}}
						>
							{#if node.children.length > 0}
								<ChevronDown
									size={12}
									class="shrink-0 text-[var(--color-text-tertiary)] transition-transform {expanded.has(node.id) ? '' : '-rotate-90'}"
								/>
							{:else}
								<span class="w-3"></span>
							{/if}
							{#if node.icon}<span>{node.icon}</span>{/if}
							<span class="truncate">{node.title || m['docs.untitled']()}</span>
						</button>
					</div>
					{#if expanded.has(node.id)}
						{#each node.children as child (child.id)}
							{@render treeNode(child, depth + 1)}
						{/each}
					{/if}
				{/if}
			{/snippet}
			{#each docsState.tree as node (node.id)}
				{@render treeNode(node, 0)}
			{/each}
		</div>
		<Dialog.Footer>
			<Button variant="outline" onclick={() => (open = false)} disabled={submitting}
				>{m['docs.cancel']()}</Button
			>
			<Button onclick={move} disabled={submitting}>{m['docs.move.submit']()}</Button>
		</Dialog.Footer>
	</Dialog.Content>
</Dialog.Root>
