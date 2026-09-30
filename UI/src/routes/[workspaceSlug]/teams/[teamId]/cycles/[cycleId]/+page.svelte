<script lang="ts">
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { getCycle, completeCycle, updateCycle, deleteCycle, listCycles } from '$lib/api/cycles';
	import CompleteCycleDialog from '$lib/features/cycles/CompleteCycleDialog.svelte';
	import { updateIssue } from '$lib/api/issues';
	import { listMembers } from '$lib/api/members';
	import { listLabels } from '$lib/api/labels';
	import { getWorkspace } from '$lib/api/workspaces';
	import { issuesState } from '$lib/features/issues/issues.state.svelte';
	import { teamStatusesState } from '$lib/features/issues/team-statuses.state.svelte';
	import type { Cycle } from '$lib/types/cycle';
	import type { Issue } from '$lib/types/issue';
	import type { WorkspaceMember } from '$lib/types/workspace';
	import type { Label } from '$lib/types/label';
	import IssueRow from '$lib/features/issues/IssueRow.svelte';
	import CycleProgress from '$lib/features/cycles/CycleProgress.svelte';
	import DateRangePickerPopover from '$lib/components/shared/DateRangePickerPopover.svelte';
	import EmptyState from '$lib/components/shared/EmptyState.svelte';
	import { Badge } from '$lib/components/ui/badge';
	import { Button } from '$lib/components/ui/button';
	import * as Popover from '$lib/components/ui/popover';
	import { appToast } from '$lib/features/toast/toast';
	import { m } from '$lib/paraglide/messages.js';
	import { getLocale, setLocale } from '$lib/paraglide/runtime.js';
	import { formatRelativeTime } from '$lib/utils/format';
	import CheckCircle2 from '@lucide/svelte/icons/circle-check';
	import Play from '@lucide/svelte/icons/play';
	import Clock from '@lucide/svelte/icons/clock';
	import Trash2 from '@lucide/svelte/icons/trash-2';
	import Ellipsis from '@lucide/svelte/icons/ellipsis';
	import Search from '@lucide/svelte/icons/search';
	import Plus from '@lucide/svelte/icons/plus';
	import SquareUser from '@lucide/svelte/icons/square-user';
	import RefreshCcwDot from '@lucide/svelte/icons/refresh-ccw-dot';
	import ChevronRight from '@lucide/svelte/icons/chevron-right';
	import SidebarToggle from '$lib/components/layout/SidebarToggle.svelte';
	import { sidebarState } from '$lib/features/layout/sidebar.state.svelte';
	import { createKeyboardHandler } from '$lib/utils/keyboard';
	import { hasPermission } from '$lib/security/permissions';
	import type { Role } from '$lib/security/roles';

	const slug = $derived(page.params.workspaceSlug ?? '');
	const teamId = $derived(page.params.teamId ?? '');
	const cycleId = $derived(page.params.cycleId ?? '');

	let cycle = $state<Cycle | null>(null);
	let loading = $state(true);
	let actionsOpen = $state(false);
	let members = $state<WorkspaceMember[]>([]);
	let labels = $state<Label[]>([]);
	let workspaceRole = $state<Role>('guest');
	const canManageCycles = $derived(hasPermission(workspaceRole, 'cycle:manage'));
	const canUpdateIssues = $derived(hasPermission(workspaceRole, 'issue:update'));

	let lastSelectedId = $state<string | null>(null);

	let showComplete = $state(false);
	let allCycles = $state<Cycle[]>([]);
	const nextUpcomingCycle = $derived(
		allCycles
			.filter(c => c.status === 'upcoming')
			.sort((a, b) => {
				const aD = a.start_date ? new Date(a.start_date).getTime() : Infinity;
				const bD = b.start_date ? new Date(b.start_date).getTime() : Infinity;
				return aD - bD;
			})[0] ?? null
	);
	const incompleteCount = $derived.by(() => {
		if (!cycle?.progress) return 0;
		const { total, completed, cancelled } = cycle.progress;
		return total - completed - cancelled;
	});

	// Add issues search
	let addSearchQuery = $state('');
	let addSearchOpen = $state(false);

	onMount(async () => {
		try {
			const [c, m, l, cyc, workspace] = await Promise.all([
				getCycle(slug, teamId, cycleId),
				listMembers(slug),
				listLabels(slug),
				listCycles(slug, teamId),
				getWorkspace(slug)
			]);
			cycle = c;
			members = m;
			labels = l;
			allCycles = cyc;
			workspaceRole = workspace.current_user_role as Role;
			// Load team statuses for this team
			teamStatusesState.load(slug, teamId);
			// Load issues for this cycle using server-side cycle filter
			issuesState.load(slug, { cycle: cycleId, per_page: '200' });
		} catch {
			appToast.error(m['cycles.toast.not_found']());
			goto(`/${slug}/teams/${teamId}/cycles`);
		} finally {
			loading = false;
		}
	});

	// Issues available to add (same team, no cycle assigned) — requires a separate search
	let availableIssues = $state<import('$lib/types/issue').Issue[]>([]);
	let searchingAvailable = $state(false);

	async function searchAvailableIssues() {
		if (!canUpdateIssues) return;
		if (!addSearchQuery.trim()) {
			availableIssues = [];
			return;
		}
		searchingAvailable = true;
		try {
			const { listIssues } = await import('$lib/api/issues');
			const q = addSearchQuery.toLowerCase();
			const results = await listIssues(slug, { team: teamId, per_page: '50' });
			availableIssues = results.data
				.filter((i: import('$lib/types/issue').Issue) => i.cycle_id !== cycleId && (i.title.toLowerCase().includes(q) || i.identifier.toLowerCase().includes(q)))
				.slice(0, 10);
		} catch {
			availableIssues = [];
		} finally {
			searchingAvailable = false;
		}
	}

	function handleComplete() {
		if (!cycle || !canManageCycles) return;
		showComplete = true;
	}

	async function handleCompleteSubmit(data: { retrospective?: string; carry_over: boolean }) {
		if (!cycle || !canManageCycles) return;
		try {
			const result = await completeCycle(slug, teamId, cycle.id, {
				retrospective: data.retrospective,
				carry_over: data.carry_over
			});
			cycle = result.cycle;
			appToast.success(m['cycles.toast.completed']());
			issuesState.load(slug, { cycle: cycleId, per_page: '200' });
		} catch (err: any) {
			appToast.apiError(err, m['cycles.toast.failed_complete']());
		}
	}

	async function handleActivate() {
		if (!cycle || !canManageCycles) return;
		try {
			cycle = await updateCycle(slug, teamId, cycle.id, { status: 'active' });
			appToast.success(m['cycles.toast.activated']());
		} catch (err: any) {
			appToast.apiError(err, m['cycles.toast.failed_activate']());
		}
	}

	async function handleDelete() {
		if (!cycle || !canManageCycles) return;
		try {
			await deleteCycle(slug, teamId, cycle.id);
			appToast.success(m['cycles.toast.deleted']());
			goto(`/${slug}/teams/${teamId}/cycles`);
		} catch (err: any) {
			appToast.apiError(err, m['cycles.toast.failed_delete']());
		}
	}

	async function handleDateRangeChange(start: string, end: string) {
		if (!cycle || !canManageCycles) return;
		try {
			cycle = await updateCycle(slug, teamId, cycle.id, { start_date: start, end_date: end });
			appToast.success(m['cycles.toast.dates_updated']());
		} catch (err: any) {
			appToast.apiError(err, m['cycles.toast.failed_update_dates']());
		}
	}

	async function handleAddIssueToCycle(issue: Issue) {
		if (!canUpdateIssues) return;
		try {
			await updateIssue(slug, issue.identifier, { cycle_id: cycleId });
			// Reload to reflect change
			issuesState.load(slug, { cycle: cycleId, per_page: '200' });
			addSearchQuery = '';
			appToast.success(m['cycles.toast.added_issue']({ identifier: issue.identifier }));
		} catch (err: any) {
			appToast.apiError(err, m['cycles.toast.failed_add_issue']());
		}
	}

	const keyHandler = createKeyboardHandler([
		{ key: 'a', ctrl: true, handler: () => issuesState.selectAll() },
		{ key: 'Escape', handler: () => issuesState.clearSelection() },
	]);

	onMount(() => {
		document.addEventListener('keydown', keyHandler);
		return () => document.removeEventListener('keydown', keyHandler);
	});

	const STATUS_ICONS = {
		upcoming: Clock,
		active: Play,
		completed: CheckCircle2
	} as const;
