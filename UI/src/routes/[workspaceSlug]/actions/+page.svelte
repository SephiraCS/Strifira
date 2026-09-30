<script lang="ts">
	import { onMount, untrack } from 'svelte';
	import { page } from '$app/state';
	import Workflow from '@lucide/svelte/icons/workflow';
	import RefreshCw from '@lucide/svelte/icons/refresh-cw';
	import GitBranch from '@lucide/svelte/icons/git-branch';
	import GitCommitHorizontal from '@lucide/svelte/icons/git-commit-horizontal';
	import ExternalLink from '@lucide/svelte/icons/external-link';
	import Download from '@lucide/svelte/icons/download';
	import ChevronLeft from '@lucide/svelte/icons/chevron-left';
	import ChevronRight from '@lucide/svelte/icons/chevron-right';
	import LoaderCircle from '@lucide/svelte/icons/loader-circle';
	import Square from '@lucide/svelte/icons/square';
	import RotateCcw from '@lucide/svelte/icons/rotate-ccw';
	import { Button } from '$lib/components/ui/button';
	import { Input } from '$lib/components/ui/input';
	import * as AlertDialog from '$lib/components/ui/alert-dialog';
	import SidebarToggle from '$lib/components/layout/SidebarToggle.svelte';
	import EmptyState from '$lib/components/shared/EmptyState.svelte';
	import LoadingState from '$lib/components/shared/LoadingState.svelte';
	import ErrorState from '$lib/components/shared/ErrorState.svelte';
	import ActionStatus from '$lib/features/forgejo/ActionStatus.svelte';
	import { actionDuration, actionStatus, isActiveAction, plainActionLogs } from '$lib/features/forgejo/actions';
	import { getWorkspace } from '$lib/api/workspaces';
	import {
		getForgejoStatus,
		listForgejoActionRuns,
		getForgejoActionRun,
		listForgejoActionJobs,
		getForgejoActionJob,
		getForgejoActionLogs,
		requestForgejoAction
	} from '$lib/api/forgejo';
	import type { ForgejoStatus, ForgejoActionRun, ForgejoActionJob, ForgejoActionLogs } from '$lib/types/forgejo';
	import { safeForgejoActionsUrl } from '$lib/security/forgejo-url';
	import { formatRelativeTime } from '$lib/utils/format';
	import { appToast } from '$lib/features/toast/toast';
	import { m } from '$lib/paraglide/messages.js';
	import { getLocale } from '$lib/paraglide/runtime.js';

	const slug = $derived(page.params.workspaceSlug ?? '');
	let mounted = $state(false);
	let status = $state<ForgejoStatus | null>(null);
	let role = $state('');
	let initializing = $state(true);
	let initFailed = $state(false);
	let repoId = $state('');
	let statusFilter = $state('');
	let branchInput = $state('');
	let branchFilter = $state('');
	let runs = $state<ForgejoActionRun[]>([]);
	let runsPage = $state(1);
	let runsHasMore = $state(false);
	let runsLoading = $state(false);
	let runsFailed = $state(false);
	let selectedRunId = $state<number | null>(null);
	let selectedRun = $state<ForgejoActionRun | null>(null);
	let jobs = $state<ForgejoActionJob[]>([]);
	let jobsPage = $state(1);
	let jobsHasMore = $state(false);
	let detailsLoading = $state(false);
	let detailsFailed = $state(false);
	let selectedJob = $state<ForgejoActionJob | null>(null);
	let logs = $state<ForgejoActionLogs | null>(null);
	let jobLoading = $state(false);
	let jobFailed = $state(false);
	let logsFailed = $state(false);
	let refreshing = $state(false);
	let pendingAction = $state<'rerun' | 'cancel' | null>(null);
	let actionDialogOpen = $state(false);
	let actionBusy = $state(false);
	let actionRequested = $state<number | null>(null);
	let actionRequestedState = '';
	let visible = $state(true);

	let workspaceGeneration = 0;
	let runsGeneration = 0;
	let detailsGeneration = 0;
	let jobGeneration = 0;
	let runsController: AbortController | undefined;
	let detailsController: AbortController | undefined;
	let jobController: AbortController | undefined;

	const repos = $derived(status?.repos.filter((repo) => repo.is_active) ?? []);
	const repository = $derived(repos.find((repo) => repo.id === repoId));
	const canManage = $derived(role === 'owner');
	const runIsActive = $derived(
		selectedRun ? isActiveAction(actionStatus(selectedRun.status, selectedRun.conclusion)) : false
	);
	const canRerun = $derived(
		selectedRun &&
			['success', 'failure', 'cancelled', 'skipped'].includes(actionStatus(selectedRun.status, selectedRun.conclusion))
	);
	const activeRuns = $derived(
		runs.some((run) => isActiveAction(actionStatus(run.status, run.conclusion))) || runIsActive
	);
	const runUrl = $derived(
		selectedRun && repository
			? safeForgejoActionsUrl(
					selectedRun.html_url,
					repository.full_name,
					repository.instance_url || status?.instance_url || ''
				)
			: null
	);
	const logContent = $derived(logs ? plainActionLogs(logs.content) : '');

	$effect(() => {
		const workspaceSlug = slug;
		if (mounted && workspaceSlug) untrack(() => void initialize(workspaceSlug));
	});

	onMount(() => {
		mounted = true;
		visible = document.visibilityState !== 'hidden';
		function visibilityChanged() {
			visible = document.visibilityState !== 'hidden';
			if (visible && activeRuns) void refresh();
		}
		document.addEventListener('visibilitychange', visibilityChanged);
		const timer = setInterval(() => {
			if (visible && activeRuns && !runsLoading && !detailsLoading && !jobLoading) void refresh();
		}, 15000);
		return () => {
			mounted = false;
			workspaceGeneration++;
			abortRequests();
			clearInterval(timer);
			document.removeEventListener('visibilitychange', visibilityChanged);
		};
	});

	function abortRequests() {
		runsGeneration++;
		detailsGeneration++;
		jobGeneration++;
		runsController?.abort();
		detailsController?.abort();
		jobController?.abort();
	}

	function clearDetails() {
		detailsGeneration++;
		jobGeneration++;
		detailsController?.abort();
		jobController?.abort();
		selectedRunId = null;
		selectedRun = null;
		selectedJob = null;
		jobs = [];
		jobsPage = 1;
		jobsHasMore = false;
		logs = null;
		detailsLoading = false;
		detailsFailed = false;
		jobLoading = false;
		jobFailed = false;
		logsFailed = false;
		actionRequested = null;
		actionDialogOpen = false;
	}

	async function initialize(workspaceSlug: string) {
		const generation = ++workspaceGeneration;
		abortRequests();
		clearDetails();
		status = null;
		role = '';
		repoId = '';
		runs = [];
		statusFilter = '';
		branchInput = '';
		branchFilter = '';
		initializing = true;
		initFailed = false;
		try {
			const [connection, workspace] = await Promise.all([getForgejoStatus(workspaceSlug), getWorkspace(workspaceSlug)]);
			if (!mounted || generation !== workspaceGeneration) return;
			status = connection;
			role = workspace.current_user_role;
			const linked = connection.repos.filter((repo) => repo.is_active);
			const requested = page.url.searchParams.get('repo');
			repoId = linked.find((repo) => repo.id === requested)?.id ?? linked[0]?.id ?? '';
			if (connection.connected && repoId) await loadRuns(1);
		} catch {
			if (generation === workspaceGeneration) initFailed = true;
		} finally {
			if (generation === workspaceGeneration) initializing = false;
		}
	}

	function changeRepository(id: string) {
		abortRequests();
		clearDetails();
		repoId = id;
		runs = [];
		statusFilter = '';
		branchInput = '';
		branchFilter = '';
		void loadRuns(1);
	}

	function applyFilters() {
		clearDetails();
		branchFilter = branchInput.trim();
		void loadRuns(1);
	}

	async function loadRuns(pageNumber = runsPage, background = false) {
		if (!repoId) return;
		const generation = ++runsGeneration;
		const workspaceSlug = slug;
		const repositoryId = repoId;
		runsController?.abort();
		const controller = new AbortController();
		runsController = controller;
		if (!background) runsLoading = true;
		runsFailed = false;
		try {
			const result = await listForgejoActionRuns(
				workspaceSlug,
				repositoryId,
				{ page: pageNumber, status: statusFilter, ref: branchFilter },
				controller.signal
			);
			if (!mounted || generation !== runsGeneration) return;
			runs = result.data ?? [];
			runsPage = result.page;
			runsHasMore = result.has_more;
		} catch {
			if (generation === runsGeneration && !controller.signal.aborted) runsFailed = true;
		} finally {
			if (generation === runsGeneration) runsLoading = false;
		}
	}

	async function loadRun(runId: number, preserve = false) {
		if (!preserve) clearDetails();
		selectedRunId = runId;
		const generation = ++detailsGeneration;
		const workspaceSlug = slug;
		const repositoryId = repoId;
		detailsController?.abort();
		const controller = new AbortController();
		detailsController = controller;
		if (!preserve) detailsLoading = true;
		detailsFailed = false;
		try {
			const [run, result] = await Promise.all([
				getForgejoActionRun(workspaceSlug, repositoryId, runId, controller.signal),
				listForgejoActionJobs(workspaceSlug, repositoryId, runId, jobsPage, controller.signal)
			]);
			if (!mounted || generation !== detailsGeneration) return;
			if (actionRequested === run.id && `${run.status}:${run.updated_at}` !== actionRequestedState)
				actionRequested = null;
			selectedRun = run;
			jobs = result.data ?? [];
			jobsPage = result.page;
			jobsHasMore = result.has_more;
			if (selectedJob) await loadJob(jobs.find((job) => job.id === selectedJob?.id) ?? selectedJob, true);
		} catch {
			if (generation === detailsGeneration && !controller.signal.aborted) detailsFailed = true;
		} finally {
			if (generation === detailsGeneration) detailsLoading = false;
		}
	}

	async function loadJob(job: ForgejoActionJob, preserve = false) {
		const generation = ++jobGeneration;
		const workspaceSlug = slug;
		const repositoryId = repoId;
		jobController?.abort();
		const controller = new AbortController();
		jobController = controller;
		selectedJob = job;
		if (!preserve) logs = null;
		jobLoading = true;
		jobFailed = false;
		logsFailed = false;
		const [detail, output] = await Promise.allSettled([
			getForgejoActionJob(workspaceSlug, repositoryId, job.id, controller.signal),
			getForgejoActionLogs(workspaceSlug, repositoryId, job.id, job.attempt, controller.signal)
		]);
		if (!mounted || generation !== jobGeneration || controller.signal.aborted) return;
		if (detail.status === 'fulfilled') selectedJob = detail.value;
		else jobFailed = true;
		if (detail.status === 'fulfilled' && detail.value.attempt !== job.attempt) {
			logs = null;
			try {
				const currentOutput = await getForgejoActionLogs(
					workspaceSlug,
					repositoryId,
					job.id,
					detail.value.attempt,
					controller.signal
				);
				if (generation !== jobGeneration || controller.signal.aborted) return;
				logs = currentOutput;
			} catch {
				if (generation !== jobGeneration || controller.signal.aborted) return;
				logsFailed = true;
			}
		} else if (output.status === 'fulfilled') logs = output.value;
		else logsFailed = true;
		jobLoading = false;
	}

	async function refresh() {
		if (refreshing || initializing || !repoId) return;
		refreshing = true;
		const repositoryId = repoId;
		const workspaceSlug = slug;
		const runId = selectedRunId;
		try {
			await loadRuns(runsPage, true);
			if (mounted && repositoryId === repoId && workspaceSlug === slug && runId !== null && selectedRunId === runId)
				await loadRun(runId, true);
		} finally {
			refreshing = false;
		}
	}

	function jobsPagination(pageNumber: number) {
		jobGeneration++;
		jobController?.abort();
		selectedJob = null;
		logs = null;
		jobLoading = false;
		jobsPage = pageNumber;
		if (selectedRunId !== null) void loadRun(selectedRunId, true);
	}

	function confirmAction(action: 'rerun' | 'cancel') {
		pendingAction = action;
		actionDialogOpen = true;
	}

	async function performAction() {
		if (!selectedRun || !pendingAction || !canManage || actionBusy) return;
		const runId = selectedRun.id;
		const repositoryId = repoId;
		const workspaceSlug = slug;
		const action = pendingAction;
		actionBusy = true;
		try {
			await requestForgejoAction(workspaceSlug, repositoryId, runId, action);
			appToast.success(m['forgejo_actions.requested']());
			if (repositoryId === repoId && workspaceSlug === slug && selectedRunId === runId) {
				actionRequested = runId;
				actionRequestedState = `${selectedRun.status}:${selectedRun.updated_at}`;
				await refresh();
			}
		} catch (error) {
			appToast.apiError(error, m['forgejo_actions.failed_action']());
		} finally {
			actionBusy = false;
			actionDialogOpen = false;
		}
	}

	function downloadLogs() {
		if (!logs || !selectedJob) return;
		const url = URL.createObjectURL(new Blob([logContent], { type: 'text/plain;charset=utf-8' }));
		const link = document.createElement('a');
		link.href = url;
		link.download = `forgejo-run-${selectedRunId}-job-${selectedJob.id}-attempt-${selectedJob.attempt || 1}.log`;
		link.click();
		setTimeout(() => URL.revokeObjectURL(url), 1000);
	}
