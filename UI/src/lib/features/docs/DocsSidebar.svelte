<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { Button } from '$lib/components/ui/button';
	import * as Dialog from '$lib/components/ui/dialog';
	import * as AlertDialog from '$lib/components/ui/alert-dialog';
	import { createDocument, deleteDocument, duplicateDocument, updateDocument } from '$lib/api/docs';
	import { listFavorites, deleteFavorite } from '$lib/api/favorites';
	import { docsState, type DocTreeEntry } from './docs.state.svelte';
	import DocsTree from './DocsTree.svelte';
	import MoveDocDialog from './MoveDocDialog.svelte';
	import { appToast } from '$lib/features/toast/toast';
	import { m } from '$lib/paraglide/messages.js';
	import FilePlus from '@lucide/svelte/icons/file-plus';
	import Star from '@lucide/svelte/icons/star';
	import Clock from '@lucide/svelte/icons/clock';
	import Trash2 from '@lucide/svelte/icons/trash-2';

	let { slug }: { slug: string } = $props();

	let favorites = $state<{ id: string; title: string; icon?: string; favId: string }[]>([]);
	let favoritesOpen = $state(true);
	let recentsOpen = $state(true);

	// Tree management state (same flows as the docs list page).
	let moveTarget = $state<string | null>(null);
	let renameOpen = $state(false);
	let renameTarget = $state<DocTreeEntry | null>(null);
	let renameValue = $state('');
	let renaming = $state(false);
	let deleteTarget = $state<DocTreeEntry | null>(null);
	let deleting = $state(false);

	const currentDocId = $derived(page.params.docId ?? '');
	const recentDocs = $derived.by(() => {
		return docsState.recents
			.map((id) => docsState.byId(id))
			.filter((d): d is NonNullable<typeof d> => Boolean(d))
			.slice(0, 5);
	});

	onMount(() => {
		void docsState.load(slug);
		void loadFavorites();
		const onDocEvent = () => void loadFavorites();
		window.addEventListener('ws:doc-event', onDocEvent);
		return () => window.removeEventListener('ws:doc-event', onDocEvent);
	});

	async function loadFavorites() {
		try {
			const list = await listFavorites(slug);
			favorites = list
				.filter((f) => f.entity_type === 'document')
				.flatMap((f) => {
					const doc = docsState.byId(f.entity_id);
					return doc ? [{ id: doc.id, title: doc.title, icon: doc.icon as string | undefined, favId: f.id }] : [];
				});
		} catch {
			favorites = [];
		}
	}

	async function newPage() {
		try {
			const created = await createDocument(slug, { title: '' });
			await docsState.load(slug, true);
			goto(`/${slug}/docs/${created.id}`);
		} catch (err) {
			appToast.apiError(err, m['docs.create_failed']());
		}
	}

	async function newChild(parent: DocTreeEntry) {
		try {
			const created = await createDocument(slug, { title: '', parent_id: parent.id });
			await docsState.load(slug, true);
			goto(`/${slug}/docs/${created.id}`);
		} catch (err) {
			appToast.apiError(err, m['docs.create_failed']());
		}
	}

	async function newSubfolder(parent: DocTreeEntry) {
		try {
			const created = await createDocument(slug, { title: '', is_folder: true, parent_id: parent.id });
			await docsState.load(slug, true);
			const entry = docsState.byId(created.id);
			if (entry) {
				renameTarget = { ...entry, children: [] };
				renameValue = '';
				renameOpen = true;
			}
		} catch (err) {
			appToast.apiError(err, m['docs.folder.create_failed']());
		}
	}

	function startRename(entry: DocTreeEntry) {
		renameTarget = entry;
		renameValue = entry.title;
		renameOpen = true;
	}

	async function applyRename() {
		if (!renameTarget || renaming) return;
		const name = renameValue.trim();
		if (!name) {
			renameOpen = false;
			renameTarget = null;
			return;
		}
		renaming = true;
		try {
			await updateDocument(slug, renameTarget.id, { title: name });
			await docsState.load(slug, true);
			appToast.success(m['docs.folder.renamed']());
			renameOpen = false;
			renameTarget = null;
		} catch (err) {
			appToast.apiError(err, m['docs.folder.rename_failed']());
		} finally {
			renaming = false;
		}
	}

	async function duplicate(entry: DocTreeEntry) {
		try {
			const copy = await duplicateDocument(slug, entry.id);
			await docsState.load(slug, true);
			goto(`/${slug}/docs/${copy.id}`);
		} catch (err) {
			appToast.apiError(err, m['docs.duplicate_failed']());
		}
	}

	async function removeDoc() {
		if (!deleteTarget || deleting) return;
		deleting = true;
		try {
			await deleteDocument(slug, deleteTarget.id);
			await docsState.load(slug, true);
			appToast.success(m['docs.deleted']());
			if (currentDocId === deleteTarget.id) goto(`/${slug}/docs`);
		} catch (err) {
			appToast.apiError(err, m['docs.delete_failed']());
		} finally {
			deleting = false;
			deleteTarget = null;
		}
	}

	async function unfavorite(favId: string) {
		try {
			await deleteFavorite(slug, favId);
			favorites = favorites.filter((f) => f.favId !== favId);
		} catch {
			// Best-effort.
		}
	}
