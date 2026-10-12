<script lang="ts">
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { getDocument } from '$lib/api/docs';
	import { listMembers } from '$lib/api/members';
	import type { Doc } from '$lib/types/docs';
	import type { WorkspaceMember } from '$lib/types/workspace';
	import DocEditor from '$lib/features/docs/DocEditor.svelte';
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

	async function load(id: string) {
		loading = true;
		failed = false;
		try {
			const [loaded, mems] = await Promise.all([getDocument(slug, id), listMembers(slug).catch(() => [])]);
			if (id !== docId) return;
			doc = loaded;
			members = mems;
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
{:else}
	{#key docId}
		<DocEditor {slug} {doc} {members} />
	{/key}
{/if}