</script>

<div class="flex h-full min-h-0 flex-col">
	<header
		class="flex min-h-[49px] shrink-0 flex-wrap items-center justify-between gap-2 border-b border-[var(--app-border)] px-4 py-2 sm:px-6"
	>
		<div class="flex items-center gap-2">
			<SidebarToggle /><Workflow size={16} />
			<h1 class="text-sm font-medium">{m['forgejo_actions.title']()}</h1>
		</div>
		<div class="flex items-center gap-2">
			{#if visible && activeRuns}<span
					class="hidden items-center gap-1.5 text-xs text-[var(--color-text-tertiary)] sm:flex"
					><span class="h-1.5 w-1.5 rounded-full bg-blue-500"></span>{m['forgejo_actions.active']()}</span
				>{/if}
			<Button
				variant="outline"
				size="sm"
				onclick={refresh}
				disabled={initializing || refreshing || runsLoading || !repoId}
				><RefreshCw size={14} class={refreshing ? 'animate-spin' : ''} />{m['forgejo_actions.refresh']()}</Button
			>
			{#if canManage}<Button variant="ghost" size="sm" href="/{slug}/settings/forgejo"
					>{m['forgejo_actions.settings']()}</Button
				>{/if}
		</div>
	</header>
	<div class="min-h-0 flex-1 overflow-y-auto p-4 sm:p-6">
		{#if initializing}<LoadingState />
		{:else if initFailed}<ErrorState message={m['forgejo_actions.failed_load']()} onretry={() => initialize(slug)} />
		{:else if !status?.connected || repos.length === 0}
			<EmptyState
				title={m['forgejo_actions.title']()}
				description={!status?.connected ? m['forgejo_actions.unavailable']() : m['forgejo_actions.no_repos']()}
			/>
			{#if canManage}<div class="flex justify-center">
					<Button variant="outline" href="/{slug}/settings/forgejo">{m['forgejo_actions.settings']()}</Button>
				</div>{/if}
		{:else}
			<p class="mb-5 text-sm text-[var(--color-text-tertiary)]">{m['forgejo_actions.description']()}</p>
			<form
				class="mb-5 flex flex-wrap items-end gap-3"
				onsubmit={(event) => {
					event.preventDefault();
					applyFilters();
				}}
			>
				<div class="min-w-52 flex-1 sm:max-w-sm">
					<label for="actions-repo" class="mb-1.5 block text-xs font-medium text-[var(--color-text-secondary)]"
						>{m['forgejo_actions.repository']()}</label
					><select
						id="actions-repo"
						class="h-9 w-full rounded-md border border-[var(--app-border)] bg-[var(--color-bg-secondary)] px-3 text-sm"
						value={repoId}
						onchange={(event) => changeRepository(event.currentTarget.value)}
						>{#each repos as repo (repo.id)}<option value={repo.id}>{repo.full_name}</option>{/each}</select
					>
				</div>
				<div>
					<label for="actions-status" class="mb-1.5 block text-xs font-medium text-[var(--color-text-secondary)]"
						>{m['forgejo_actions.status']()}</label
					><select
						id="actions-status"
						class="h-9 rounded-md border border-[var(--app-border)] bg-[var(--color-bg-secondary)] px-3 text-sm"
						bind:value={statusFilter}
						onchange={applyFilters}
						><option value="">{m['forgejo_actions.all_statuses']()}</option><option value="waiting"
							>{m['forgejo_actions.status_waiting']()}</option
						><option value="running">{m['forgejo_actions.status_running']()}</option><option value="success"
							>{m['forgejo_actions.status_success']()}</option
						><option value="failure">{m['forgejo_actions.status_failure']()}</option><option value="cancelled"
							>{m['forgejo_actions.status_cancelled']()}</option
						><option value="skipped">{m['forgejo_actions.status_skipped']()}</option><option value="blocked"
							>{m['forgejo_actions.status_blocked']()}</option
						></select
					>
				</div>
				<div class="min-w-40 flex-1 sm:max-w-xs">
					<label for="actions-branch" class="mb-1.5 block text-xs font-medium text-[var(--color-text-secondary)]"
						>{m['forgejo_actions.branch']()}</label
					><Input
						id="actions-branch"
						bind:value={branchInput}
						placeholder={m['forgejo_actions.branch_placeholder']()}
						maxlength={255}
						class="h-9"
						onblur={() => {
							if (branchInput.trim() !== branchFilter) applyFilters();
						}}
					/>
				</div>
			</form>
			<div class="grid min-w-0 gap-4 xl:grid-cols-[minmax(280px,0.8fr)_minmax(0,1.6fr)]">
				<section
					aria-label={m['forgejo_actions.title']()}
					class="min-w-0 self-start rounded-lg border border-[var(--app-border)] bg-[var(--color-bg-secondary)]"
				>
					{#if runsLoading}<div class="p-5"><LoadingState /></div>{:else if runsFailed}<ErrorState
							message={m['forgejo_actions.failed_load']()}
							onretry={() => loadRuns(runsPage)}
						/>{:else if runs.length === 0}<EmptyState
							title={m['forgejo_actions.no_runs']()}
							description={m['forgejo_actions.no_runs_desc']()}
						/>{:else}
						<div class="divide-y divide-[var(--app-border)]">
							{#each runs as run (run.id)}<button
									class="flex w-full gap-3 p-4 text-left transition-colors hover:bg-[var(--color-bg-hover)] {selectedRunId ===
									run.id
										? 'bg-[var(--color-bg-hover)] ring-1 ring-inset ring-[var(--app-border)]'
										: ''}"
									aria-pressed={selectedRunId === run.id}
									onclick={() => loadRun(run.id)}
									><div class="pt-0.5"><ActionStatus status={run.status} conclusion={run.conclusion} compact /></div>
									<div class="min-w-0 flex-1">
										<div class="truncate text-sm font-medium">
											{run.title || run.name || m['forgejo_actions.run_number']({ n: run.run_number || run.id })}
										</div>
										<div class="mt-1 truncate text-xs text-[var(--color-text-secondary)]">
											{run.name || run.workflow_id} · #{run.run_number || run.id}
										</div>
										<div
											class="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-[var(--color-text-tertiary)]"
										>
											{#if run.head_branch}<span class="inline-flex min-w-0 items-center gap-1"
													><GitBranch size={12} /><span class="max-w-40 truncate">{run.head_branch}</span></span
												>{/if}{#if run.created_at}<span>{formatRelativeTime(run.created_at, getLocale())}</span>{/if}
										</div>
									</div>
									<ChevronRight size={14} class="mt-1 shrink-0 text-[var(--color-text-tertiary)]" /></button
								>{/each}
						</div>
						{#if runsPage > 1 || runsHasMore}<div
								class="flex items-center justify-between gap-2 border-t border-[var(--app-border)] p-3"
							>
								<Button size="sm" variant="ghost" disabled={runsPage <= 1} onclick={() => loadRuns(runsPage - 1)}
									><ChevronLeft size={14} />{m['forgejo_actions.previous']()}</Button
								><span class="text-xs text-[var(--color-text-tertiary)]"
									>{m['forgejo_actions.page']({ n: runsPage })}</span
								><Button size="sm" variant="ghost" disabled={!runsHasMore} onclick={() => loadRuns(runsPage + 1)}
									>{m['forgejo_actions.next']()}<ChevronRight size={14} /></Button
								>
							</div>{/if}
					{/if}
				</section>
				<section
					class="min-w-0 rounded-lg border border-[var(--app-border)] bg-[var(--color-bg-secondary)]"
					aria-label={m['forgejo_actions.jobs']()}
				>
					{#if detailsLoading}<div class="p-5"><LoadingState /></div>{:else if detailsFailed}<ErrorState
							message={m['forgejo_actions.failed_details']()}
							onretry={() => selectedRunId !== null && loadRun(selectedRunId, true)}
						/>{:else if !selectedRun}<div
							class="flex min-h-64 flex-col items-center justify-center gap-3 p-6 text-center text-sm text-[var(--color-text-tertiary)]"
						>
							<Workflow size={28} />
							<p>{m['forgejo_actions.select_run']()}</p>
						</div>{:else}
						<div class="border-b border-[var(--app-border)] p-4">
							<div class="flex flex-wrap items-start justify-between gap-3">
								<div class="min-w-0 flex-1">
									<h2 class="break-words text-base font-semibold">
										{selectedRun.title ||
											selectedRun.name ||
											m['forgejo_actions.run_number']({ n: selectedRun.run_number || selectedRun.id })}
									</h2>
									<div class="mt-2 flex flex-wrap items-center gap-3 text-xs text-[var(--color-text-tertiary)]">
										<ActionStatus status={selectedRun.status} conclusion={selectedRun.conclusion} /><span
											>#{selectedRun.run_number || selectedRun.id}</span
										><span>{selectedRun.event}</span>{#if selectedRun.head_branch}<span
												class="inline-flex items-center gap-1"><GitBranch size={12} />{selectedRun.head_branch}</span
											>{/if}{#if selectedRun.head_sha}<span
												class="inline-flex items-center gap-1"
												title={`${m['forgejo_actions.commit']()}: ${selectedRun.head_sha}`}
												><GitCommitHorizontal size={12} /><code>{selectedRun.head_sha.slice(0, 8)}</code></span
											>{/if}
									</div>
								</div>
								{#if runUrl}<Button
										size="sm"
										variant="ghost"
										href={runUrl}
										target="_blank"
										rel="noopener noreferrer"
										aria-label={m['forgejo_actions.open_forgejo']()}
										title={m['forgejo_actions.open_forgejo']()}><ExternalLink size={14} /></Button
									>{/if}
							</div>
							{#if canManage && (runIsActive || canRerun)}<div class="mt-4 flex items-center gap-2">
									{#if runIsActive}<Button
											variant="outline"
											size="sm"
											disabled={actionBusy || actionRequested === selectedRun.id}
											onclick={() => confirmAction('cancel')}
											><Square size={12} />{m['forgejo_actions.cancel_run']()}</Button
										>{:else if canRerun}<Button
											variant="outline"
											size="sm"
											disabled={actionBusy || actionRequested === selectedRun.id}
											onclick={() => confirmAction('rerun')}
											><RotateCcw size={14} />{m['forgejo_actions.rerun']()}</Button
										>{/if}{#if actionRequested === selectedRun.id}<span
											role="status"
											class="text-xs text-[var(--color-text-tertiary)]">{m['forgejo_actions.requested']()}</span
										>{/if}
								</div>{/if}
						</div>
						<div class="p-4">
							<h3 class="mb-3 text-xs font-semibold uppercase tracking-wide text-[var(--color-text-tertiary)]">
								{m['forgejo_actions.jobs']()}
							</h3>
							<div class="flex flex-wrap gap-2">
								{#each jobs as job (job.id)}<button
										aria-pressed={selectedJob?.id === job.id}
										onclick={() => loadJob(job)}
										class="flex items-center gap-2 rounded-md border px-3 py-2 text-xs transition-colors hover:bg-[var(--color-bg-hover)] {selectedJob?.id ===
										job.id
											? 'border-blue-500/50 bg-blue-500/10'
											: 'border-[var(--app-border)]'}"
										><ActionStatus status={job.status} conclusion={job.conclusion} compact /><span>{job.name}</span
										>{#if job.attempt > 1}<span class="text-[var(--color-text-tertiary)]"
												>{m['forgejo_actions.attempt']({ n: job.attempt })}</span
											>{/if}</button
									>{:else}<p class="text-sm text-[var(--color-text-tertiary)]">
										{m['forgejo_actions.no_jobs']()}
									</p>{/each}
							</div>
							{#if jobsPage > 1 || jobsHasMore}<div class="mt-3 flex items-center gap-2">
									<Button
										size="sm"
										variant="ghost"
										disabled={jobsPage <= 1}
										onclick={() => jobsPagination(jobsPage - 1)}
										><ChevronLeft size={14} />{m['forgejo_actions.previous']()}</Button
									><span class="text-xs">{m['forgejo_actions.page']({ n: jobsPage })}</span><Button
										size="sm"
										variant="ghost"
										disabled={!jobsHasMore}
										onclick={() => jobsPagination(jobsPage + 1)}
										>{m['forgejo_actions.next']()}<ChevronRight size={14} /></Button
									>
								</div>{/if}
						</div>
						{#if selectedJob}<div class="border-t border-[var(--app-border)] p-4">
								<div class="mb-3 flex flex-wrap items-center justify-between gap-2">
									<h3 class="text-sm font-medium">{selectedJob.name}</h3>
									<ActionStatus status={selectedJob.status} conclusion={selectedJob.conclusion} />
								</div>
								{#if jobFailed}<ErrorState
										message={m['forgejo_actions.failed_steps']()}
										onretry={() => selectedJob && loadJob(selectedJob)}
									/>{/if}
								{#if selectedJob.steps?.length}<div class="mb-4 space-y-1" aria-label={m['forgejo_actions.steps']()}>
										{#each selectedJob.steps as step (step.number)}<div
												class="flex min-w-0 items-center gap-2 rounded px-2 py-1.5 text-xs"
											>
												<ActionStatus status={step.status} conclusion={step.conclusion} compact /><span
													class="min-w-0 flex-1 break-words">{step.name}</span
												><span class="shrink-0 tabular-nums text-[var(--color-text-tertiary)]"
													>{actionDuration(step.started_at, step.completed_at)}</span
												>
											</div>{/each}
									</div>{/if}
								<div class="mb-2 flex flex-wrap items-center justify-between gap-2">
									<h4 class="text-xs font-semibold uppercase tracking-wide text-[var(--color-text-tertiary)]">
										{m['forgejo_actions.logs']()}
									</h4>
									<div class="flex items-center gap-2">
										{#if jobLoading}<LoaderCircle
												size={14}
												class="animate-spin text-[var(--color-text-tertiary)]"
												aria-label={m['forgejo_actions.loading']()}
											/>{/if}<Button size="sm" variant="ghost" disabled={!logs} onclick={downloadLogs}
											><Download size={13} />{m['forgejo_actions.download_logs']()}</Button
										>
									</div>
								</div>
								{#if logsFailed}<ErrorState
										message={m['forgejo_actions.failed_logs']()}
										onretry={() => selectedJob && loadJob(selectedJob, true)}
									/>{:else if logs}{#if logs.truncated}<p class="mb-2 text-xs text-amber-500">
											{m['forgejo_actions.truncated_logs']({ n: Math.round(logs.max_bytes / 1048576) })}
										</p>{/if}
									<!-- svelte-ignore a11y_no_noninteractive_tabindex (Scrollable logs need keyboard focus.) -->
									<pre
										role="region"
										tabindex="0"
										aria-label={m['forgejo_actions.logs']()}
										class="max-h-[32rem] overflow-auto rounded-md border border-[var(--app-border)] bg-[var(--color-bg-primary)] p-3 font-mono text-[11px] leading-5 text-[var(--color-text-secondary)]">{logContent ||
											m['forgejo_actions.no_logs']()}</pre>{:else if !jobLoading}<p
										class="text-xs text-[var(--color-text-tertiary)]"
									>
										{m['forgejo_actions.no_logs']()}
									</p>{/if}
							</div>{:else if jobs.length}<p
								class="border-t border-[var(--app-border)] p-6 text-center text-sm text-[var(--color-text-tertiary)]"
							>
								{m['forgejo_actions.select_job']()}
							</p>{/if}
					{/if}
				</section>
			</div>
		{/if}
	</div>
</div>

<AlertDialog.Root bind:open={actionDialogOpen}
	><AlertDialog.Content
		><AlertDialog.Header
			><AlertDialog.Title
				>{pendingAction === 'cancel'
					? m['forgejo_actions.confirm_cancel_title']()
					: m['forgejo_actions.confirm_rerun_title']()}</AlertDialog.Title
			><AlertDialog.Description
				>{pendingAction === 'cancel'
					? m['forgejo_actions.confirm_cancel_desc']()
					: m['forgejo_actions.confirm_rerun_desc']()}</AlertDialog.Description
			></AlertDialog.Header
		><AlertDialog.Footer
			><AlertDialog.Cancel disabled={actionBusy}>{m['common.cancel']()}</AlertDialog.Cancel><AlertDialog.Action
				variant={pendingAction === 'cancel' ? 'destructive' : 'default'}
				onclick={performAction}
				disabled={actionBusy}
				>{pendingAction === 'cancel'
					? m['forgejo_actions.cancel_run']()
					: m['forgejo_actions.rerun']()}</AlertDialog.Action
			></AlertDialog.Footer
		></AlertDialog.Content
	></AlertDialog.Root
>
