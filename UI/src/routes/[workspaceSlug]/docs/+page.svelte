<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { Button } from '$lib/components/ui/button';
	import * as DropdownMenu from '$lib/components/ui/dropdown-menu';
	import * as Dialog from '$lib/components/ui/dialog';
	import EmptyState from '$lib/components/shared/EmptyState.svelte';
	import { createDocument, updateDocument } from '$lib/api/docs';
	import { listFavorites, deleteFavorite } from '$lib/api/favorites';
	import { docsState, type DocTreeEntry } from '$lib/features/docs/docs.state.svelte';
	import { markdownTitle } from '$lib/features/docs/markdown';
	import { wikiTitlesFromHtml } from '$lib/features/docs/wiki-link.extension';
	import DocsTree from '$lib/features/docs/DocsTree.svelte';
	import DocGraphView from '$lib/features/docs/DocGraphView.svelte';
	import MoveDocDialog from '$lib/features/docs/MoveDocDialog.svelte';
	import * as AlertDialog from '$lib/components/ui/alert-dialog';
	import { appToast } from '$lib/features/toast/toast';
	import { m } from '$lib/paraglide/messages.js';
	import FilePlus from '@lucide/svelte/icons/file-plus';
	import FileStack from '@lucide/svelte/icons/file-stack';
	import FolderPlus from '@lucide/svelte/icons/folder-plus';
	import Upload from '@lucide/svelte/icons/upload';
	import CalendarDays from '@lucide/svelte/icons/calendar-days';
	import Network from '@lucide/svelte/icons/network';
	import Trash2 from '@lucide/svelte/icons/trash-2';
	import Search from '@lucide/svelte/icons/search';
	import Star from '@lucide/svelte/icons/star';
	import Clock from '@lucide/svelte/icons/clock';

	const slug = $derived(page.params.workspaceSlug ?? '');

	let search = $state('');
	let showGraph = $state(false);
	let moveTarget = $state<string | null>(null);
	let deleteTarget = $state<DocTreeEntry | null>(null);
	let deleting = $state(false);
	let importInput: HTMLInputElement | undefined = $state();
	let favoriteDocs = $state<{ id: string; title: string; icon?: string; favId: string }[]>([]);

	// Folder view: /docs?folder=<id> lists the folder's contents and scopes
	// search to its subtree. Absent (or dangling) ids fall back to the root.
	const folderId = $derived(page.url.searchParams.get('folder') ?? '');
	const folderDoc = $derived(folderId ? docsState.byId(folderId) : undefined);
	const inFolder = $derived(Boolean(folderId && folderDoc?.is_folder));

	let renameOpen = $state(false);
	let renameTarget = $state<DocTreeEntry | null>(null);
	let renameValue = $state('');
	let renaming = $state(false);

	/** The open folder's children as tree entries, or the whole tree at the root. */
	const scopedTree = $derived.by(() => {
		const tree = docsState.tree;
		if (!inFolder) return tree;
		const find = (entries: DocTreeEntry[]): DocTreeEntry | null => {
			for (const entry of entries) {
				if (entry.id === folderId) return entry;
				const inChild = find(entry.children);
				if (inChild) return inChild;
			}
			return null;
		};
		const folder = find(tree);
		return folder ? folder.children : [];
	});

	const filteredTree = $derived.by(() => {
		const q = search.trim().toLowerCase();
		if (!q) return scopedTree;
		// Search keeps ancestors of matches so the tree stays navigable;
		// inside a folder the candidates are exactly its subtree.
		const matches = (entry: DocTreeEntry): DocTreeEntry | null => {
			const self = entry.title.toLowerCase().includes(q) || entry.tags.some((t) => t.toLowerCase().includes(q));
			const children = entry.children.map(matches).filter((c): c is DocTreeEntry => c !== null);
			if (self || children.length > 0) return { ...entry, children };
			return null;
		};
		return scopedTree.map(matches).filter((e): e is DocTreeEntry => e !== null);
	});

	const recentDocs = $derived.by(() => {
		return docsState.recents
			.map((id) => docsState.byId(id))
			.filter((d): d is NonNullable<typeof d> => Boolean(d))
			.slice(0, 6);
	});

	const templates = $derived(docsState.docs.filter((d) => d.is_template));
	const folderBreadcrumb = $derived(inFolder ? docsState.breadcrumb(folderId) : []);

	onMount(() => {
		void docsState.load(slug);
		void loadFavorites();
		const onDocEvent = () => void loadFavorites();
		window.addEventListener('ws:doc-event', onDocEvent);
		return () => window.removeEventListener('ws:doc-event', onDocEvent);
	});

	async function loadFavorites() {
		try {
			const favorites = await listFavorites(slug);
			favoriteDocs = favorites
				.filter((f) => f.entity_type === 'document')
				.flatMap((f) => {
					const doc = docsState.byId(f.entity_id);
					return doc ? [{ id: doc.id, title: doc.title, icon: doc.icon as string | undefined, favId: f.id }] : [];
				});
		} catch {
			favoriteDocs = [];
		}
	}

	async function newDoc(parentId?: string, template?: DocTreeEntry) {
		try {
			const created = await createDocument(slug, {
				title: '',
				parent_id: parentId
			});
			if (template) {
				await duplicateInto(template, created.id);
			}
			await docsState.load(slug, true);
			goto(`/${slug}/docs/${created.id}`);
		} catch (err) {
			appToast.apiError(err, m['docs.create_failed']());
		}
	}

	async function newFolder(parentId?: string) {
		try {
			const created = await createDocument(slug, {
				title: '',
				is_folder: true,
				parent_id: parentId
			});
			await docsState.load(slug, true);
			// A folder's name is its whole identity: open rename right away.
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

	/** Copy a template's content into the freshly created doc via its update API. */
	async function duplicateInto(template: DocTreeEntry, targetId: string) {
		try {
			const { getDocument, updateDocument } = await import('$lib/api/docs');
			const source = await getDocument(slug, template.id);
			await updateDocument(slug, targetId, {
				content_html: source.content_html,
				content_text: source.content_text,
				wiki_titles: wikiTitlesFromHtml(source.content_html)
			});
		} catch {
			// Template body copy is best-effort; the page still opened.
		}
	}

	async function newFromTemplate(templateId: string) {
		const template = templates.find((t) => t.id === templateId);
		if (template) await newDoc(inFolder ? folderId : undefined, template as DocTreeEntry);
	}

	async function importMarkdownFiles(files: FileList | null) {
		if (!files || files.length === 0) return;
		let imported = 0;
		for (const file of Array.from(files)) {
			try {
				const text = await file.text();
				const created = await createDocument(slug, {
					title: markdownTitle(file.name, text),
					content_md: text
				});
				imported++;
				docsState.docs = [...docsState.docs, {
					id: created.id,
					title: created.title,
					icon: created.icon,
					position: created.position,
					is_template: false,
					is_folder: false,
					updated_at: created.updated_at,
					tags: created.tags ?? []
				}];
			} catch {
				// Keep importing the rest.
			}
		}
		if (imported > 0) {
			appToast.success(m['docs.import.done']({ count: String(imported) }));
			await docsState.load(slug, true);
		} else {
			appToast.error(m['docs.import.failed']());
		}
	}

	async function createDailyNote() {
		const today = new Date().toLocaleDateString(undefined, { year: 'numeric', month: 'long', day: 'numeric' });
		const existing = docsState.docs.find((d) => d.title === today);
		if (existing) {
			goto(`/${slug}/docs/${existing.id}`);
			return;
		}
		await newDoc();
	}

	async function removeDoc() {
		if (!deleteTarget || deleting) return;
		deleting = true;
		try {
			const { deleteDocument } = await import('$lib/api/docs');
			await deleteDocument(slug, deleteTarget.id);
			await docsState.load(slug, true);
			appToast.success(m['docs.deleted']());
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
			favoriteDocs = favoriteDocs.filter((f) => f.favId !== favId);
		} catch {
			// Best-effort.
		}
	}
</script>

<div class="mx-auto w-full max-w-4xl px-6 py-8">
	<div class="mb-6 flex items-center gap-2">
		<h1 class="text-xl font-semibold text-[var(--color-text-primary)]">
			{inFolder ? folderDoc?.title || m['docs.untitled']() : m['docs.title']()}
		</h1>
		{#if inFolder}
			<span class="rounded-full bg-[var(--color-bg-tertiary)] px-2 py-0.5 text-xs text-[var(--color-text-tertiary)]">📁</span>
		{/if}
		<div class="ml-auto flex items-center gap-1.5">
			<Button variant="ghost" size="sm" onclick={() => (showGraph = true)} title={m['docs.graph.title']()}>
				<Network size={15} />
			</Button>
			<a
				href="/{slug}/docs/trash"
				class="rounded-md p-1.5 text-[var(--color-text-tertiary)] hover:bg-[var(--color-bg-hover)] hover:text-[var(--color-text-secondary)]"
				title={m['docs.trash.title']()}
				aria-label={m['docs.trash.title']()}
			>
				<Trash2 size={15} />
			</a>
			<DropdownMenu.Root>
				<DropdownMenu.Trigger>
					{m['docs.new']()}
				</DropdownMenu.Trigger>
				<DropdownMenu.Content align="end" class="w-52">
					<DropdownMenu.Item onclick={() => void newDoc(inFolder ? folderId : undefined)}>
						<FilePlus size={14} class="mr-2" />
						{m['docs.new_blank']()}
					</DropdownMenu.Item>
					<DropdownMenu.Item onclick={() => void newFolder(inFolder ? folderId : undefined)}>
						<FolderPlus size={14} class="mr-2" />
						{inFolder ? m['docs.folder.new_subfolder']() : m['docs.folder.new']()}
					</DropdownMenu.Item>
					{#if !inFolder}
						<DropdownMenu.Item onclick={() => void createDailyNote()}>
							<CalendarDays size={14} class="mr-2" />
							{m['docs.daily_note']()}
						</DropdownMenu.Item>
						<DropdownMenu.Item onclick={() => importInput?.click()}>
							<Upload size={14} class="mr-2" />
							{m['docs.import.markdown']()}
						</DropdownMenu.Item>
					{/if}
					{#if templates.length > 0 && !inFolder}
						<DropdownMenu.Separator />
						<DropdownMenu.Label class="text-xs text-[var(--color-text-tertiary)]">
							{m['docs.templates.new_from']()}
						</DropdownMenu.Label>
						{#each templates as template (template.id)}
							<DropdownMenu.Item onclick={() => void newFromTemplate(template.id)}>
								<FileStack size={14} class="mr-2" />
								{template.icon || '📄'} {template.title || m['docs.untitled']()}
							</DropdownMenu.Item>
						{/each}
					{/if}
				</DropdownMenu.Content>
			</DropdownMenu.Root>
		</div>
	</div>

	<input
		bind:this={importInput}
		type="file"
		accept=".md,.markdown,text/markdown"
		multiple
		class="hidden"
		onchange={(e) => {
			void importMarkdownFiles(e.currentTarget.files);
			e.currentTarget.value = '';
		}}
	/>

	<!-- Folder breadcrumb -->
	{#if inFolder}
		<nav class="mb-4 flex items-center gap-1 text-xs text-[var(--color-text-tertiary)]" aria-label="Breadcrumb">
			<a href="/{slug}/docs" class="hover:text-[var(--color-text-secondary)]">{m['docs.title']()}</a>
			{#each folderBreadcrumb as crumb (crumb.id)}
				<span>/</span>
				<a
					href="/{slug}/docs?folder={crumb.id}"
					class="truncate {crumb.id === folderId ? 'text-[var(--color-text-secondary)]' : 'hover:text-[var(--color-text-secondary)]'}"
				>
					{crumb.icon ? crumb.icon + ' ' : ''}{crumb.title || m['docs.untitled']()}
				</a>
			{/each}
		</nav>
	{/if}

	<!-- Search (scoped to the folder subtree when inside one) -->
	<div class="relative mb-6">
		<Search size={14} class="absolute left-3 top-1/2 -translate-y-1/2 text-[var(--color-text-tertiary)]" />
		<input
			type="search"
			class="w-full rounded-md border border-[var(--app-border)] bg-[var(--color-bg-secondary)] py-2 pl-9 pr-3 text-sm text-[var(--color-text-primary)] outline-none placeholder:text-[var(--color-text-tertiary)] focus:border-[var(--app-accent)]"
			placeholder={m['docs.search_placeholder']()}
			bind:value={search}
		/>
	</div>

	{#if docsState.loading && docsState.docs.length === 0}
		<p class="py-12 text-center text-sm text-[var(--color-text-tertiary)]">{m['docs.loading']()}</p>
	{:else if !docsState.loading && !inFolder && docsState.docs.length === 0}
		<EmptyState
			title={m['docs.empty_title']()}
			description={m['docs.empty_description']()}
			action={{ label: m['docs.new_blank'](), onclick: () => void newDoc() }}
		/>
	{:else if inFolder && filteredTree.length === 0 && !search}
		<EmptyState
			title={m['docs.folder.empty']()}
			description={m['docs.empty_description']()}
			action={{ label: m['docs.new_blank'](), onclick: () => void newDoc(folderId) }}
		/>
	{:else}
		{#if !inFolder && favoriteDocs.length > 0}
			<section class="mb-6">
				<h2 class="mb-1.5 flex items-center gap-1.5 text-xs font-semibold uppercase tracking-wide text-[var(--color-text-tertiary)]">
					<Star size={12} /> {m['docs.favorites']()}
				</h2>
				<div class="flex flex-wrap gap-1.5">
					{#each favoriteDocs as fav (fav.id)}
						<span class="flex items-center gap-1 rounded-md border border-[var(--app-border)] bg-[var(--color-bg-secondary)] px-2 py-1 text-xs">
							<a href="/{slug}/docs/{fav.id}" class="text-[var(--color-text-secondary)] hover:text-[var(--color-text-primary)]">
								{fav.icon || '📄'} {fav.title || m['docs.untitled']()}
							</a>
							<button
								type="button"
								class="text-[var(--color-text-tertiary)] hover:text-[var(--color-error)]"
								onclick={() => void unfavorite(fav.favId)}
								aria-label={m['docs.favorite.remove']()}
							>
								✕
							</button>
						</span>
					{/each}
				</div>
			</section>
		{/if}

		{#if !inFolder && recentDocs.length > 0 && !search}
			<section class="mb-6">
				<h2 class="mb-1.5 flex items-center gap-1.5 text-xs font-semibold uppercase tracking-wide text-[var(--color-text-tertiary)]">
					<Clock size={12} /> {m['docs.recents']()}
				</h2>
				<div class="flex flex-wrap gap-1.5">
					{#each recentDocs as doc (doc.id)}
						<a
							href="/{slug}/docs/{doc.id}"
							class="rounded-md border border-[var(--app-border)] bg-[var(--color-bg-secondary)] px-2 py-1 text-xs text-[var(--color-text-secondary)] hover:bg-[var(--color-bg-hover)] hover:text-[var(--color-text-primary)]"
						>
							{doc.icon || '📄'} {doc.title || m['docs.untitled']()}
						</a>
					{/each}
				</div>
			</section>
		{/if}

		<section aria-label={m['docs.all_pages']()}>
			<DocsTree
				{slug}
				entries={filteredTree}
				manage
				onnewchild={(parent) => void newDoc(parent.id)}
				onnewfolder={(parent) => void newFolder(parent.id)}
				onrename={startRename}
				onduplicate={async (entry) => {
					const { duplicateDocument } = await import('$lib/api/docs');
					try {
						const copy = await duplicateDocument(slug, entry.id);
						await docsState.load(slug, true);
						goto(`/${slug}/docs/${copy.id}`);
					} catch (err) {
						appToast.apiError(err, m['docs.duplicate_failed']());
					}
				}}
				onmove={(entry) => (moveTarget = entry.id)}
				ondelete={(entry) => (deleteTarget = entry)}
			/>
		</section>
	{/if}
</div>

<DocGraphView bind:open={showGraph} {slug} />
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
