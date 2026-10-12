<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { goto } from '$app/navigation';
	import type { Editor as CoreEditor } from '@tiptap/core';
	import Collaboration from '@tiptap/extension-collaboration';
	import { TableKit } from '@tiptap/extension-table';
	import { BubbleMenu, type Editor } from 'svelte-tiptap';
	import RichEditor from '$lib/components/shared/RichEditor.svelte';
	import * as DropdownMenu from '$lib/components/ui/dropdown-menu';
	import * as Popover from '$lib/components/ui/popover';
	import * as AlertDialog from '$lib/components/ui/alert-dialog';
	import { Button } from '$lib/components/ui/button';
	import {
		deleteDocument,
		duplicateDocument,
		getDocument,
		setDocumentTags,
		updateDocument
	} from '$lib/api/docs';
	import { createFavorite, deleteFavorite, listFavorites } from '$lib/api/favorites';
	import type { Doc } from '$lib/types/docs';
	import type { WorkspaceMember as Member } from '$lib/types/workspace';
	import { DocSyncSession } from './doc-sync.svelte';
	import { WikiLink, createWikiLinkAutocomplete, collectWikiTitles } from './wiki-link.extension';
	import type { WikiLinkState } from './wiki-link.extension';
	import { docsState } from './docs.state.svelte';
	import { editorHtmlToMarkdown, markdownFilename, downloadTextFile } from './markdown';
	import { authState } from '$lib/features/auth/auth.state.svelte';
	import { presenceColorFor } from '$lib/features/presence/presence.state.svelte';
	import { sidebarState } from '$lib/features/layout/sidebar.state.svelte';
	import { appToast } from '$lib/features/toast/toast';
	import { sanitizeEditorOutput } from '$lib/security/sanitize';
	import { ROLE_HIERARCHY } from '$lib/security/roles';
	import { m } from '$lib/paraglide/messages.js';
	import BacklinksPanel from './BacklinksPanel.svelte';
	import DocComments from './DocComments.svelte';
	import MoveDocDialog from './MoveDocDialog.svelte';
	import DocShareDialog from './DocShareDialog.svelte';
	import DocRevisionsDialog from './DocRevisionsDialog.svelte';
	import Ellipsis from '@lucide/svelte/icons/ellipsis';
	import Star from '@lucide/svelte/icons/star';
	import History from '@lucide/svelte/icons/history';
	import Share2 from '@lucide/svelte/icons/share-2';
	import Copy from '@lucide/svelte/icons/copy';
	import FolderInput from '@lucide/svelte/icons/folder-input';
	import Download from '@lucide/svelte/icons/download';
	import Trash2 from '@lucide/svelte/icons/trash-2';
	import FileStack from '@lucide/svelte/icons/file-stack';
	import ArrowLeft from '@lucide/svelte/icons/arrow-left';
	import Hash from '@lucide/svelte/icons/hash';
	import TableIcon from '@lucide/svelte/icons/table';
	import Minus from '@lucide/svelte/icons/minus';
	import ArrowUpToLine from '@lucide/svelte/icons/arrow-up-to-line';
	import ArrowDownToLine from '@lucide/svelte/icons/arrow-down-to-line';
	import ArrowLeftToLine from '@lucide/svelte/icons/arrow-left-to-line';
	import ArrowRightToLine from '@lucide/svelte/icons/arrow-right-to-line';
	import Rows3 from '@lucide/svelte/icons/rows-3';
	import Columns3 from '@lucide/svelte/icons/columns-3';

	let {
		slug,
		doc,
		members = []
	}: { slug: string; doc: Doc; members?: Member[] } = $props();

	// --- Session and editor state ---
	let session = $state<DocSyncSession | null>(null);
	let editor = $state<Editor | null>(null);
	let ready = $state(false);
	// Fallback when the collab socket cannot connect: still edit locally.
	let offlineFallback = $state(false);
	let cursorTick = $state(0);
	// Seeding guard: persisted HTML enters the Yjs document exactly once.
	let seeded = false;
	let resetting = false;
	// Initial-value capture is intentional: the editor keeps local editable
	// copies of the doc metadata loaded when the page mounts.
	// svelte-ignore state_referenced_locally
	let title = $state(doc.title);
	// svelte-ignore state_referenced_locally
	let icon = $state(doc.icon ?? '');
	// svelte-ignore state_referenced_locally
	let tags = $state<string[]>(doc.tags ?? []);
	let tagInput = $state('');
	let saveState = $state<'saved' | 'saving' | 'dirty' | 'error'>('saved');
	let outgoingTitles = $state<string[]>([]);
	let readOnlyDismissed = $state(false);

	// Dialogs
	let showMove = $state(false);
	let showShare = $state(false);
	let showHistory = $state(false);
	let confirmDelete = $state(false);
	let deleting = $state(false);
	let iconPickerOpen = $state(false);

	// Wiki autocomplete
	let wikiActive = $state(false);
	let wikiQuery = $state('');
	let wikiPosition = $state({ x: 0, y: 0 });
	let wikiRange = $state<{ from: number; to: number } | null>(null);
	let wikiSelectedIndex = $state(0);

	// Write access defaults to the workspace role so the editor stays usable
	// while connecting/offline (Yjs merges local edits after reconnect); the
	// server's init verdict and FORBIDDEN refusals override it once known.
	const canWriteWorkspace = $derived(
		(ROLE_HIERARCHY[sidebarState.workspaceRole as keyof typeof ROLE_HIERARCHY] ?? 0) >=
			ROLE_HIERARCHY.member
	);
	const canWrite = $derived(
		session
			? session.readOnly
				? false
				: session.syncedOnce
					? session.canWrite
					: canWriteWorkspace
			: canWriteWorkspace
	);
	// The server explicitly told us this client may not write.
	const readOnlyNotice = $derived(
		!readOnlyDismissed &&
			session !== null &&
			(session.readOnly || (session.syncedOnce && !session.canWrite))
	);
	const status = $derived(session?.status ?? 'connecting');
	const peers = $derived(session?.peerUsers() ?? []);
	const wikiItems = $derived.by(() => {
		const q = wikiQuery.toLowerCase();
		return docsState.docs
			.filter((d) => d.id !== doc.id && !d.is_template)
			.filter((d) => !q || d.title.toLowerCase().includes(q))
			.slice(0, 8);
	});
	const remoteCursors = $derived.by(() => {
		void cursorTick;
		if (!session || !editor || editor.isDestroyed) return [];
		return session.remoteCursors(editor);
	});

	const DOC_EMOJIS = [
		'📄', '📝', '📌', '📎', '📁', '📂', '📊', '📈', '📉', '🗒️',
		'📚', '📖', '🔖', '🏷️', '🎯', '💡', '🔧', '⚙️', '🛠️', '📦',
		'🚀', '✅', '⭐', '🔥', '❤️', '🧠', '🔭', '🧪', '🌱', '🍀',
		'☕', '🍎', '🏠', '🏢', '🌍', '🎉', '🧩', '🔒', '🔑', '🗄️'
	];

	const extraSlashItems = [
		{
			id: 'wiki-link',
			label: 'Wiki link',
			icon: Hash,
			group: 'Advanced',
			keywords: ['wiki', 'link', 'page', '[[', 'ref'],
			action: (target: CoreEditor) => target.chain().focus().insertContent('[[').run()
		},
		{
			id: 'table',
			label: 'Table',
			icon: TableIcon,
			group: 'Advanced',
			keywords: ['table', 'grid', 'rows', 'columns', 'cells'],
			action: (target: CoreEditor) => target.chain().focus().insertTable({ rows: 3, cols: 3, withHeaderRow: true }).run()
		},
		{
			id: 'divider',
			label: 'Divider',
			icon: Minus,
			group: 'Advanced',
			keywords: ['divider', 'horizontal rule', 'hr', 'separator', 'line'],
			action: (target: CoreEditor) => target.chain().focus().setHorizontalRule().run()
		}
	];

	function startSession() {
		if (fallbackTimer) clearTimeout(fallbackTimer);
		const user = authState.user;
		session = new DocSyncSession({
			slug,
			docId: doc.id,
			name: user?.display_name || user?.name || 'Anonymous',
			color: presenceColorFor(user?.id ?? 'anon'),
			onRemoteUpdate: () => {
				cursorTick++;
			},
			onCursorActivity: () => {
				cursorTick++;
			},
			onReset: () => {
				void handleSessionReset();
			}
		});
		// If the socket never syncs (backend down), open the editor locally
		// after a grace period; the session reconnects in the background.
		fallbackTimer = setTimeout(() => {
			if (!session?.syncedOnce) offlineFallback = true;
		}, 6000);
	}

	onMount(() => {
		void docsState.load(slug);
		startSession();
		void loadFavoriteState();

		const onBeforeUnload = () => flushNow();
		window.addEventListener('beforeunload', onBeforeUnload);
		const onPageHide = () => flushNow();
		window.addEventListener('pagehide', onPageHide);
		return () => {
			if (fallbackTimer) clearTimeout(fallbackTimer);
			window.removeEventListener('beforeunload', onBeforeUnload);
			window.removeEventListener('pagehide', onPageHide);
		};
	});

	let fallbackTimer: ReturnType<typeof setTimeout> | null = null;

	// The editor mounts once the session replayed the document (or the
	// offline fallback fires when the backend is unreachable).
	$effect(() => {
		if (session?.syncedOnce || offlineFallback) {
			if (fallbackTimer) clearTimeout(fallbackTimer);
			ready = true;
		}
	});

	// Seed persisted HTML into the collaborative document exactly once: docs
	// created from markdown imports or templates have no Yjs update log yet,
	// so only the (empty) log would render and the first autosave would wipe
	// content_html. The setContent transaction flows through the
	// Collaboration binding, so it becomes a broadcast + persisted Yjs update.
	$effect(() => {
		if (!ready || !editor || editor.isDestroyed || seeded) return;
		if (!session || session.hasRemoteUpdates) return;
		const html = doc.content_html;
		if (!html.trim()) return;
		if (session.ydoc.getXmlFragment('default').length > 0) return;
		seeded = true;
		editor.commands.setContent(sanitizeEditorOutput(html), { emitUpdate: false });
	});

	// RichEditor applies `editable` only at creation; keep the live editor in
	// sync when the server verdict or offline fallback changes write access.
	$effect(() => {
		if (editor && !editor.isDestroyed && editor.isEditable !== canWrite) {
			editor.setEditable(canWrite);
		}
	});

	/**
	 * The server wiped the update log (revision restore): reload the
	 * persisted snapshot and rebuild the collab session in place, so seeding
	 * shows the restored content instead of merging it with stale local state.
	 */
	async function handleSessionReset() {
		if (resetting) return;
		resetting = true;
		if (saveTimer) {
			clearTimeout(saveTimer);
			saveTimer = null;
		}
		if (saveRetryTimer) {
			clearTimeout(saveRetryTimer);
			saveRetryTimer = null;
		}
		saveState = 'saved';
		offlineFallback = false;
		try {
			const fresh = await getDocument(slug, doc.id);
			doc = fresh;
			title = fresh.title;
			pendingTitle = fresh.title;
			icon = fresh.icon ?? '';
			pendingIcon = icon;
			tags = fresh.tags ?? [];
			lastSavedHtml = fresh.content_html;
			outgoingTitles = [];
		} catch {
			// Keep the current snapshot; seeding reuses it as-is.
		}
		seeded = false;
		readOnlyDismissed = false;
		session?.destroy();
		session = null;
		editor = null;
		ready = false;
		resetting = false;
		startSession();
	}

	onDestroy(() => {
		if (saveRetryTimer) {
			clearTimeout(saveRetryTimer);
			saveRetryTimer = null;
		}
		flushNow();
		session?.destroy();
		session = null;
	});

	// --- Favorite ---
	let favoriteId = $state<string | null>(null);

	async function loadFavoriteState() {
		try {
			const favorites = await listFavorites(slug);
			const found = favorites.find((f) => f.entity_type === 'document' && f.entity_id === doc.id);
			favoriteId = found?.id ?? null;
		} catch {
			favoriteId = null;
		}
	}

	async function toggleFavorite() {
		try {
			if (favoriteId) {
				await deleteFavorite(slug, favoriteId);
				favoriteId = null;
			} else {
				const created = await createFavorite(slug, 'document', doc.id);
				favoriteId = created.id;
			}
		} catch (err) {
			appToast.apiError(err, m['docs.favorite.failed']());
		}
	}

	// --- Autosave ---
	let saveTimer: ReturnType<typeof setTimeout> | null = null;
	let saveRetryTimer: ReturnType<typeof setTimeout> | null = null;
	// Autosave baselines, captured once at mount for dirty-checking.
	// svelte-ignore state_referenced_locally
	let pendingTitle = doc.title;
	// svelte-ignore state_referenced_locally
	let pendingIcon = doc.icon ?? '';
	// svelte-ignore state_referenced_locally
	let lastSavedHtml = doc.content_html;

	function scheduleSave() {
		saveState = 'dirty';
		if (saveTimer) clearTimeout(saveTimer);
		saveTimer = setTimeout(() => void save(), 1200);
	}

	async function save() {
		if (!editor) return;
		if (!canWrite) {
			saveState = 'saved';
			return;
		}
		const html = sanitizeEditorOutput(editor.getHTML());
		const text = editor.getText();
		const wikiTitles = collectWikiTitles(editor);
		outgoingTitles = wikiTitles;
		if (html === lastSavedHtml && pendingTitle === title && pendingIcon === icon) {
			saveState = 'saved';
			return;
		}
		saveState = 'saving';
		try {
			await updateDocument(slug, doc.id, {
				title: pendingTitle,
				icon: pendingIcon,
				content_html: html,
				content_text: text,
				wiki_titles: wikiTitles
			});
			lastSavedHtml = html;
			saveState = 'saved';
			if (saveRetryTimer) {
				clearTimeout(saveRetryTimer);
				saveRetryTimer = null;
			}
			docsState.scheduleRefresh();
		} catch {
			saveState = 'error';
			scheduleSaveRetry();
		}
	}

	/**
	 * Failed saves (typically offline) retry periodically; the collab socket
	 * also re-pushes the Yjs state once reconnected, so nothing is lost.
	 */
	function scheduleSaveRetry() {
		if (saveRetryTimer) return;
		saveRetryTimer = setTimeout(() => {
			saveRetryTimer = null;
			if (saveState === 'error') void save();
		}, 5000);
	}

	/** Best-effort synchronous flush on unload/navigation. */
	function flushNow() {
		if (saveTimer) clearTimeout(saveTimer);
		saveTimer = null;
		if (!editor || editor.isDestroyed || !canWrite) return;
		if (saveState !== 'dirty') return;
		const html = sanitizeEditorOutput(editor.getHTML());
		if (html === lastSavedHtml && pendingTitle === title && pendingIcon === icon) return;
		const payload = JSON.stringify({
			title: pendingTitle,
			icon: pendingIcon,
			content_html: html,
			content_text: editor.getText(),
			wiki_titles: collectWikiTitles(editor)
		});
		lastSavedHtml = html;
		try {
			fetch(`/api/workspaces/${slug}/docs/${doc.id}`, {
				method: 'PATCH',
				credentials: 'include',
				headers: { 'Content-Type': 'application/json' },
				body: payload,
				keepalive: true
			}).catch(() => undefined);
			saveState = 'saved';
		} catch {
			// Unload paths cannot retry.
		}
	}

	function onTitleInput(value: string) {
		title = value;
		pendingTitle = value;
		scheduleSave();
	}

	function chooseIcon(emoji: string) {
		icon = emoji;
		pendingIcon = emoji;
		iconPickerOpen = false;
		scheduleSave();
	}

	function clearIcon() {
		icon = '';
		pendingIcon = '';
		iconPickerOpen = false;
		scheduleSave();
	}

	async function applyTags(next: string[]) {
		tags = next;
		try {
			await setDocumentTags(slug, doc.id, next);
			docsState.scheduleRefresh();
		} catch (err) {
			appToast.apiError(err, m['docs.tags.failed']());
		}
	}

	function addTagFromInput() {
		const name = tagInput.trim().replace(/^#/, '');
		if (!name || tags.includes(name)) {
			tagInput = '';
			return;
		}
		tagInput = '';
		void applyTags([...tags, name]);
	}

	// --- Wiki autocomplete ---
	function handleWikiStateChange(state: WikiLinkState) {
		wikiActive = state.active;
		wikiQuery = state.query;
		wikiPosition = { x: state.x, y: state.y };
		wikiRange = state.range;
		if (state.active) wikiSelectedIndex = 0;
	}

	function handleWikiNavigate(direction: 'up' | 'down') {
		if (wikiItems.length === 0) return;
		if (direction === 'down') wikiSelectedIndex = (wikiSelectedIndex + 1) % wikiItems.length;
		else wikiSelectedIndex = (wikiSelectedIndex - 1 + wikiItems.length) % wikiItems.length;
	}

	function handleWikiSelect() {
		if (!editor || !wikiRange) return;
		const item = wikiItems[wikiSelectedIndex] ?? wikiItems[0];
		if (!item) return;
		editor
			.chain()
			.focus()
			.deleteRange({ from: wikiRange.from, to: wikiRange.to })
			.insertContent({
				type: 'wikiLink',
				attrs: { title: item.title, docId: item.id }
			})
			.insertContent(' ')
			.run();
		wikiActive = false;
		wikiRange = null;
	}

	// Clicking a wiki link opens the page.
	function handleContentClick(event: MouseEvent) {
		const anchor = (event.target as HTMLElement).closest?.('a[data-type="wiki"]');
		if (!anchor) return;
		event.preventDefault();
		const title = anchor.getAttribute('data-wiki-title');
		const target = docsState.docs.find(
			(d) => d.title.toLowerCase() === (title ?? '').toLowerCase()
		);
		if (target) goto(`/${slug}/docs/${target.id}`);
		else appToast.info(m['docs.wiki.unresolved']({ title: title ?? '' }));
	}

	// TableKit ships the table nodes/commands but no bubble menu in this
	// version, so docs render their own row/column controls when the caret
	// sits inside a table.
	function showTableMenu(props: { editor: CoreEditor }): boolean {
		return props.editor.isEditable && props.editor.isActive('table');
	}

	function tableBtnClass(): string {
		return 'flex h-7 w-7 items-center justify-center rounded text-[var(--color-text-tertiary)] hover:bg-[var(--color-bg-hover)] hover:text-[var(--color-text-secondary)]';
	}

	// --- Document actions ---
	async function duplicate() {
		try {
			const copy = await duplicateDocument(slug, doc.id);
			await docsState.load(slug, true);
			appToast.success(m['docs.duplicated']());
			goto(`/${slug}/docs/${copy.id}`);
		} catch (err) {
			appToast.apiError(err, m['docs.duplicate_failed']());
		}
	}

	function exportMarkdown() {
		if (!editor) return;
		const markdown = editorHtmlToMarkdown(sanitizeEditorOutput(editor.getHTML()));
		downloadTextFile(markdownFilename(title), `# ${title || m['docs.untitled']()}\n\n${markdown}\n`);
	}

	async function toggleTemplate() {
		try {
			await updateDocument(slug, doc.id, { is_template: !doc.is_template });
			doc.is_template = !doc.is_template;
			docsState.scheduleRefresh();
			appToast.success(doc.is_template ? m['docs.template.saved']() : m['docs.template.removed']());
		} catch (err) {
			appToast.apiError(err, m['docs.template.failed']());
		}
	}

	async function remove() {
		if (deleting) return;
		deleting = true;
		try {
			await deleteDocument(slug, doc.id);
			appToast.success(m['docs.deleted']());
			await docsState.load(slug, true);
			goto(`/${slug}/docs`);
		} catch (err) {
			appToast.apiError(err, m['docs.delete_failed']());
		} finally {
			deleting = false;
			confirmDelete = false;
		}
	}

	const saveLabel = $derived.by(() => {
		if (saveState === 'saving') return m['docs.saving']();
		if (saveState === 'dirty') return m['docs.unsaved']();
		if (saveState === 'error') return m['docs.save_error']();
		if (status !== 'live') return m['docs.offline']();
		return m['docs.saved']();
	});

	const extensions = $derived.by(() => {
		if (!session) return [];
		return [
			TableKit.configure({ table: { resizable: true } }),
			WikiLink,
			createWikiLinkAutocomplete({
				onStateChange: handleWikiStateChange,
				onNavigate: handleWikiNavigate,
				onSelect: handleWikiSelect
			}),
			Collaboration.configure({ document: session.ydoc })
		];
	});

	const breadcrumb = $derived(docsState.breadcrumb(doc.id));
</script>

<div class="mx-auto flex min-h-full w-full max-w-4xl flex-col px-6 pb-24 pt-6">
	<!-- Toolbar row -->
	<div class="mb-2 flex items-center gap-1.5">
		<Button
			variant="ghost"
			size="icon"
			onclick={() => goto(`/${slug}/docs`)}
			title={m['docs.back_to_docs']()}
			aria-label={m['docs.back_to_docs']()}
		>
			<ArrowLeft size={16} />
		</Button>
		<nav class="flex min-w-0 items-center gap-1 text-xs text-[var(--color-text-tertiary)]" aria-label="Breadcrumb">
			{#each breadcrumb.slice(0, -1) as crumb (crumb.id)}
				<a href="/{slug}/docs/{crumb.id}" class="truncate hover:text-[var(--color-text-secondary)]">
					{crumb.icon ? crumb.icon + ' ' : ''}{crumb.title || m['docs.untitled']()}
				</a>
				<span>/</span>
			{/each}
		</nav>

		<div class="ml-auto flex items-center gap-2">
			<!-- Presence avatars -->
			<div class="flex items-center -space-x-1.5">
				{#each peers as peer (peer.user_id)}
					<span
						class="flex h-6 w-6 items-center justify-center rounded-full border border-[var(--color-bg-secondary)] text-[10px] font-semibold text-white"
						style="background: {peer.color}"
						title={peer.name}
					>
						{peer.name.slice(0, 1).toUpperCase()}
					</span>
				{/each}
			</div>
			<span class="text-xs {saveState === 'error' ? 'text-[var(--color-error)]' : 'text-[var(--color-text-tertiary)]'}">
				{saveLabel}
			</span>
			<button
				type="button"
				class="rounded p-1.5 {favoriteId
					? 'text-yellow-500'
					: 'text-[var(--color-text-tertiary)] hover:bg-[var(--color-bg-hover)] hover:text-[var(--color-text-secondary)]'}"
				onclick={() => void toggleFavorite()}
				title={m['docs.favorite']()}
				aria-label={m['docs.favorite']()}
			>
				<Star size={15} fill={favoriteId ? 'currentColor' : 'none'} />
			</button>
			<DropdownMenu.Root>
				<DropdownMenu.Trigger
					class="rounded p-1.5 text-[var(--color-text-tertiary)] hover:bg-[var(--color-bg-hover)] hover:text-[var(--color-text-secondary)]"
				>
					<Ellipsis size={16} />
				</DropdownMenu.Trigger>
				<DropdownMenu.Content align="end" class="w-48">
					<DropdownMenu.Item onclick={() => (showShare = true)}>
						<Share2 size={14} class="mr-2" />
						{m['docs.share.title']()}
					</DropdownMenu.Item>
					<DropdownMenu.Item onclick={() => (showHistory = true)}>
						<History size={14} class="mr-2" />
						{m['docs.history.title']()}
					</DropdownMenu.Item>
					<DropdownMenu.Item onclick={() => void duplicate()}>
						<Copy size={14} class="mr-2" />
						{m['docs.duplicate']()}
					</DropdownMenu.Item>
					<DropdownMenu.Item onclick={() => (showMove = true)}>
						<FolderInput size={14} class="mr-2" />
						{m['docs.move.title']()}
					</DropdownMenu.Item>
					<DropdownMenu.Item onclick={exportMarkdown}>
						<Download size={14} class="mr-2" />
						{m['docs.export_markdown']()}
					</DropdownMenu.Item>
					<DropdownMenu.Item onclick={() => void toggleTemplate()}>
						<FileStack size={14} class="mr-2" />
						{doc.is_template ? m['docs.template.remove']() : m['docs.template.save']()}
					</DropdownMenu.Item>
					<DropdownMenu.Separator />
					<DropdownMenu.Item onclick={() => (confirmDelete = true)} class="text-[var(--color-error)]">
						<Trash2 size={14} class="mr-2" />
						{m['docs.delete']()}
					</DropdownMenu.Item>
				</DropdownMenu.Content>
			</DropdownMenu.Root>
		</div>
	</div>

	<!-- Read-only notice (server refused writes for this client) -->
	{#if readOnlyNotice}
		<div
			class="mb-2 flex items-center gap-2 rounded-md border border-[var(--app-border)] bg-[var(--color-bg-secondary)] px-3 py-2 text-xs text-[var(--color-text-secondary)]"
			role="status"
		>
			<span>{m['docs.readonly.notice']()}</span>
			<button
				type="button"
				class="ml-auto rounded p-1 text-[var(--color-text-tertiary)] hover:bg-[var(--color-bg-hover)] hover:text-[var(--color-text-secondary)]"
				onclick={() => (readOnlyDismissed = true)}
				aria-label={m['docs.readonly.dismiss']()}
			>
				✕
			</button>
		</div>
	{/if}

	<!-- Title -->
	<div class="mb-1 flex items-start gap-2">
		<Popover.Root open={iconPickerOpen} onOpenChange={(o) => (iconPickerOpen = canWrite ? o : false)}>
			<Popover.Trigger
				class="mt-1 rounded px-1 text-2xl leading-none hover:bg-[var(--color-bg-hover)]"
				title={m['docs.icon.change']()}
			>
				{icon || '📄'}
			</Popover.Trigger>
			<Popover.Content side="bottom" align="start" class="w-64">
				<div class="grid grid-cols-10 gap-1">
					{#each DOC_EMOJIS as emoji (emoji)}
						<button
							type="button"
							class="rounded p-1 text-lg hover:bg-[var(--color-bg-hover)]"
							onclick={() => chooseIcon(emoji)}
						>
							{emoji}
						</button>
					{/each}
				</div>
				{#if icon}
					<Button variant="ghost" size="sm" class="mt-2 w-full" onclick={clearIcon}>
						{m['docs.icon.remove']()}
					</Button>
				{/if}
			</Popover.Content>
		</Popover.Root>
		<input
			type="text"
			class="w-full bg-transparent text-3xl font-bold text-[var(--color-text-primary)] outline-none placeholder:text-[var(--color-text-tertiary)]"
			placeholder={m['docs.title_placeholder']()}
			readonly={!canWrite}
			value={title}
			oninput={(e) => onTitleInput(e.currentTarget.value)}
		/>
	</div>

	<!-- Tags -->
	<div class="mb-4 flex flex-wrap items-center gap-1.5">
		{#each tags as tag (tag)}
			<span class="flex items-center gap-1 rounded-full border border-[var(--app-border)] bg-[var(--color-bg-secondary)] px-2 py-0.5 text-xs text-[var(--color-text-secondary)]">
				#{tag}
				{#if canWrite}
					<button
						type="button"
						class="text-[var(--color-text-tertiary)] hover:text-[var(--color-error)]"
						onclick={() => void applyTags(tags.filter((t) => t !== tag))}
						aria-label={m['docs.tags.remove']()}
					>
						✕
					</button>
				{/if}
			</span>
		{/each}
		{#if canWrite}
			<input
				type="text"
				class="w-24 bg-transparent text-xs text-[var(--color-text-secondary)] outline-none placeholder:text-[var(--color-text-tertiary)]"
				placeholder={m['docs.tags.add']()}
				value={tagInput}
				oninput={(e) => (tagInput = e.currentTarget.value)}
				onkeydown={(e) => {
					if (e.key === 'Enter' || e.key === ',') {
						e.preventDefault();
						addTagFromInput();
					}
				}}
			/>
		{/if}
	</div>

	<!-- Editor -->
	{#if ready && session}
		<!-- svelte-ignore a11y_no_static_element_interactions -->
	<div class="docs-editor-shell" role="presentation" onclick={handleContentClick}>
			<RichEditor
				content=""
				placeholder={m['docs.editor_placeholder']()}
				editable={canWrite}
				borderless
				minHeight="40vh"
				uploadUrl="/api/workspaces/{slug}/upload"
				workspaceSlug={slug}
				{members}
				remoteCursors={remoteCursors}
				oncursorchange={() => editor && session?.sendCursor(editor)}
				extraExtensions={extensions}
				extraSlashItems={extraSlashItems}
				disableUndoRedo
				suppressExternalContent
				showAIButton={false}
				onready={(instance) => {
					editor = instance;
					outgoingTitles = collectWikiTitles(instance);
				}}
				onupdate={() => {
					scheduleSave();
					cursorTick++;
				}}
			/>
		</div>
		{#if editor && canWrite}
			<BubbleMenu {editor} shouldShow={showTableMenu}>
				{#snippet children()}
					<div class="table-bubble" role="toolbar" aria-label={m['docs.table.menu']()}>
						<button
							type="button"
							class={tableBtnClass()}
							onclick={() => editor?.chain().focus().addRowBefore().run()}
							title={m['docs.table.add_row_above']()}
							aria-label={m['docs.table.add_row_above']()}
						>
							<ArrowUpToLine size={14} />
						</button>
						<button
							type="button"
							class={tableBtnClass()}
							onclick={() => editor?.chain().focus().addRowAfter().run()}
							title={m['docs.table.add_row_below']()}
							aria-label={m['docs.table.add_row_below']()}
						>
							<ArrowDownToLine size={14} />
						</button>
						<button
							type="button"
							class={tableBtnClass()}
							onclick={() => editor?.chain().focus().deleteRow().run()}
							title={m['docs.table.delete_row']()}
							aria-label={m['docs.table.delete_row']()}
						>
							<Rows3 size={14} />
						</button>
						<span class="table-bubble-separator"></span>
						<button
							type="button"
							class={tableBtnClass()}
							onclick={() => editor?.chain().focus().addColumnBefore().run()}
							title={m['docs.table.add_column_left']()}
							aria-label={m['docs.table.add_column_left']()}
						>
							<ArrowLeftToLine size={14} />
						</button>
						<button
							type="button"
							class={tableBtnClass()}
							onclick={() => editor?.chain().focus().addColumnAfter().run()}
							title={m['docs.table.add_column_right']()}
							aria-label={m['docs.table.add_column_right']()}
						>
							<ArrowRightToLine size={14} />
						</button>
						<button
							type="button"
							class={tableBtnClass()}
							onclick={() => editor?.chain().focus().deleteColumn().run()}
							title={m['docs.table.delete_column']()}
							aria-label={m['docs.table.delete_column']()}
						>
							<Columns3 size={14} />
						</button>
						<span class="table-bubble-separator"></span>
						<button
							type="button"
							class="table-bubble-danger"
							onclick={() => editor?.chain().focus().deleteTable().run()}
							title={m['docs.table.delete_table']()}
							aria-label={m['docs.table.delete_table']()}
						>
							<Trash2 size={14} />
						</button>
					</div>
				{/snippet}
			</BubbleMenu>
		{/if}
	{:else}
		<div class="flex h-64 items-center justify-center text-sm text-[var(--color-text-tertiary)]">
			{m['docs.loading']()}
		</div>
	{/if}

	<!-- Wiki link autocomplete -->
	{#if wikiActive && wikiItems.length > 0 && editor}
		<div
			class="fixed z-50 max-h-56 w-72 overflow-y-auto rounded-md border border-[var(--app-border)] bg-[var(--color-bg-secondary)] p-1 shadow-xl"
			style="left: {wikiPosition.x}px; top: {wikiPosition.y}px"
			role="listbox"
		>
			{#each wikiItems as item, index (item.id)}
				<button
					type="button"
					role="option"
					aria-selected={index === wikiSelectedIndex}
					class="flex w-full items-center gap-2 rounded px-2 py-1.5 text-left text-sm {index === wikiSelectedIndex
						? 'bg-[var(--color-bg-hover)] text-[var(--color-text-primary)]'
						: 'text-[var(--color-text-secondary)]'}"
					onmousedown={(e) => {
						e.preventDefault();
						wikiSelectedIndex = index;
						handleWikiSelect();
					}}
				>
					{item.icon ? item.icon : '📄'}
					<span class="truncate">{item.title || m['docs.untitled']()}</span>
				</button>
			{/each}
		</div>
	{/if}

	{#if editor}
		<BacklinksPanel {slug} docId={doc.id} outgoingTitles={outgoingTitles} />
		<DocComments {slug} docId={doc.id} {members} readonly={!canWrite} />
	{/if}
</div>

<MoveDocDialog bind:open={showMove} {slug} docId={doc.id} onmoved={() => docsState.scheduleRefresh()} />
<DocShareDialog bind:open={showShare} {slug} docId={doc.id} docTitle={title} />
<DocRevisionsDialog
	bind:open={showHistory}
	{slug}
	docId={doc.id}
	onrestored={() => goto(`/${slug}/docs/${doc.id}`, { replaceState: true })}
/>

<AlertDialog.Root bind:open={confirmDelete}>
	<AlertDialog.Content>
		<AlertDialog.Header>
			<AlertDialog.Title>{m['docs.delete_confirm_title']()}</AlertDialog.Title>
			<AlertDialog.Description>
				{m['docs.delete_confirm']({ title: title || m['docs.untitled']() })}
			</AlertDialog.Description>
		</AlertDialog.Header>
		<AlertDialog.Footer>
			<AlertDialog.Cancel variant="outline" disabled={deleting}>{m['docs.cancel']()}</AlertDialog.Cancel>
			<AlertDialog.Action variant="destructive" onclick={remove} disabled={deleting}>
				{deleting ? m['docs.working']() : m['docs.delete']()}
			</AlertDialog.Action>
		</AlertDialog.Footer>
	</AlertDialog.Content>
</AlertDialog.Root>

<style>
	/* Wiki links inside the doc editor */
	:global(.docs-editor-shell a[data-type="wiki"]) {
		color: var(--app-accent-light);
		text-decoration: none;
		border-bottom: 1px dashed var(--app-accent-light);
		cursor: pointer;
	}
	:global(.docs-editor-shell a[data-type="wiki"]:hover) {
		border-bottom-style: solid;
	}
	/* Table bubble menu (row/column management) */
	:global(.table-bubble) {
		display: flex;
		align-items: center;
		gap: 2px;
		z-index: 50;
		background: var(--color-bg-secondary);
		border: 1px solid var(--app-border);
		border-radius: 8px;
		padding: 4px;
		box-shadow: 0 4px 12px rgba(0, 0, 0, 0.3);
	}
	:global(.table-bubble-separator) {
		width: 1px;
		height: 16px;
		background: var(--app-border);
		margin: 0 4px;
	}
	:global(.table-bubble-danger) {
		display: flex;
		height: 28px;
		width: 28px;
		align-items: center;
		justify-content: center;
		border-radius: 4px;
		color: var(--color-text-tertiary);
	}
	:global(.table-bubble-danger:hover) {
		background: var(--color-bg-hover);
		color: var(--color-error);
	}
	/* Tables (Notion-style minimal chrome) */
	:global(.docs-editor-shell .tiptap table) {
		border-collapse: collapse;
		table-layout: fixed;
		width: 100%;
		overflow: hidden;
		margin: 0.5rem 0;
	}
	:global(.docs-editor-shell .tiptap table td),
	:global(.docs-editor-shell .tiptap table th) {
		border: 1px solid var(--app-border);
		padding: 4px 8px;
		vertical-align: top;
		position: relative;
		min-width: 3rem;
	}
	:global(.docs-editor-shell .tiptap table th) {
		background: var(--color-bg-tertiary);
		font-weight: 600;
		text-align: left;
	}
	:global(.docs-editor-shell .tiptap .selectedCell::after) {
		content: '';
		position: absolute;
		inset: 0;
		background: color-mix(in srgb, var(--app-accent) 12%, transparent);
		pointer-events: none;
	}
	:global(.docs-editor-shell .tiptap .column-resize-handle) {
		position: absolute;
		right: -2px;
		top: 0;
		bottom: -2px;
		width: 4px;
		background: var(--app-accent);
		pointer-events: none;
	}
	:global(.docs-editor-shell .tiptap.resize-cursor) {
		cursor: col-resize;
	}
</style>
