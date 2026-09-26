<script lang="ts">
	import { page } from '$app/state';
	import { listIssues, setIssueArchived } from '$lib/api/issues';
	import type { Issue } from '$lib/types/issue';
	import { appToast } from '$lib/features/toast/toast';
	import { m } from '$lib/paraglide/messages.js';
	import { ArchiveRestore } from 'lucide-svelte';

	const slug = $derived(page.params.workspaceSlug ?? '');
	let issues = $state<Issue[]>([]);
	let currentPage = $state(1);
	let hasMore = $state(false);
	let loading = $state(false);
	let restoring = $state<string | null>(null);
	let loadRequestId = 0;

	$effect(() => {
		if (!slug) return;
		issues = [];
		hasMore = false;
		void loadPage(1);
	});

	async function loadPage(nextPage: number) {
		const requestId = ++loadRequestId;
		const requestedSlug = slug;
		loading = true;
		try {
			const result = await listIssues(requestedSlug, {
				archived: 'true',
				page: String(nextPage),
				per_page: '50',
				sort: 'updated_at',
				order: 'desc'
			});
			if (requestId !== loadRequestId || requestedSlug !== slug) return;
			issues = nextPage === 1 ? result.data : [...issues, ...result.data];
			currentPage = nextPage;
			hasMore = result.has_more;
		} catch (err: any) {
			if (requestId === loadRequestId) appToast.apiError(err, m['issues.archive.load_failed']());
		} finally {
			if (requestId === loadRequestId) loading = false;
		}
	}

	async function restore(issue: Issue) {
		restoring = issue.id;
		try {
			await setIssueArchived(slug, issue.identifier, false);
			issues = issues.filter((item) => item.id !== issue.id);
			await loadPage(1);
			appToast.success(m['issues.archive.restored']());
		} catch (err: any) {
			appToast.apiError(err, m['issues.archive.failed']());
		} finally {
			restoring = null;
		}
	}
</script>

<div class="mx-auto max-w-3xl px-6 py-10">
	<h1 class="text-2xl font-semibold text-[var(--color-text-primary)]">{m['issues.archive.title']()}</h1>
	<p class="mt-1 text-sm text-[var(--color-text-tertiary)]">{m['issues.archive.description']()}</p>
	<div class="mt-6 overflow-hidden rounded-lg border border-[var(--app-border)] bg-[var(--color-bg-secondary)]">
		{#each issues as issue (issue.id)}
			<div class="flex items-center gap-4 border-b border-[var(--app-border)] px-4 py-3 last:border-b-0">
				<a
					href="/{slug}/issue/{issue.identifier}"
					class="min-w-0 flex-1 text-sm text-[var(--color-text-primary)] hover:underline"
				>
					<span class="mr-2 text-[var(--color-text-tertiary)]">{issue.identifier}</span>{issue.title}
				</a>
				<button
					type="button"
					onclick={() => restore(issue)}
					disabled={restoring === issue.id}
					class="flex shrink-0 items-center gap-1 text-sm text-[var(--color-text-secondary)] hover:text-[var(--color-text-primary)] disabled:opacity-50"
				>
					<ArchiveRestore size={14} />{m['issues.archive.restore']()}
				</button>
			</div>
		{/each}
		{#if issues.length === 0 && !loading}
			<p class="px-4 py-8 text-center text-sm text-[var(--color-text-tertiary)]">{m['issues.archive.empty']()}</p>
		{/if}
	</div>
	{#if hasMore}
		<button
			type="button"
			onclick={() => loadPage(currentPage + 1)}
			disabled={loading}
			class="mt-4 text-sm text-[var(--app-accent)] disabled:opacity-50">{m['issues.archive.load_more']()}</button
		>
	{/if}
</div>
