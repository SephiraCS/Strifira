<script lang="ts">
	import { onMount } from 'svelte';
	import { linkDocIssue, listDocIssues, unlinkDocIssue } from '$lib/api/docs';
	import { listIssues } from '$lib/api/issues';
	import type { DocIssueLink } from '$lib/types/docs';
	import type { Issue, IssuePriority } from '$lib/types/issue';
	import IssueStatusIcon from '$lib/features/issues/IssueStatusIcon.svelte';
	import IssuePriorityIcon from '$lib/features/issues/IssuePriorityIcon.svelte';
	import { appToast } from '$lib/features/toast/toast';
	import { m } from '$lib/paraglide/messages.js';
	import Link2 from '@lucide/svelte/icons/link-2';
	import X from '@lucide/svelte/icons/x';
	import Search from '@lucide/svelte/icons/search';

	let { slug, docId, readonly = false }: { slug: string; docId: string; readonly?: boolean } =
		$props();

	let linked = $state<DocIssueLink[]>([]);
	let loading = $state(true);
	let query = $state('');
	let suggestions = $state<Issue[]>([]);
	let showSuggestions = $state(false);
	let submitting = $state(false);
	let loadedFor = '';

	let suggestionTimer: ReturnType<typeof setTimeout> | null = null;

	const suggestionItems = $derived.by(() => {
		const q = query.trim().toLowerCase();
		const linkedIds = new Set(linked.map((l) => l.id));
		return suggestions.filter((s) => !linkedIds.has(s.id)).slice(0, 6);
	});

	async function load() {
		if (loadedFor === docId) return;
		loadedFor = docId;
		loading = true;
		try {
			linked = await listDocIssues(slug, docId);
		} catch {
			linked = [];
		} finally {
			loading = false;
		}
	}

	async function refresh() {
		try {
			linked = await listDocIssues(slug, docId);
		} catch {
			// Keep the current list; the toast reports write failures.
		}
	}

	function fetchSuggestions(value: string) {
		if (suggestionTimer) clearTimeout(suggestionTimer);
		const q = value.trim();
		if (!q) {
			suggestions = [];
			showSuggestions = false;
			return;
		}
		suggestionTimer = setTimeout(async () => {
			try {
				const res = await listIssues(slug, { search: q, per_page: '8' });
				suggestions = res.data ?? [];
				showSuggestions = suggestions.length > 0;
			} catch {
				suggestions = [];
				showSuggestions = false;
			}
		}, 250);
	}

	function onInput(value: string) {
		query = value;
		fetchSuggestions(value);
	}

	async function linkIssue(identifier: string) {
		if (submitting || !identifier.trim()) return;
		submitting = true;
		showSuggestions = false;
		try {
			linked = await linkDocIssue(slug, docId, identifier.trim());
			query = '';
		} catch (err) {
			appToast.apiError(err, m['docs.link_issues.failed']());
		} finally {
			submitting = false;
		}
	}

	async function unlinkIssue(identifier: string) {
		if (submitting) return;
		submitting = true;
		try {
			linked = await unlinkDocIssue(slug, docId, identifier);
		} catch (err) {
			appToast.apiError(err, m['docs.link_issues.failed']());
		} finally {
			submitting = false;
		}
	}

	function pickSuggestion(issue: Issue) {
		void linkIssue(issue.identifier);
	}

	onMount(() => {
		void load();
	});
</script>