</script>

<div class="flex h-full min-w-0 flex-col">
	{#if !loading && cycle}
		<!-- Header -->
		<div class="flex min-h-[49px] items-center justify-between gap-2 border-b border-[var(--app-border)] px-3 sm:px-4">
			<div class="flex min-w-0 items-center gap-3">
				<SidebarToggle />
				<nav class="flex min-w-0 items-center gap-1.5 text-sm">
					{#if sidebarState.getTeam(teamId)}
						<a href="/{slug}/teams/{teamId}" class="flex items-center gap-1.5 text-[var(--color-text-tertiary)] hover:text-[var(--color-text-secondary)]">
							<SquareUser size={14} class="shrink-0" style="color: {sidebarState.getTeamColor(teamId)}" />
							<span class="hidden sm:inline truncate">{sidebarState.getTeam(teamId)?.name}</span>
						</a>
						<ChevronRight size={12} class="shrink-0 text-[var(--color-text-tertiary)]" />
						<a href="/{slug}/teams/{teamId}/cycles" class="flex items-center gap-1.5 text-[var(--color-text-tertiary)] hover:text-[var(--color-text-secondary)]">
							<RefreshCcwDot size={14} class="shrink-0" />
							<span class="hidden sm:inline">{m['cycles.title']()}</span>
						</a>
						<ChevronRight size={12} class="shrink-0 text-[var(--color-text-tertiary)]" />
					{/if}
					<span class="truncate font-medium text-[var(--color-text-primary)]">{cycle.name}</span>
				</nav>
				<Badge variant={cycle.status === 'active' ? 'default' : cycle.status === 'completed' ? 'secondary' : 'outline'} class="shrink-0 text-[10px]">
					{cycle.status}
				</Badge>
			</div>
			{#if canManageCycles}
			<div class="flex shrink-0 items-center gap-2">
				{#if cycle.status === 'upcoming'}
					<Button size="sm" onclick={handleActivate}>
						<Play size={14} class="mr-1" />
						{m['cycles.start_cycle']()}
					</Button>
				{/if}
				{#if cycle.status === 'active'}
					<Button size="sm" onclick={handleComplete}>
						<CheckCircle2 size={14} class="mr-1" />
						{m['cycles.complete']()}
					</Button>
				{/if}
				<Popover.Root bind:open={actionsOpen}>
					<Popover.Trigger>
						<Button variant="ghost" size="icon-sm">
							<Ellipsis size={14} />
						</Button>
					</Popover.Trigger>
					<Popover.Content class="w-40 p-1" align="end">
						<button
							onclick={() => { actionsOpen = false; handleDelete(); }}
							class="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-sm text-[var(--color-error)] hover:bg-[var(--color-bg-hover)]"
						>
							<Trash2 size={14} />
							{m['cycles.delete_cycle']()}
						</button>
					</Popover.Content>
				</Popover.Root>
			</div>
			{/if}
		</div>

		<!-- Cycle info -->
		<div class="border-b border-[var(--app-border)] px-3 py-3 sm:px-6 sm:py-4">
			<div class="flex flex-wrap items-center gap-3 sm:gap-6">
				<div class="flex items-center gap-2 text-xs text-[var(--color-text-tertiary)]">
					{#if canManageCycles}
						<DateRangePickerPopover
							startDate={cycle.start_date}
							endDate={cycle.end_date}
							onchange={handleDateRangeChange}
							placeholder={m['cycles.select_dates']()}
						/>
					{:else if cycle.start_date || cycle.end_date}
						<span>{cycle.start_date?.slice(0, 10) ?? ''} to {cycle.end_date?.slice(0, 10) ?? ''}</span>
					{/if}
				</div>
				{#if cycle.progress}
					<div class="flex items-center gap-2 text-xs text-[var(--color-text-tertiary)]">
						<span>{m['cycles.issues_done']({ completed: cycle.progress.completed, total: cycle.progress.total })}</span>
					</div>
				{/if}
				{#if cycle.completed_at}
					<div class="text-xs text-[var(--color-text-tertiary)]">
						Completed {formatRelativeTime(cycle.completed_at, getLocale())}
					</div>
				{/if}
			</div>
			{#if cycle.description}
				<p class="mt-2 text-sm text-[var(--color-text-secondary)]">{cycle.description}</p>
			{/if}
			{#if cycle.goals}
				<div class="mt-2">
					<span class="text-xs font-medium text-[var(--color-text-tertiary)]">{m['cycles.goals']()}</span>
					<p class="mt-0.5 text-sm text-[var(--color-text-secondary)]">{cycle.goals}</p>
				</div>
			{/if}
			{#if cycle.retrospective}
				<div class="mt-2">
					<span class="text-xs font-medium text-[var(--color-text-tertiary)]">{m['cycles.retrospective']()}</span>
					<p class="mt-0.5 text-sm text-[var(--color-text-secondary)]">{cycle.retrospective}</p>
				</div>
			{/if}
			{#if cycle.progress && cycle.progress.total > 0}
				<div class="mt-3 w-full sm:w-64">
					<CycleProgress progress={cycle.progress} />
				</div>
			{/if}
		</div>

		<!-- Add issues section -->
		{#if canUpdateIssues}
		<div class="border-b border-[var(--app-border)] px-3 py-3 sm:px-6">
			<div class="relative">
				<div class="flex items-center gap-2 rounded-md border border-[var(--app-border)] bg-[var(--color-bg-secondary)] px-3 py-1.5">
					<Search size={14} class="text-[var(--color-text-tertiary)]" />
					<input
						type="text"
						bind:value={addSearchQuery}
						oninput={() => searchAvailableIssues()}
						onfocus={() => (addSearchOpen = true)}
						onblur={() => setTimeout(() => (addSearchOpen = false), 200)}
						placeholder={m['cycles.search_issues_placeholder']()}
						class="w-full bg-transparent text-sm text-[var(--color-text-primary)] placeholder:text-[var(--color-text-tertiary)] outline-none"
					/>
				</div>
				{#if addSearchOpen && availableIssues.length > 0}
					<div class="absolute left-0 right-0 top-full z-50 mt-1 max-h-60 overflow-y-auto rounded-md border border-[var(--app-border)] bg-[var(--color-bg)] shadow-lg">
						{#each availableIssues as issue}
							<button
								onmousedown={() => handleAddIssueToCycle(issue)}
								class="flex w-full items-center gap-3 px-3 py-2 text-left text-sm hover:bg-[var(--color-bg-hover)]"
							>
								<Plus size={14} class="shrink-0 text-[var(--color-text-tertiary)]" />
								<span class="shrink-0 text-xs text-[var(--color-text-tertiary)]">{issue.identifier}</span>
								<span class="truncate text-[var(--color-text-primary)]">{issue.title}</span>
							</button>
						{/each}
					</div>
				{/if}
			</div>
		</div>
		{/if}

		<!-- Issues list -->
		<div class="flex-1 overflow-y-auto">
			{#if !issuesState.loading && issuesState.issues.length === 0}
				<EmptyState
					title={m['cycles.no_issues']()}
					description={m['cycles.no_issues_desc']()}
				/>
			{:else}
				{#each issuesState.issues as issue (issue.id)}
					<IssueRow {issue} {slug} {members} {labels} {lastSelectedId} onlastselected={(id) => lastSelectedId = id} onclick={(i) => { lastSelectedId = i.id; goto(`/${slug}/issue/${i.identifier}`); }} />
				{/each}
			{/if}
		</div>
	{/if}
</div>

{#if canManageCycles}
	<CompleteCycleDialog
		bind:open={showComplete}
		cycle={cycle}
		{incompleteCount}
		{nextUpcomingCycle}
		onsubmit={handleCompleteSubmit}
	/>
{/if}
