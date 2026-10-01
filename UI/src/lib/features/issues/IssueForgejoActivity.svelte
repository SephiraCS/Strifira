<script lang="ts">
	import { onMount, untrack } from 'svelte';
	import { getCachedForgejoStatus, getForgejoStatus, getIssueForgejoActivity } from '$lib/api/forgejo';
	import type { ForgejoIssueActivity, ForgejoStatus } from '$lib/types/forgejo';
	import { formatRelativeTime } from '$lib/utils/format';
	import { m } from '$lib/paraglide/messages.js';
	import { getLocale } from '$lib/paraglide/runtime.js';
	import GitBranch from '@lucide/svelte/icons/git-branch';
	import GitPullRequest from '@lucide/svelte/icons/git-pull-request';
	import GitCommitHorizontal from '@lucide/svelte/icons/git-commit-horizontal';
	import ExternalLink from '@lucide/svelte/icons/external-link';
	import Copy from '@lucide/svelte/icons/copy';
	import Check from '@lucide/svelte/icons/check';
	import ChevronRight from '@lucide/svelte/icons/chevron-right';
	import CheckCircle2 from '@lucide/svelte/icons/circle-check';
	import RefreshCw from '@lucide/svelte/icons/refresh-cw';
	import { appToast } from '$lib/features/toast/toast';

	let { slug, identifier, issueId }: { slug: string; identifier: string; issueId: string } = $props();

	let activity = $state<ForgejoIssueActivity | null>(null);
	let loading = $state(true);
	let expanded = $state(true);
	let copiedBranch = $state<string | null>(null);
	let mounted = $state(false);
	let connection = $state<ForgejoStatus | null>(null);
	let failed = $state(false);
	let generation = 0;
	let refreshContext = '';
	let refreshing = $state(false);
	let refreshQueued = false;
	let freshStatusQueued = false;
	let refreshTimer: ReturnType<typeof setTimeout> | undefined;
	let copiedTimer: ReturnType<typeof setTimeout> | undefined;
	let controller: AbortController | undefined;

	const hasActivity = $derived(
		activity && (activity.pull_requests.length > 0 || activity.branches.length > 0 || activity.commits.length > 0)
	);

	$effect(() => {
		const workspaceSlug = slug;
		const issueIdentifier = identifier;
		const currentIssueId = issueId;
		if (!mounted) return;
		untrack(() => {
			const context = `${workspaceSlug}/${currentIssueId}/${issueIdentifier}`;
			if (context === refreshContext) return;
			refreshContext = context;
			generation++;
			controller?.abort();
			clearTimeout(refreshTimer);
			clearTimeout(copiedTimer);
			connection = null;
			activity = null;
			copiedBranch = null;
			loading = true;
			failed = false;
			refreshing = false;
			refreshQueued = false;
			freshStatusQueued = false;
			void refresh(workspaceSlug, issueIdentifier, generation);
		});
	});

	async function refresh(
		workspaceSlug: string,
		issueIdentifier: string,
		requestGeneration: number,
		freshStatus = false
	) {
		refreshing = true;
		failed = false;
		const requestController = new AbortController();
		controller = requestController;
		try {
			const status = await (freshStatus ? getForgejoStatus(workspaceSlug) : getCachedForgejoStatus(workspaceSlug));
			if (!mounted || requestGeneration !== generation) return;
			if (!status.connected || connection?.instance_url !== status.instance_url) activity = null;
			connection = status;
			if (!status.connected) return;
			const result = await getIssueForgejoActivity(
				workspaceSlug,
				issueIdentifier,
				status.instance_url ?? '',
				requestController.signal
			);
			if (mounted && requestGeneration === generation) activity = result;
		} catch {
			if (mounted && requestGeneration === generation) failed = true;
		} finally {
			if (requestGeneration === generation) {
				loading = false;
				refreshing = false;
				if (refreshQueued) {
					refreshQueued = false;
					scheduleRefresh(freshStatusQueued);
				}
			}
		}
	}

	function manualRefresh() {
		if (refreshing) return;
		void refresh(slug, identifier, generation, true);
	}

	function scheduleRefresh(freshStatus = false) {
		freshStatusQueued ||= freshStatus;
		clearTimeout(refreshTimer);
		refreshTimer = setTimeout(() => {
			if (refreshing) refreshQueued = true;
			else {
				const freshStatus = freshStatusQueued;
				freshStatusQueued = false;
				void refresh(slug, identifier, generation, freshStatus);
			}
		}, 180);
	}

	onMount(() => {
		mounted = true;
		function handleForgejoUpdate(event: Event) {
			const detail = (event as CustomEvent<{ issue_id?: string; identifier?: string; id?: string }>).detail;
			if (!detail || (detail.issue_id !== issueId && detail.id !== issueId && detail.identifier !== identifier)) return;
			scheduleRefresh();
		}
		function handleReconnected(event: Event) {
			const detail = (event as CustomEvent<{ workspaceSlug?: string }>).detail;
			if (detail?.workspaceSlug === slug) scheduleRefresh(true);
		}
		window.addEventListener('ws:forgejo:pr_updated', handleForgejoUpdate);
		window.addEventListener('ws:forgejo:branch_created', handleForgejoUpdate);
		window.addEventListener('ws:forgejo:commit_pushed', handleForgejoUpdate);
		window.addEventListener('ws:issue-updated', handleForgejoUpdate);
		window.addEventListener('ws:reconnected', handleReconnected);
		return () => {
			mounted = false;
			generation++;
			controller?.abort();
			clearTimeout(refreshTimer);
			clearTimeout(copiedTimer);
			window.removeEventListener('ws:forgejo:pr_updated', handleForgejoUpdate);
			window.removeEventListener('ws:forgejo:branch_created', handleForgejoUpdate);
			window.removeEventListener('ws:forgejo:commit_pushed', handleForgejoUpdate);
			window.removeEventListener('ws:issue-updated', handleForgejoUpdate);
			window.removeEventListener('ws:reconnected', handleReconnected);
		};
	});

	async function copyBranch(name: string) {
		const requestGeneration = generation;
		try {
			await navigator.clipboard.writeText(name);
			if (requestGeneration !== generation) return;
			copiedBranch = name;
			appToast.success(m['issue.forgejo.branch_copied']());
			clearTimeout(copiedTimer);
			copiedTimer = setTimeout(() => {
				copiedBranch = null;
			}, 2000);
		} catch {
			appToast.error(m['issue.forgejo.copy_failed']());
		}
	}

	function prStateClass(state: string) {
		switch (state) {
			case 'merged':
				return 'bg-purple-500/10 text-purple-400 border-purple-500/20';
			case 'open':
				return 'bg-green-500/10 text-green-400 border-green-500/20';
			case 'draft':
				return 'bg-[var(--color-bg-tertiary)] text-[var(--color-text-tertiary)] border-[var(--app-border)]';
			case 'closed':
				return 'bg-red-500/10 text-red-400 border-red-500/20';
			default:
				return 'bg-[var(--color-bg-tertiary)] text-[var(--color-text-tertiary)] border-[var(--app-border)]';
		}
	}

	let referenceCount = $derived(
		activity ? activity.pull_requests.length + activity.branches.length + activity.commits.length : 0
	);