{#snippet linkedRow(item: DocIssueLink)}
	<div class="group flex items-center gap-2 rounded-md px-2 py-1.5 hover:bg-[var(--color-bg-hover)]">
		<IssueStatusIcon status={item.status as Issue['status']} size={14} />
		<a
			href="/{slug}/issue/{item.identifier}"
			class="shrink-0 text-xs font-medium tabular-nums text-[var(--color-text-secondary)] hover:text-[var(--color-text-primary)]"
		>
			{item.identifier}
		</a>
		<a
			href="/{slug}/issue/{item.identifier}"
			class="min-w-0 flex-1 truncate text-sm text-[var(--color-text-secondary)] hover:text-[var(--color-text-primary)]"
		>
			{item.title}
		</a>
		<IssuePriorityIcon priority={item.priority as IssuePriority} size={13} />
		{#if !readonly}
			<button
				type="button"
				class="shrink-0 rounded p-0.5 text-[var(--color-text-tertiary)] opacity-0 transition-opacity group-hover:opacity-100 hover:bg-[var(--color-bg-tertiary)] hover:text-[var(--color-error)]"
				onclick={() => void unlinkIssue(item.identifier)}
				title={m['docs.link_issues.remove']()}
				aria-label={m['docs.link_issues.remove']()}
			>
				<X size={13} />
			</button>
		{/if}
	</div>
{/snippet}

<section class="mb-4 rounded-lg border border-[var(--app-border)] bg-[var(--color-bg-secondary)]/60">
	<div class="flex items-center gap-2 px-3 py-2">
		<Link2 size={13} class="text-[var(--color-text-tertiary)]" />
		<h3 class="text-[11px] font-semibold uppercase tracking-wider text-[var(--color-text-tertiary)]">
			{m['docs.link_issues.title']()}
		</h3>
		{#if linked.length > 0}
			<span class="rounded-full bg-[var(--color-bg-tertiary)] px-1.5 text-xs text-[var(--color-text-tertiary)]">
				{linked.length}
			</span>
		{/if}
	</div>
	<div class="border-t border-[var(--app-border)] px-2 py-1.5">
		{#if loading}
			<p class="px-2 py-2 text-xs text-[var(--color-text-tertiary)]">{m['docs.loading']()}</p>
		{:else if linked.length === 0}
			<p class="px-2 py-2 text-xs text-[var(--color-text-tertiary)]">{m['docs.link_issues.empty']()}</p>
		{:else}
			{#each linked as item (item.id)}
				{@render linkedRow(item)}
			{/each}
		{/if}

		{#if !readonly}
			<div class="relative mt-1 px-1 pb-1">
				<Search
					size={13}
					class="absolute left-4 top-1/2 -translate-y-1/2 text-[var(--color-text-tertiary)]"
				/>
				<input
					type="text"
					class="w-full rounded-md border border-[var(--app-border)] bg-[var(--color-bg-primary)] py-1.5 pl-8 pr-3 text-sm text-[var(--color-text-primary)] outline-none placeholder:text-[var(--color-text-tertiary)] focus:border-[var(--app-accent)]"
					placeholder={m['docs.link_issues.placeholder']()}
					value={query}
					{readonly}
					oninput={(e) => onInput(e.currentTarget.value)}
					onkeydown={(e) => {
						if (e.key === 'Enter') {
							e.preventDefault();
							void linkIssue(suggestionItems[0]?.identifier ?? query);
						} else if (e.key === 'Escape') {
							showSuggestions = false;
						}
					}}
				/>
				{#if showSuggestions && suggestionItems.length > 0}
					<div
						class="absolute bottom-full left-3 z-30 mb-1 w-80 overflow-y-auto rounded-md border border-[var(--app-border)] bg-[var(--color-bg-secondary)] p-1 shadow-xl"
						role="listbox"
					>
						{#each suggestionItems as item (item.id)}
							<button
								type="button"
								role="option"
								aria-selected="false"
								class="flex w-full items-center gap-2 rounded px-2 py-1.5 text-left text-sm text-[var(--color-text-secondary)] hover:bg-[var(--color-bg-hover)] hover:text-[var(--color-text-primary)]"
								onmousedown={(e) => {
									e.preventDefault();
									pickSuggestion(item);
								}}
							>
								<IssueStatusIcon status={item.status} size={13} />
								<span class="shrink-0 text-xs tabular-nums">{item.identifier}</span>
								<span class="min-w-0 flex-1 truncate">{item.title}</span>
							</button>
						{/each}
					</div>
				{/if}
			</div>
		{/if}
	</div>
</section>
