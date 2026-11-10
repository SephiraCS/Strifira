<script lang="ts">
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { getDocument } from '$lib/api/docs';
	import { listMembers } from '$lib/api/members';
	import type { Doc } from '$lib/types/docs';
	import type { WorkspaceMember } from '$lib/types/workspace';
	import { docsState } from '$lib/features/docs/docs.state.svelte';
	import ErrorState from '$lib/components/shared/ErrorState.svelte';
	import { m } from '$lib/paraglide/messages.js';

	const slug = $derived(page.params.workspaceSlug ?? '');
	const docId = $derived(page.params.docId ?? '');

	let doc = $state<Doc | null>(null);
	let members = $state<WorkspaceMember[]>([]);
	let loading = $state(true);
	let failed = $state(false);
	let loadedDocId = '';

	// The editor and its collaboration stack are the heaviest chunk graph in
	// the app; loading them after the page shell paints keeps document opens
	// fast. The module is cached after the first load, so later navigations
	// mount the editor immediately.
	let DocEditorComponent = $state<
		| (typeof import('$lib/features/docs/DocEditor.svelte'))['default']
		| null
	>(null);
	let editorChunkFailed = $state(false);

	async function loadEditorChunk() {
		if (DocEditorComponent || editorChunkFailed) return;
		try {
			const module = await import('$lib/features/docs/DocEditor.svelte');
			DocEditorComponent = module.default;
		} catch {
			editorChunkFailed = true;
		}
	}

	async function load(id: string) {
		loading = true;
		failed = false;
		try {
			const [loaded, mems] = await Promise.all([getDocument(slug, id), listMembers(slug).catch(() => [])]);
			if (id !== docId) return;
			members = mems;
			// Folders are containers, not pages: land on their listing instead
			// of ever mounting the editor.
			if (loaded.is_folder) {
				await docsState.load(slug);
				goto(`/${slug}/docs?folder=${id}`, { replaceState: true });
				return;
			}
			doc = loaded;
			loadedDocId = id;
			docsState.markOpened(slug, id);
		} catch {
			if (id === docId) failed = true;
		} finally {
			if (id === docId) loading = false;
		}
	}

	onMount(() => {
		void docsState.load(slug);
		void load(docId);
		void loadEditorChunk();
	});

	// SvelteKit reuses this component across doc-id changes; reload then.
	$effect(() => {
		if (docId && docId !== loadedDocId && !loading) void load(docId);
	});
</script>

{#if loading}
	<div class="flex h-full items-center justify-center py-24 text-sm text-[var(--color-text-tertiary)]">
		{m['docs.loading']()}
	</div>
{:else if failed || !doc}
	<div class="flex h-full items-center justify-center py-24">
		<ErrorState message={m['docs.not_found']()} />
	</div>
{:else if DocEditorComponent}
	{#key docId}
		<DocEditorComponent {slug} {doc} {members} />
	{/key}
{:else if editorChunkFailed}
	<div class="flex h-full items-center justify-center py-24">
		<ErrorState message={m['docs.editor.load_failed']()} onretry={() => {
			editorChunkFailed = false;
			void loadEditorChunk();
		}} />
	</div>
{:else}
	<!-- Skeleton mirroring the editor layout while the chunk graph loads. -->
	<div class="mx-auto w-full max-w-4xl px-6 py-8" aria-hidden="true">
		<div class="mb-4 flex items-center gap-2">
			<div class="h-4 w-24 animate-pulse rounded bg-[var(--color-bg-tertiary)]"></div>
		</div>
		<div class="mb-2 h-9 w-2/3 animate-pulse rounded bg-[var(--color-bg-tertiary)]"></div>
		<div class="mb-6 h-4 w-32 animate-pulse rounded bg-[var(--color-bg-tertiary)]"></div>
		{#each Array(6) as _, i (i)}
			<div class="mb-3 h-4 animate-pulse rounded bg-[var(--color-bg-tertiary)]" style="width: {88 - (i % 3) * 14}%"></div>
		{/each}
	</div>
{/if}
