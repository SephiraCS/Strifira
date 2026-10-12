<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { flip } from 'svelte/animate';
	import { dndzone } from 'svelte-dnd-action';
	import * as ContextMenu from '$lib/components/ui/context-menu';
	import * as DropdownMenu from '$lib/components/ui/dropdown-menu';
	import { moveDocument } from '$lib/api/docs';
	import { docsState, type DocTreeEntry } from './docs.state.svelte';
	import { positionForIndex } from './position';
	import { appToast } from '$lib/features/toast/toast';
	import { m } from '$lib/paraglide/messages.js';
	import ChevronDown from '@lucide/svelte/icons/chevron-down';
	import Ellipsis from '@lucide/svelte/icons/ellipsis';
	import FilePlus from '@lucide/svelte/icons/file-plus';
	import Copy from '@lucide/svelte/icons/copy';
	import FolderInput from '@lucide/svelte/icons/folder-input';
	import Trash2 from '@lucide/svelte/icons/trash-2';
	import Self from './DocsTree.svelte';

	let {
		slug,
		entries,
		depth = 0,
		manage = false,
		onnewchild,
		onduplicate,
		onmove,
		ondelete
	}: {
		slug: string;
		entries: DocTreeEntry[];
		depth?: number;
		manage?: boolean;
		onnewchild?: (parent: DocTreeEntry) => void;
		onduplicate?: (entry: DocTreeEntry) => void;
		onmove?: (entry: DocTreeEntry) => void;
		ondelete?: (entry: DocTreeEntry) => void;
	} = $props();

	const collapsed = $state<Set<string>>(loadCollapsed());

	function loadCollapsed(): Set<string> {
		try {
			const raw = localStorage.getItem('ku:docs:collapsed');
			const ids = raw ? (JSON.parse(raw) as string[]) : [];
			return new Set(Array.isArray(ids) ? ids : []);
		} catch {
			return new Set();
		}
	}

	function persistCollapsed() {
		try {
			localStorage.setItem('ku:docs:collapsed', JSON.stringify([...collapsed]));
		} catch {
			// Best-effort.
		}
	}

	function toggle(id: string) {
		if (collapsed.has(id)) collapsed.delete(id);
		else collapsed.add(id);
		persistCollapsed();
	}

	const currentDocId = $derived(page.params.docId ?? '');

	function isActive(id: string): boolean {
		return currentDocId === id;
	}

	type DndEvent = CustomEvent<{ items: DocTreeEntry[] }>;
	const dragOptions = { flipDurationMs: 120, morphDisabled: true, dropTargetStyle: { outline: 'none' } };

	/** Persist a same-parent reorder as fractional positions. */
	function handleFinalize(event: DndEvent) {
		const items = event.detail.items;
		if (items.every((item, i) => entries[i]?.id === item.id)) return;
		const reordered = items.find((item, i) => entries[i]?.id !== item.id);
		if (!reordered) return;
		const index = items.findIndex((item) => item.id === reordered.id);
		void moveDocument(
			slug,
			reordered.id,
			reordered.parent_id ?? null,
			positionForIndex(
				items.map((item) => item.position),
				index
			)
		)
			.then(() => docsState.load(slug, true))
			.catch((err) => appToast.apiError(err, m['docs.move.failed']()));
	}

	const hasChildren = $derived.by(() => {
		const set = new Set<string>();
		for (const item of entries) {
			if (docsState.docs.some((d) => d.parent_id === item.id)) set.add(item.id);
		}
		return set;
	});
</script>

