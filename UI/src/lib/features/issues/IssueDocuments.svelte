<script lang="ts">
	import { docsState } from '$lib/features/docs/docs.state.svelte';
	import { linkDocIssue, listDocIssues, listIssueDocuments, unlinkDocIssue } from '$lib/api/docs';
	import type { DocIssueLink, IssueDocSummary } from '$lib/types/docs';
	import { appToast } from '$lib/features/toast/toast';
	import { m } from '$lib/paraglide/messages.js';
	import FileText from '@lucide/svelte/icons/file-text';
	import Folder from '@lucide/svelte/icons/folder';
	import Link2 from '@lucide/svelte/icons/link-2';
	import Plus from '@lucide/svelte/icons/plus';
	import X from '@lucide/svelte/icons/x';
	import ChevronRight from '@lucide/svelte/icons/chevron-right';

	let { slug, identifier }: { slug: string; identifier: string } = $props();

	let docs = $state<IssueDocSummary[]>([]);
	let loading = $state(true);
	let loadedFor = '';
	let expanded = $state(true);
	let linking = $state(false);
	let showPicker = $state(false);
	let query = $state('');

	const candidates = $derived.by(() => {
		const linkedIds = new Set(docs.map((d) => d.id));
		const q = query.trim().toLowerCase();
		return docsState.docs
			.filter((d) => !d.is_template && !linkedIds.has(d.id))
			.filter((d) => !q || d.title.toLowerCase().includes(q))
			.slice(0, 8);
	});

	async function load() {
		if (loadedFor === identifier) return;
		loadedFor = identifier;
		loading = true;
		try {
			docs = await listIssueDocuments(slug, identifier);
		} catch {
			docs = [];
		} finally {
			loading = false;
		}
	}

	async function refresh() {
		try {
			docs = await listIssueDocuments(slug, identifier);
		} catch {
			// Keep the current list; failures toast on write paths.
		}
	}

	async function link(docId: string) {
		if (linking) return;
		linking = true;
		showPicker = false;
		query = '';
		try {
			await linkDocIssue(slug, docId, identifier);
			await refresh();
		} catch (err) {
			appToast.apiError(err, m['docs.link_docs.failed']());
		} finally {
			linking = false;
		}
	}

	async function unlink(docId: string) {
		if (linking) return;
		linking = true;
		try {
			await unlinkDocIssue(slug, docId, identifier);
			await refresh();
		} catch (err) {
			appToast.apiError(err, m['docs.link_docs.failed']());
		} finally {
			linking = false;
		}
	}

	$effect(() => {
		void load();
	});
</script>

<div class="overflow-hidden rounded-lg border border-[var(--app-border)] bg-[var(--color-bg-secondary)]">
	<div class="flex items-center gap-2 px-3 py-2.5">
		<button
			type="button"
			class="flex min-w-0 flex-1 items-center gap-2 text-[11px] font-medium uppercase tracking-wider text-[var(--color-text-tertiary)] hover:text-[var(--color-text-secondary)]"
			onclick={() => (expanded = !expanded)}
		>
			<ChevronRight size={12} class="shrink-0 transition-transform {expanded ? 'rotate-90' : ''}" />
			{m['docs.link_docs.title']()}
			{#if docs.length > 0}
				<span class="rounded-full bg-[var(--color-bg-tertiary)] px-1.5 text-xs normal-case text-[var(--color-text-tertiary)]">
					{docs.length}
				</span>
			{/if}
		</button>
		<button
			type="button"
			class="flex h-6 w-6 shrink-0 items-center justify-center rounded-full text-[var(--color-text-tertiary)] transition-colors hover:bg-[var(--color-bg-hover)] hover:text-[var(--color-text-primary)]"
			onclick={() => {
				showPicker = !showPicker;
				void docsState.load(slug);
			}}
			title={m['docs.link_docs.title']()}
			aria-label={m['docs.link_docs.title']()}
		>
			<Plus size={13} />
		</button>
	</div>

	{#if expanded}
		<div class="border-t border-[var(--app-border)] px-1.5 pb-2 pt-1">
			{#if showPicker}
				<div class="mb-1 px-1">
					<input
						type="text"
						class="w-full rounded-md border border-[var(--app-border)] bg-[var(--color-bg-primary)] px-2.5 py-1.5 text-sm text-[var(--color-text-primary)] outline-none placeholder:text-[var(--color-text-tertiary)] focus:border-[var(--app-accent)]"
						placeholder={m['docs.link_docs.placeholder']()}
						value={query}
						oninput={(e) => (query = e.currentTarget.value)}
						onkeydown={(e) => {
							if (e.key === 'Enter' && candidates[0]) {
								e.preventDefault();
								void link(candidates[0].id);
							} else if (e.key === 'Escape') {
								showPicker = false;
							}
						}}
					/>
					{#if candidates.length > 0}
						<div class="mt-1 space-y-0.5">
							{#each candidates as item (item.id)}
								<button
									type="button"
									class="flex w-full items-center gap-2 rounded px-2 py-1.5 text-left text-sm text-[var(--color-text-secondary)] hover:bg-[var(--color-bg-hover)] hover:text-[var(--color-text-primary)]"
									onclick={() => void link(item.id)}
								>
									<span class="shrink-0">{item.is_folder ? '📁' : item.icon || '📄'}</span>
									<span class="min-w-0 flex-1 truncate">{item.title || m['docs.untitled']()}</span>
								</button>
							{/each}
						</div>
					{/if}
				</div>
			{/if}

			{#if loading}
				<p class="px-2 py-2 text-xs text-[var(--color-text-tertiary)]">{m['docs.loading']()}</p>
			{:else if docs.length === 0}
				<p class="px-2 py-2 text-xs text-[var(--color-text-tertiary)]">{m['docs.link_docs.empty']()}</p>
			{:else}
				{#each docs as doc (doc.id)}
					<div class="group flex items-center gap-2 rounded-md px-2 py-1.5 hover:bg-[var(--color-bg-hover)]">
						<span class="shrink-0 text-[var(--color-text-tertiary)]">
							{#if doc.is_folder}
								<Folder size={14} />
							{:else}
								<FileText size={14} />
							{/if}
						</span>
						<a
							href="/{slug}/docs/{doc.id}"
							class="min-w-0 flex-1 truncate text-sm text-[var(--color-text-secondary)] hover:text-[var(--color-text-primary)]"
						>
							{doc.title || m['docs.untitled']()}
						</a>
						<button
							type="button"
							class="shrink-0 rounded p-0.5 text-[var(--color-text-tertiary)] opacity-0 transition-opacity group-hover:opacity-100 hover:bg-[var(--color-bg-tertiary)] hover:text-[var(--color-error)]"
							onclick={() => void unlink(doc.id)}
							title={m['docs.link_issues.remove']()}
							aria-label={m['docs.link_issues.remove']()}
						>
							<X size={13} />
						</button>
					</div>
				{/each}
			{/if}
		</div>
	{/if}
</div>