</script>

<aside
	class="flex h-full w-60 shrink-0 flex-col border-r border-[var(--app-border)] bg-[var(--color-bg-secondary)]"
	aria-label={m['docs.title']()}
>
	<div class="flex items-center gap-1 px-3 pb-1 pt-3">
		<a
			href="/{slug}/docs"
			class="min-w-0 flex-1 truncate text-sm font-semibold text-[var(--color-text-primary)] hover:text-[var(--color-text-secondary)]"
		>
			{m['docs.title']()}
		</a>
		<button
			type="button"
			class="shrink-0 rounded p-1 text-[var(--color-text-tertiary)] hover:bg-[var(--color-bg-hover)] hover:text-[var(--color-text-secondary)]"
			onclick={() => void newPage()}
			title={m['docs.new_blank']()}
			aria-label={m['docs.new_blank']()}
		>
			<FilePlus size={15} />
		</button>
	</div>

	<div class="min-h-0 flex-1 overflow-y-auto px-2 pb-2">
		{#if docsState.loading && docsState.docs.length === 0}
			<p class="px-2 py-6 text-center text-xs text-[var(--color-text-tertiary)]">{m['docs.loading']()}</p>
		{:else if docsState.error}
			<p class="px-2 py-6 text-center text-xs text-[var(--color-error)]">{m['docs.load_failed']()}</p>
		{:else}
			{#if favorites.length > 0}
				<section class="mb-1">
					<button
						type="button"
						class="flex w-full items-center gap-1.5 rounded px-2 py-1 text-[10px] font-semibold uppercase tracking-wide text-[var(--color-text-tertiary)] hover:bg-[var(--color-bg-hover)]"
						onclick={() => (favoritesOpen = !favoritesOpen)}
						aria-expanded={favoritesOpen}
					>
						<Star size={11} />
						<span class="min-w-0 flex-1 text-left">{m['docs.favorites']()}</span>
					</button>
					{#if favoritesOpen}
						<div class="space-y-px">
							{#each favorites as fav (fav.favId)}
								<div class="group/fav flex items-center rounded-md hover:bg-[var(--color-bg-hover)]">
									<a
										href="/{slug}/docs/{fav.id}"
										class="flex min-w-0 flex-1 items-center gap-1 px-2 py-1 text-sm {currentDocId === fav.id
											? 'font-medium text-[var(--color-text-primary)]'
											: 'text-[var(--color-text-secondary)] hover:text-[var(--color-text-primary)]'}"
									>
										<span class="shrink-0">{fav.icon || '📄'}</span>
										<span class="truncate">{fav.title || m['docs.untitled']()}</span>
									</a>
									<button
										type="button"
										class="mr-1 shrink-0 rounded p-0.5 text-[var(--color-text-tertiary)] opacity-0 transition-opacity group-hover/fav:opacity-100 hover:text-[var(--color-error)]"
										onclick={() => void unfavorite(fav.favId)}
										aria-label={m['docs.favorite.remove']()}
										title={m['docs.favorite.remove']()}
									>
										✕
									</button>
								</div>
							{/each}
						</div>
					{/if}
				</section>
			{/if}

			{#if recentDocs.length > 0}
				<section class="mb-1">
					<button
						type="button"
						class="flex w-full items-center gap-1.5 rounded px-2 py-1 text-[10px] font-semibold uppercase tracking-wide text-[var(--color-text-tertiary)] hover:bg-[var(--color-bg-hover)]"
						onclick={() => (recentsOpen = !recentsOpen)}
						aria-expanded={recentsOpen}
					>
						<Clock size={11} />
						<span class="min-w-0 flex-1 text-left">{m['docs.recents']()}</span>
					</button>
					{#if recentsOpen}
						<div class="space-y-px">
							{#each recentDocs as doc (doc.id)}
								<a
									href="/{slug}/docs/{doc.id}"
									class="flex min-w-0 items-center gap-1 rounded-md px-2 py-1 text-sm {currentDocId === doc.id
										? 'bg-[var(--color-bg-hover)]/50 font-medium text-[var(--color-text-primary)]'
										: 'text-[var(--color-text-secondary)] hover:bg-[var(--color-bg-hover)] hover:text-[var(--color-text-primary)]'}"
								>
									<span class="shrink-0">{doc.icon || '📄'}</span>
									<span class="truncate">{doc.title || m['docs.untitled']()}</span>
								</a>
							{/each}
						</div>
					{/if}
				</section>
			{/if}

			<section class="mt-1" aria-label={m['docs.all_pages']()}>
				<div class="px-2 py-1 text-[10px] font-semibold uppercase tracking-wide text-[var(--color-text-tertiary)]">
					{m['docs.all_pages']()}
				</div>
				<DocsTree
					{slug}
					entries={docsState.tree}
					manage
					onnewchild={(parent) => void newChild(parent)}
					onnewfolder={(parent) => void newSubfolder(parent)}
					onrename={startRename}
					onduplicate={(entry) => void duplicate(entry)}
					onmove={(entry) => (moveTarget = entry.id)}
					ondelete={(entry) => (deleteTarget = entry)}
				/>
			</section>
		{/if}
	</div>

	<div class="border-t border-[var(--app-border)] px-2 py-1.5">
		<a
			href="/{slug}/docs/trash"
			class="flex items-center gap-2 rounded-md px-2 py-1 text-xs text-[var(--color-text-tertiary)] hover:bg-[var(--color-bg-hover)] hover:text-[var(--color-text-secondary)]"
		>
			<Trash2 size={13} />
			{m['docs.trash.title']()}
		</a>
	</div>
</aside>

<MoveDocDialog
	open={moveTarget !== null}
	{slug}
	docId={moveTarget ?? ''}
	onmoved={() => {
		moveTarget = null;
		docsState.scheduleRefresh();
	}}
/>

<Dialog.Root bind:open={renameOpen}>
	<Dialog.Content class="sm:max-w-[380px] border-[var(--app-border)] bg-[var(--color-bg-secondary)]">
		<Dialog.Header>
			<Dialog.Title>{m['docs.folder.rename_title']()}</Dialog.Title>
		</Dialog.Header>
		<input
			type="text"
			class="w-full rounded-md border border-[var(--app-border)] bg-[var(--color-bg-primary)] px-3 py-2 text-sm text-[var(--color-text-primary)] outline-none placeholder:text-[var(--color-text-tertiary)] focus:border-[var(--app-accent)]"
			placeholder={m['docs.folder.name_placeholder']()}
			bind:value={renameValue}
			onkeydown={(e) => {
				if (e.key === 'Enter') {
					e.preventDefault();
					void applyRename();
				}
			}}
		/>
		<Dialog.Footer>
			<Button variant="outline" onclick={() => (renameOpen = false)} disabled={renaming}
				>{m['docs.cancel']()}</Button
			>
			<Button onclick={() => void applyRename()} disabled={renaming}>{m['docs.done']()}</Button>
		</Dialog.Footer>
	</Dialog.Content>
</Dialog.Root>

<AlertDialog.Root open={deleteTarget !== null} onOpenChange={(o) => !o && (deleteTarget = null)}>
	<AlertDialog.Content>
		<AlertDialog.Header>
			<AlertDialog.Title>{m['docs.delete_confirm_title']()}</AlertDialog.Title>
			<AlertDialog.Description>
				{m['docs.delete_confirm']({ title: deleteTarget?.title || m['docs.untitled']() })}
			</AlertDialog.Description>
		</AlertDialog.Header>
		<AlertDialog.Footer>
			<AlertDialog.Cancel variant="outline" disabled={deleting}>{m['docs.cancel']()}</AlertDialog.Cancel>
			<AlertDialog.Action variant="destructive" onclick={removeDoc} disabled={deleting}>
				{deleting ? m['docs.working']() : m['docs.delete']()}
			</AlertDialog.Action>
		</AlertDialog.Footer>
	</AlertDialog.Content>
</AlertDialog.Root>
