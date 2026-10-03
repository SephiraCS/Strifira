<script lang="ts">
	import { onMount, untrack } from 'svelte';
	import {
		cancelForgejoActionRun,
		getCachedForgejoStatus,
		getForgejoActionLogs,
		getForgejoStatus,
		getIssueForgejoActivity,
		listForgejoActionJobs,
		listForgejoActionRuns
	} from '$lib/api/forgejo';
	import type {
		ForgejoActionJob,
		ForgejoActionLogs,
		ForgejoActionRun,
		ForgejoIssueActivity,
		ForgejoPullRequest,
		ForgejoStatus
	} from '$lib/types/forgejo';
	import ActionStatus from '$lib/features/forgejo/ActionStatus.svelte';
	import { actionDuration, actionStatus, isActiveAction, plainActionLogs } from '$lib/features/forgejo/actions';
	import { safeForgejoActionsUrl } from '$lib/security/forgejo-url';
	import { sidebarState } from '$lib/features/layout/sidebar.state.svelte';
	import * as AlertDialog from '$lib/components/ui/alert-dialog';
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
	import Workflow from '@lucide/svelte/icons/workflow';
	import Square from '@lucide/svelte/icons/square';
	import LoaderCircle from '@lucide/svelte/icons/loader-circle';
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

	// Forgejo Actions runs for each linked pull request, keyed by pull request id.
	// `pages` counts the newest pages loaded; older history loads on request.
	type PullRequestRuns = {
		runs: ForgejoActionRun[];
		pages: number;
		hasMore: boolean;
		loading: boolean;
		loadingMore: boolean;
		failed: boolean;
		// Bumped when older history loads, so a refresh started earlier is dropped.
		version: number;
	};
	type ExpandedRun = { pullRequestId: string; repoId: string; repoFullName: string; run: ForgejoActionRun };
	const ACTIONS_POLL_MS = 10000;
	// Open PRs keep a slower poll so newly queued runs appear without a webhook.
	const ACTIONS_DISCOVERY_MS = 30000;
	const RUNS_PAGE_SIZE = 20;
	let runsByPullRequest = $state<Record<string, PullRequestRuns>>({});
	let expandedRun = $state<ExpandedRun | null>(null);
	let jobs = $state<ForgejoActionJob[]>([]);
	let jobsLoading = $state(false);
	let jobsFailed = $state(false);
	let selectedJob = $state<ForgejoActionJob | null>(null);
	let logs = $state<ForgejoActionLogs | null>(null);
	let logsLoading = $state(false);
	let logsFailed = $state(false);
	let cancelTarget = $state<ExpandedRun | null>(null);
	let cancelBusy = $state(false);
	let cancelRequested = $state<number | null>(null);
	let actionsGeneration = 0;
	let runsController: AbortController | undefined;
	let jobsController: AbortController | undefined;
	let logsController: AbortController | undefined;
	let pollTimer: ReturnType<typeof setTimeout> | undefined;
	// Set when a background detail refresh fails, so the next poll retries it.
	let detailsStale = false;

	const actionPullRequests = $derived(activity?.pull_requests.filter((pr) => pr.repo_id) ?? []);
	const canManageActions = $derived(sidebarState.workspaceRole === 'owner');
	const anyActiveRun = $derived(
		Object.values(runsByPullRequest).some((entry) =>
			entry.runs.some((run) => isActiveAction(actionStatus(run.status, run.conclusion)))
		)
	);
	const logContent = $derived(logs ? plainActionLogs(logs.content) : '');

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
			resetActions();
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
			if (!status.connected || connection?.instance_url !== status.instance_url) {
				activity = null;
				resetActions();
			}
			connection = status;
			if (!status.connected) return;
			const result = await getIssueForgejoActivity(
				workspaceSlug,
				issueIdentifier,
				status.instance_url ?? '',
				requestController.signal
			);
			if (mounted && requestGeneration === generation) {
				activity = result;
				void loadRuns(workspaceSlug, result.pull_requests);
			}
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
			resetActions();
			window.removeEventListener('ws:forgejo:pr_updated', handleForgejoUpdate);
			window.removeEventListener('ws:forgejo:branch_created', handleForgejoUpdate);
			window.removeEventListener('ws:forgejo:commit_pushed', handleForgejoUpdate);
			window.removeEventListener('ws:issue-updated', handleForgejoUpdate);
			window.removeEventListener('ws:reconnected', handleReconnected);
		};
	});

	function resetActions() {
		actionsGeneration++;
		runsController?.abort();
		clearTimeout(pollTimer);
		runsByPullRequest = {};
		closeRun();
		cancelTarget = null;
		cancelBusy = false;
		cancelRequested = null;
	}

	function closeRun() {
		detailsStale = false;
		jobsController?.abort();
		logsController?.abort();
		expandedRun = null;
		jobs = [];
		jobsLoading = false;
		jobsFailed = false;
		selectedJob = null;
		logs = null;
		logsLoading = false;
		logsFailed = false;
	}

	// A PR's runs come from pull_request workflows (refs/pull/N/head) and from
	// push workflows on its source branch; one page holds both, newest first.
	async function fetchRunPage(workspaceSlug: string, pr: ForgejoPullRequest, page: number, signal: AbortSignal) {
		const refs = [`refs/pull/${pr.number}/head`];
		if (pr.head_branch && !pr.head_branch.startsWith('refs/')) refs.push(`refs/heads/${pr.head_branch}`);
		const results = await Promise.all(
			refs.map((ref) =>
				listForgejoActionRuns(workspaceSlug, pr.repo_id, { ref, page, perPage: RUNS_PAGE_SIZE }, signal)
			)
		);
		const byId = new Map<number, ForgejoActionRun>();
		for (const result of results) for (const run of result.data ?? []) byId.set(run.id, run);
		return {
			data: [...byId.values()].sort((a, b) => b.id - a.id),
			has_more: results.some((result) => result.has_more && !!result.data?.length)
		};
	}

	async function loadRuns(workspaceSlug: string, pullRequests: ForgejoPullRequest[]) {
		const runGeneration = ++actionsGeneration;
		runsController?.abort();
		clearTimeout(pollTimer);
		const requestController = new AbortController();
		runsController = requestController;
		const targets = pullRequests.filter((pr) => pr.repo_id);
		const next: Record<string, PullRequestRuns> = {};
		for (const pr of targets)
			next[pr.id] = runsByPullRequest[pr.id]
				? { ...runsByPullRequest[pr.id], loading: false, loadingMore: false }
				: { runs: [], pages: 1, hasMore: false, loading: true, loadingMore: false, failed: false, version: 0 };
		runsByPullRequest = next;
		await Promise.all(
			targets.map(async (pr) => {
				const { pages, version } = next[pr.id];
				try {
					// Refresh only the pages already shown, so polling stays bounded.
					const runs: ForgejoActionRun[] = [];
					let hasMore = false;
					for (let page = 1; page <= pages; page++) {
						const result = await fetchRunPage(workspaceSlug, pr, page, requestController.signal);
						// Pages have no snapshot: a new run can shift a run onto the next page.
						const seen = new Set(runs.map((run) => run.id));
						runs.push(...(result.data ?? []).filter((run) => !seen.has(run.id)));
						hasMore = result.has_more && !!result.data?.length;
						if (!hasMore) break;
					}
					if (!mounted || runGeneration !== actionsGeneration) return;
					// Older history loaded meanwhile; the next poll refreshes every page.
					if (runsByPullRequest[pr.id]?.version !== version) return;
					runsByPullRequest[pr.id] = {
						runs,
						pages,
						hasMore,
						loading: false,
						loadingMore: runsByPullRequest[pr.id].loadingMore,
						failed: false,
						version
					};
					if (expandedRun?.pullRequestId === pr.id) {
						const run = runs.find((candidate) => candidate.id === expandedRun?.run.id);
						if (run) {
							if (cancelRequested === run.id && !isActiveAction(actionStatus(run.status, run.conclusion)))
								cancelRequested = null;
							// The selected run just finished: its details need one final refresh.
							if (runIsActive(expandedRun.run) && !runIsActive(run)) detailsStale = true;
							expandedRun = { ...expandedRun, run };
						} else closeRun();
					}
				} catch {
					if (!mounted || runGeneration !== actionsGeneration || requestController.signal.aborted) return;
					// Keep previously loaded runs visible when a background refresh fails.
					const previous = runsByPullRequest[pr.id];
					runsByPullRequest[pr.id] = {
						...previous,
						loading: false,
						loadingMore: false,
						failed: !previous?.runs.length
					};
				}
			})
		);
		if (!mounted || runGeneration !== actionsGeneration) return;
		if (expandedRun && !targets.some((pr) => pr.id === expandedRun?.pullRequestId)) closeRun();
		// Refresh open details only after the runs, so the tick that observes a
		// run finishing also shows its final jobs and logs before polling stops.
		// Finished details never change, so refresh them only while the run is
		// active, once more after it finishes, and after a failed refresh.
		const target = expandedRun;
		if (target && (detailsStale || runIsActive(target.run))) {
			detailsStale = false;
			await loadJobs(workspaceSlug, target, true);
			// Starting logs for a superseded selection would abort the current request.
			const job = selectedJob;
			if (job && mounted && runGeneration === actionsGeneration && sameRun(expandedRun, target))
				await loadLogs(workspaceSlug, target, job, true);
		}
		if (mounted && runGeneration === actionsGeneration) schedulePoll(workspaceSlug, runGeneration);
	}

	async function loadOlderRuns(pr: ForgejoPullRequest) {
		const entry = runsByPullRequest[pr.id];
		if (!entry || entry.loadingMore || !entry.hasMore) return;
		const runGeneration = actionsGeneration;
		const page = entry.pages + 1;
		runsByPullRequest[pr.id] = { ...entry, loadingMore: true };
		try {
			const result = await fetchRunPage(slug, pr, page, runsController?.signal ?? new AbortController().signal);
			if (!mounted || runGeneration !== actionsGeneration) return;
			const current = runsByPullRequest[pr.id];
			const known = new Set(current.runs.map((run) => run.id));
			runsByPullRequest[pr.id] = {
				...current,
				runs: [...current.runs, ...(result.data ?? []).filter((run) => !known.has(run.id))],
				pages: page,
				hasMore: result.has_more && !!result.data?.length,
				loadingMore: false,
				version: current.version + 1
			};
			// An older active run must keep the fast poll going.
			if (anyActiveRun) schedulePoll(slug, runGeneration);
		} catch {
			if (!mounted || runGeneration !== actionsGeneration) return;
			runsByPullRequest[pr.id] = { ...runsByPullRequest[pr.id], loadingMore: false };
			appToast.error(m['forgejo_actions.failed_load']());
		}
	}

	function runIsActive(run: ForgejoActionRun): boolean {
		return isActiveAction(actionStatus(run.status, run.conclusion));
	}

	function sameRun(a: ExpandedRun | null, b: ExpandedRun): boolean {
		return !!a && a.pullRequestId === b.pullRequestId && a.repoId === b.repoId && a.run.id === b.run.id;
	}

	function schedulePoll(workspaceSlug: string, runGeneration: number) {
		clearTimeout(pollTimer);
		const watching = actionPullRequests.some((pr) => pr.state === 'open' || pr.state === 'draft');
		if (!anyActiveRun && !watching && !detailsStale) return;
		pollTimer = setTimeout(
			() => {
				if (!mounted || runGeneration !== actionsGeneration || !activity) return;
				if (document.visibilityState === 'hidden') {
					schedulePoll(workspaceSlug, runGeneration);
					return;
				}
				void loadRuns(workspaceSlug, activity.pull_requests);
			},
			anyActiveRun ? ACTIONS_POLL_MS : ACTIONS_DISCOVERY_MS
		);
	}

	function toggleRun(pr: ForgejoPullRequest, run: ForgejoActionRun) {
		if (expandedRun?.pullRequestId === pr.id && expandedRun.run.id === run.id) {
			closeRun();
			return;
		}
		closeRun();
		expandedRun = { pullRequestId: pr.id, repoId: pr.repo_id, repoFullName: pr.repo_full_name, run };
		void loadJobs(slug, expandedRun);
	}

	async function loadJobs(workspaceSlug: string, target: ExpandedRun, background = false) {
		jobsController?.abort();
		const requestController = new AbortController();
		jobsController = requestController;
		if (!background) {
			jobsLoading = true;
			jobsFailed = false;
		}
		try {
			const loaded: ForgejoActionJob[] = [];
			for (let page = 1; ; page++) {
				const result = await listForgejoActionJobs(
					workspaceSlug,
					target.repoId,
					target.run.id,
					page,
					requestController.signal
				);
				loaded.push(...(result.data ?? []));
				if (!result.has_more || !result.data?.length) break;
			}
			if (!mounted || requestController.signal.aborted || !sameRun(expandedRun, target)) return;
			jobs = loaded;
			jobsFailed = false;
		} catch {
			if (requestController.signal.aborted) return;
			if (background) detailsStale = true;
			else jobsFailed = true;
		} finally {
			if (jobsController === requestController) jobsLoading = false;
		}
	}

	function toggleJob(job: ForgejoActionJob) {
		if (!expandedRun) return;
		if (selectedJob?.id === job.id) {
			logsController?.abort();
			selectedJob = null;
			logs = null;
			logsLoading = false;
			logsFailed = false;
			return;
		}
		selectedJob = job;
		logs = null;
		void loadLogs(slug, expandedRun, job);
	}

	async function loadLogs(workspaceSlug: string, target: ExpandedRun, job: ForgejoActionJob, background = false) {
		logsController?.abort();
		const requestController = new AbortController();
		logsController = requestController;
		if (!background) {
			logsLoading = true;
			logsFailed = false;
		}
		const current = jobs.find((candidate) => candidate.id === job.id) ?? job;
		try {
			const result = await getForgejoActionLogs(
				workspaceSlug,
				target.repoId,
				target.run.id,
				current.id,
				current.attempt,
				requestController.signal
			);
			if (!mounted || requestController.signal.aborted || selectedJob?.id !== job.id || !sameRun(expandedRun, target))
				return;
			logs = result;
			logsFailed = false;
		} catch {
			if (requestController.signal.aborted) return;
			if (background) detailsStale = true;
			else logsFailed = true;
		} finally {
			if (logsController === requestController) logsLoading = false;
		}
	}

	async function cancelRun() {
		const target = cancelTarget;
		if (!target || !canManageActions || cancelBusy) return;
		const workspaceSlug = slug;
		// generation changes with the issue or workspace; a late response must
		// not touch the state of the issue now shown.
		const requestGeneration = generation;
		cancelBusy = true;
		try {
			await cancelForgejoActionRun(workspaceSlug, target.repoId, target.run.id);
			appToast.success(m['forgejo_actions.requested']());
			if (requestGeneration !== generation) return;
			cancelRequested = target.run.id;
			if (activity) void loadRuns(workspaceSlug, activity.pull_requests);
		} catch (error) {
			appToast.apiError(error, m['forgejo_actions.failed_action']());
		} finally {
			if (requestGeneration === generation) {
				cancelBusy = false;
				cancelTarget = null;
			}
		}
	}

	function runUrl(run: ForgejoActionRun, repoFullName: string): string | null {
		return safeForgejoActionsUrl(run.html_url, repoFullName, connection?.instance_url ?? '');
	}

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
			{#if actionPullRequests.length > 0}
				<section
					aria-label={m['forgejo_actions.title']()}
					class="space-y-2 border-t border-[var(--app-border)] px-3 py-2.5"
				>
					<div
						class="flex items-center gap-1.5 text-[11px] font-medium uppercase tracking-wider text-[var(--color-text-tertiary)]"
					>
						<Workflow size={12} />
						{m['issue.forgejo.actions']()}
						{#if anyActiveRun}<span
								class="ml-auto h-1.5 w-1.5 rounded-full bg-blue-500"
								title={m['forgejo_actions.active']()}
							></span>{/if}
					</div>
					{#each actionPullRequests as pr (pr.id)}
						{@const entry = runsByPullRequest[pr.id]}
						<div class="space-y-1">
							{#if actionPullRequests.length > 1}<div class="text-[10px] text-[var(--color-text-tertiary)]">
									{pr.repo_full_name}#{pr.number}
								</div>{/if}
							{#if !entry || entry.loading}
								<div class="flex items-center gap-1.5 text-[10px] text-[var(--color-text-tertiary)]">
									<LoaderCircle size={11} class="animate-spin" />{m['forgejo_actions.loading']()}
								</div>
							{:else if entry.failed}
								<p role="alert" class="text-[10px] text-red-500">{m['forgejo_actions.failed_load']()}</p>
							{:else if entry.runs.length === 0}
								<p class="text-[10px] text-[var(--color-text-tertiary)]">{m['issue.forgejo.no_runs']()}</p>
							{:else}
								{#each entry.runs as run (run.id)}
									{@const open = expandedRun?.pullRequestId === pr.id && expandedRun.run.id === run.id}
									{@const duration = actionDuration(run.started_at, run.completed_at)}
									{@const url = runUrl(run, pr.repo_full_name)}
									{@const active = isActiveAction(actionStatus(run.status, run.conclusion))}
									<div class="-mx-1.5 rounded-md {open ? 'bg-[var(--color-bg-hover)]' : ''}">
										<div class="flex items-center gap-1 rounded-md text-xs hover:bg-[var(--color-bg-hover)]">
											<button
												onclick={() => toggleRun(pr, run)}
												aria-expanded={open}
												class="flex min-w-0 flex-1 items-center gap-1.5 p-1.5 text-left"
											>
												<ChevronRight
													size={11}
													class="shrink-0 text-[var(--color-text-tertiary)] transition-transform {open
														? 'rotate-90'
														: ''}"
												/>
												<ActionStatus status={run.status} conclusion={run.conclusion} compact />
												<span class="min-w-0 flex-1 truncate text-[var(--color-text-primary)]"
													>{run.title ||
														run.name ||
														m['forgejo_actions.run_number']({ n: run.run_number || run.id })}</span
												>
												<span class="shrink-0 text-[10px] text-[var(--color-text-tertiary)]"
													>{run.name || run.workflow_id} · #{run.run_number || run.id}{duration
														? ` · ${duration}`
														: ''}</span
												>
											</button>
											{#if url}<a
													href={url}
													target="_blank"
													rel="noopener noreferrer"
													aria-label={m['forgejo_actions.open_forgejo']()}
													title={m['forgejo_actions.open_forgejo']()}
													class="mr-1 shrink-0 rounded p-1 text-[var(--color-text-tertiary)] hover:bg-[var(--color-bg-tertiary)] hover:text-[var(--color-text-secondary)]"
													><ExternalLink size={12} /></a
												>{/if}
										</div>
										{#if open && expandedRun}
											<div class="space-y-1.5 px-1.5 pb-2 pl-[22px]">
												{#if canManageActions && active}
													<div class="flex items-center gap-2">
														<button
															onclick={() => (cancelTarget = expandedRun)}
															disabled={cancelBusy || cancelRequested === run.id}
															class="inline-flex items-center gap-1 rounded border border-[var(--app-border)] px-1.5 py-0.5 text-[10px] text-[var(--color-text-secondary)] hover:bg-[var(--color-bg-tertiary)] disabled:opacity-50"
															><Square size={9} />{m['forgejo_actions.cancel_run']()}</button
														>
														{#if cancelRequested === run.id}<span
																role="status"
																class="text-[10px] text-[var(--color-text-tertiary)]"
																>{m['forgejo_actions.requested']()}</span
															>{/if}
													</div>
												{/if}
												{#if jobsLoading}
													<LoaderCircle
														size={11}
														class="animate-spin text-[var(--color-text-tertiary)]"
														aria-label={m['forgejo_actions.loading']()}
													/>
												{:else if jobsFailed}
													<p role="alert" class="text-[10px] text-red-500">{m['forgejo_actions.failed_details']()}</p>
												{:else}
													{#if jobs.length === 0}
														<p class="text-[10px] text-[var(--color-text-tertiary)]">
															{m['forgejo_actions.no_jobs']()}
														</p>
													{:else}
														<ul aria-label={m['forgejo_actions.jobs']()} class="space-y-0.5">
															{#each jobs as job (job.id)}
																<li>
																	<button
																		onclick={() => toggleJob(job)}
																		aria-pressed={selectedJob?.id === job.id}
																		class="flex w-full items-center gap-1.5 rounded px-1 py-0.5 text-left text-[11px] hover:bg-[var(--color-bg-tertiary)] {selectedJob?.id ===
																		job.id
																			? 'bg-[var(--color-bg-tertiary)]'
																			: ''}"
																	>
																		<ActionStatus status={job.status} conclusion={job.conclusion} compact />
																		<span class="min-w-0 flex-1 truncate">{job.name}</span>
																		{#if job.attempt > 1}<span
																				class="shrink-0 text-[10px] text-[var(--color-text-tertiary)]"
																				>{m['forgejo_actions.attempt']({ n: job.attempt })}</span
																			>{/if}
																	</button>
																</li>
															{/each}
														</ul>
													{/if}
												{/if}
												{#if selectedJob}
													{#if logsFailed}
														<p role="alert" class="text-[10px] text-red-500">{m['forgejo_actions.failed_logs']()}</p>
													{:else if logs}
														{#if logs.truncated}<p class="text-[10px] text-amber-500">
																{m['forgejo_actions.truncated_logs']({ n: Math.round(logs.max_bytes / 1048576) })}
															</p>{/if}
														<!-- svelte-ignore a11y_no_noninteractive_tabindex (Scrollable logs need keyboard focus.) -->
														<pre
															role="region"
															tabindex="0"
															aria-label={m['forgejo_actions.logs']()}
															class="max-h-64 overflow-auto rounded border border-[var(--app-border)] bg-[var(--color-bg-primary)] p-2 font-mono text-[10px] leading-4 text-[var(--color-text-secondary)]">{logContent ||
																m['forgejo_actions.no_logs']()}</pre>
													{:else if logsLoading}
														<LoaderCircle
															size={11}
															class="animate-spin text-[var(--color-text-tertiary)]"
															aria-label={m['forgejo_actions.loading']()}
														/>
													{/if}
												{/if}
											</div>
										{/if}
									</div>
								{/each}
								{#if entry.hasMore}
									<button
										onclick={() => loadOlderRuns(pr)}
										disabled={entry.loadingMore}
										class="inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-[10px] text-[var(--color-text-secondary)] hover:bg-[var(--color-bg-tertiary)] disabled:opacity-50"
										>{#if entry.loadingMore}<LoaderCircle size={10} class="animate-spin" />{/if}{m[
											'issue.forgejo.older_runs'
										]()}</button
									>
								{/if}
							{/if}
						</div>
					{/each}
				</section>
			{/if}
		{/if}
	</div>
{/if}

<AlertDialog.Root
	open={cancelTarget !== null}
	onOpenChange={(open) => {
		if (!open && !cancelBusy) cancelTarget = null;
	}}
	><AlertDialog.Content
		><AlertDialog.Header
			><AlertDialog.Title>{m['forgejo_actions.confirm_cancel_title']()}</AlertDialog.Title><AlertDialog.Description
				>{m['forgejo_actions.confirm_cancel_desc']()}</AlertDialog.Description
			></AlertDialog.Header
		><AlertDialog.Footer
			><AlertDialog.Cancel disabled={cancelBusy}>{m['common.cancel']()}</AlertDialog.Cancel><AlertDialog.Action
				variant="destructive"
				onclick={cancelRun}
				disabled={cancelBusy}>{m['forgejo_actions.cancel_run']()}</AlertDialog.Action
			></AlertDialog.Footer
		></AlertDialog.Content
	></AlertDialog.Root
>