</script>

{#if !loading && (hasActivity || connection?.connected || failed)}
	<div class="rounded-lg border border-[var(--app-border)] bg-[var(--color-bg-secondary)]">
		<div class="flex items-center">
			<button
				onclick={() => (expanded = !expanded)}
				class="flex min-w-0 flex-1 items-center gap-1.5 px-3 py-2.5 text-[11px] font-medium uppercase tracking-wider text-[var(--color-text-tertiary)] hover:text-[var(--color-text-secondary)] transition-colors"
			>
				<ChevronRight size={12} class="transition-transform {expanded ? 'rotate-90' : ''}" />
				<GitBranch size={12} />
				{m['issue.forgejo.activity']()}
				<span
					class="ml-auto rounded-full bg-[var(--color-bg-tertiary)] px-2 py-0.5 text-xs normal-case tracking-normal"
				>
					{referenceCount}
				</span>
			</button>
			<button
				onclick={manualRefresh}
				disabled={refreshing}
				aria-label={m['issue.forgejo.refresh']()}
				title={m['issue.forgejo.refresh']()}
				class="mr-2 rounded p-1 text-[var(--color-text-tertiary)] hover:bg-[var(--color-bg-hover)] disabled:opacity-50"
				><RefreshCw size={12} class={refreshing ? 'animate-spin' : ''} /></button
			>
		</div>
		{#if expanded && failed}<p
				role="alert"
				class="border-t border-[var(--app-border)] px-3 py-2.5 text-xs text-red-500"
			>
				{m['issue.forgejo.failed_load']()}
			</p>{/if}
		{#if expanded && !failed && !hasActivity}<p
				class="border-t border-[var(--app-border)] px-3 py-2.5 text-xs leading-5 text-[var(--color-text-tertiary)]"
			>
				{m['issue.forgejo.empty']({ identifier })}
			</p>{/if}

		{#if expanded && activity}
			<div class="space-y-2 border-t border-[var(--app-border)] px-3 py-2.5">
				<!-- Pull requests -->
				{#each activity.pull_requests as pr}
					<a
						href={pr.html_url || undefined}
						target="_blank"
						rel="noopener noreferrer"
						class="group block rounded-md p-1.5 -mx-1.5 text-xs transition-colors hover:bg-[var(--color-bg-hover)]"
					>
						<div class="flex items-center gap-1.5">
							<GitPullRequest
								size={13}
								class="shrink-0 {pr.state === 'merged'
									? 'text-purple-500'
									: pr.state === 'open'
										? 'text-green-500'
										: 'text-[var(--color-text-tertiary)]'}"
							/>
							<span class="min-w-0 flex-1 truncate font-medium text-[var(--color-text-primary)]">{pr.title}</span>
							{#if pr.checks_green}
								<span
									class="flex shrink-0 items-center gap-0.5 rounded-full border border-green-500/20 bg-green-500/10 px-1.5 py-0 text-[9px] leading-4 text-green-400"
									title={m['issue.forgejo.checks_green']()}
								>
									<CheckCircle2 size={9} />
									CI
								</span>
							{/if}
							<span class="shrink-0 rounded-full border px-1.5 py-0 text-[9px] leading-4 {prStateClass(pr.state)}"
								>{pr.state}</span
							>
						</div>
						<div
							class="mt-1 flex flex-wrap items-center gap-x-2 gap-y-0.5 pl-[21px] text-[10px] text-[var(--color-text-tertiary)]"
						>
							<span>{pr.repo_full_name}#{pr.number}</span>
							{#if pr.additions || pr.deletions}
								<span class="text-green-600">+{pr.additions}</span>
								<span class="text-red-500">-{pr.deletions}</span>
							{/if}
							<span>{formatRelativeTime(pr.updated_at, getLocale())}</span>
						</div>
					</a>
				{/each}

				<!-- Branches -->
				{#each activity.branches as branch}
					<div class="flex items-center gap-2 rounded-md p-1.5 -mx-1.5 text-xs hover:bg-[var(--color-bg-hover)]">
						<GitBranch size={13} class="shrink-0 text-[var(--color-text-tertiary)]" />
						<div class="min-w-0 flex-1">
							<code class="block truncate text-[var(--color-text-primary)]">{branch.name}</code>
							<div class="mt-0.5 truncate text-[10px] text-[var(--color-text-tertiary)]">{branch.repo_full_name}</div>
						</div>
						<button
							onclick={() => copyBranch(branch.name)}
							class="shrink-0 rounded p-1 text-[var(--color-text-tertiary)] hover:bg-[var(--color-bg-tertiary)] hover:text-[var(--color-text-secondary)]"
							title={m['issue.forgejo.copy_branch']()}
						>
							{#if copiedBranch === branch.name}
								<Check size={12} class="text-green-500" />
							{:else}
								<Copy size={12} />
							{/if}
						</button>
						{#if branch.html_url}
							<a
								href={branch.html_url}
								target="_blank"
								rel="noopener noreferrer"
								class="shrink-0 rounded p-1 text-[var(--color-text-tertiary)] hover:bg-[var(--color-bg-tertiary)] hover:text-[var(--color-text-secondary)]"
							>
								<ExternalLink size={12} />
							</a>
						{/if}
					</div>
				{/each}

				<!-- Commits (latest 5) -->
				{#each activity.commits.slice(0, 5) as commit}
					<a
						href={commit.html_url || undefined}
						target="_blank"
						rel="noopener noreferrer"
						class="group flex items-start gap-2 rounded-md p-1.5 -mx-1.5 text-xs transition-colors hover:bg-[var(--color-bg-hover)]"
					>
						<GitCommitHorizontal size={13} class="mt-0.5 shrink-0 text-[var(--color-text-tertiary)]" />
						<div class="min-w-0 flex-1">
							<div class="truncate text-[var(--color-text-primary)]">{commit.message.split('\n')[0]}</div>
							<div
								class="mt-0.5 flex flex-wrap items-center gap-x-2 gap-y-0.5 text-[10px] text-[var(--color-text-tertiary)]"
							>
								<code>{commit.short_sha}</code>
								<span>{commit.repo_full_name}</span>
								{#if commit.author_login}
									<span>{commit.author_login}</span>
								{/if}
								<span>{formatRelativeTime(commit.committed_at, getLocale())}</span>
							</div>
						</div>
					</a>
				{/each}
				{#if activity.commits.length > 5}
					<p class="px-1.5 text-[10px] text-[var(--color-text-tertiary)]">
						+{activity.commits.length - 5}
						{m['issue.forgejo.more_commits']()}
					</p>
				{/if}
			</div>
		{/if}
	</div>
{/if}