{#snippet row(entry: DocTreeEntry)}
	<ContextMenu.Root>
		<ContextMenu.Trigger class="block">
			<!-- svelte-ignore a11y_no_static_element_interactions -->
			<div class="group/doc flex items-center rounded-md hover:bg-[var(--color-bg-hover)]">
				<a
					href="/{slug}/docs/{entry.id}"
					class="flex min-w-0 flex-1 items-center gap-1 px-1 py-1 text-sm {isActive(entry.id)
						? 'bg-[var(--color-bg-hover)]/50 font-medium text-[var(--color-text-primary)]'
						: 'text-[var(--color-text-secondary)] hover:text-[var(--color-text-primary)]'}"
				>
					{#if hasChildren.has(entry.id)}
						<button
							type="button"
							class="shrink-0 rounded p-0.5 text-[var(--color-text-tertiary)] hover:bg-[var(--color-bg-tertiary)]"
							onclick={(e) => {
								e.preventDefault();
								e.stopPropagation();
								toggle(entry.id);
							}}
							aria-label={collapsed.has(entry.id)
								? m['docs.tree.expand']({ name: entry.title || m['docs.untitled']() })
								: m['docs.tree.collapse']({ name: entry.title || m['docs.untitled']() })}
						>
							<ChevronDown size={13} class="transition-transform {collapsed.has(entry.id) ? '-rotate-90' : ''}" />
						</button>
					{:else}
						<span class="w-[21px] shrink-0"></span>
					{/if}
					<span class="shrink-0">{entry.icon || '📄'}</span>
					<span class="truncate {entry.title ? '' : 'italic text-[var(--color-text-tertiary)]'}">
						{entry.title || m['docs.untitled']()}
					</span>
				</a>
				{#if manage}
					<button
						type="button"
						class="mr-0.5 shrink-0 rounded p-0.5 text-[var(--color-text-tertiary)] opacity-0 transition-opacity group-hover/doc:opacity-100 hover:bg-[var(--color-bg-tertiary)] hover:text-[var(--color-text-secondary)]"
						onclick={() => onnewchild?.(entry)}
						title={m['docs.tree.new_subpage']()}
					>
						<FilePlus size={13} />
					</button>
					<DropdownMenu.Root>
						<DropdownMenu.Trigger
							class="mr-0.5 shrink-0 rounded p-0.5 text-[var(--color-text-tertiary)] opacity-0 transition-opacity group-hover/doc:opacity-100 hover:bg-[var(--color-bg-tertiary)] hover:text-[var(--color-text-secondary)]"
						>
							<Ellipsis size={13} />
						</DropdownMenu.Trigger>
						<DropdownMenu.Content side="right" align="start" class="w-44">
							<DropdownMenu.Item onclick={() => onnewchild?.(entry)}>
								<FilePlus size={14} class="mr-2" />
								{m['docs.tree.new_subpage']()}
							</DropdownMenu.Item>
							<DropdownMenu.Item onclick={() => onduplicate?.(entry)}>
								<Copy size={14} class="mr-2" />
								{m['docs.duplicate']()}
							</DropdownMenu.Item>
							<DropdownMenu.Item onclick={() => onmove?.(entry)}>
								<FolderInput size={14} class="mr-2" />
								{m['docs.move.title']()}
							</DropdownMenu.Item>
							<DropdownMenu.Separator />
							<DropdownMenu.Item onclick={() => ondelete?.(entry)} class="text-[var(--color-error)]">
								<Trash2 size={14} class="mr-2" />
								{m['docs.delete']()}
							</DropdownMenu.Item>
						</DropdownMenu.Content>
					</DropdownMenu.Root>
				{/if}
			</div>
		</ContextMenu.Trigger>
		<ContextMenu.Content class="w-44">
			<ContextMenu.Item onclick={() => goto(`/${slug}/docs/${entry.id}`)}>
				📄 {m['docs.tree.open']()}
			</ContextMenu.Item>
			{#if manage}
				<ContextMenu.Item onclick={() => onnewchild?.(entry)}>
					<FilePlus size={13} class="mr-2" />
					{m['docs.tree.new_subpage']()}
				</ContextMenu.Item>
				<ContextMenu.Item onclick={() => onduplicate?.(entry)}>
					<Copy size={13} class="mr-2" />
					{m['docs.duplicate']()}
				</ContextMenu.Item>
				<ContextMenu.Item onclick={() => onmove?.(entry)}>
					<FolderInput size={13} class="mr-2" />
					{m['docs.move.title']()}
				</ContextMenu.Item>
				<ContextMenu.Separator />
				<ContextMenu.Item onclick={() => ondelete?.(entry)} class="text-[var(--color-error)] focus:text-[var(--color-error)]">
					<Trash2 size={13} class="mr-2" />
					{m['docs.delete']()}
				</ContextMenu.Item>
			{/if}
		</ContextMenu.Content>
	</ContextMenu.Root>
{/snippet}

<div
	class="space-y-px {depth > 0 ? 'ml-[21px] border-l border-[var(--app-border)] pl-1' : ''}"
	use:dndzone={{ items: entries, type: `doc-children-${entries[0]?.parent_id ?? 'root'}`, ...dragOptions }}
	onconsider={() => {}}
	onfinalize={handleFinalize}
>
	{#each entries as entry (entry.id)}
		<div animate:flip={{ duration: 120 }}>
			{@render row(entry)}
			{#if !collapsed.has(entry.id)}
				{#each [entry.children] as children (entry.id)}
					{#if children.length > 0}
						<Self {slug} entries={children} depth={depth + 1} {manage} {onnewchild} {onduplicate} {onmove} {ondelete} />
					{/if}
				{/each}
			{/if}
		</div>
	{/each}
</div>
