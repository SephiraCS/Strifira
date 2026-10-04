<script lang="ts">
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { authState } from '$lib/features/auth/auth.state.svelte';
	import { issuesState } from '$lib/features/issues/issues.state.svelte';
	import { teamStatusesState } from '$lib/features/issues/team-statuses.state.svelte';
	import IssueRow from '$lib/features/issues/IssueRow.svelte';
	import KanbanBoard from '$lib/features/issues/KanbanBoard.svelte';
	import FilterBuilder from '$lib/components/shared/FilterBuilder.svelte';
	import ViewSwitcher from '$lib/components/shared/ViewSwitcher.svelte';
	import EmptyState from '$lib/components/shared/EmptyState.svelte';
	import IssueGroupHeader from '$lib/features/issues/IssueGroupHeader.svelte';
	import IssueListLoadMore from '$lib/features/issues/IssueListLoadMore.svelte';
	import SidebarToggle from '$lib/components/layout/SidebarToggle.svelte';
	import * as Tabs from '$lib/components/ui/tabs';
	import CreateIssueDialog from '$lib/features/issues/LazyCreateIssueDialog.svelte';
	import { showIssueCreatedToast } from '$lib/features/issues/issue-created-toast';
	import { appToast } from '$lib/features/toast/toast';
	import type { Team } from '$lib/types/team';
	import type { Project } from '$lib/types/project';
	import type { Label } from '$lib/types/label';
	import type { WorkspaceMember } from '$lib/types/workspace';
	import type { ViewFilter, ViewLayout } from '$lib/types/view';
	import { viewLayoutState, setSessionViewLayout } from '$lib/features/issues/view-layout.state.svelte';
	import type { Issue, RelationType } from '$lib/types/issue';
	import AddRelationDialog from '$lib/features/issues/AddRelationDialog.svelte';
	import CircleUser from '@lucide/svelte/icons/circle-user';
	import PenLine from '@lucide/svelte/icons/pen-line';
	import { createKeyboardHandler } from '$lib/utils/keyboard';
	import BulkActionBar from '$lib/features/issues/BulkActionBar.svelte';
	import { m } from '$lib/paraglide/messages.js';
	import { getLocale, setLocale } from '$lib/paraglide/runtime.js';
	import { workspaceLabels, workspaceMembers, workspaceProjects, workspaceTeams } from '$lib/features/layout/workspace-resources';

	const slug = $derived(page.params.workspaceSlug ?? '');

	type MyTab = 'assigned' | 'created';
	const DRILL_DOWN_FILTERS = [
		'status',
		'status_type',
		'priority',
		'assignee',
		'project',
		'cycle',
		'team',
		'label',
		'creator'
	] as const;

	function initialFilters(): ViewFilter {
		const filters: ViewFilter = {};
		for (const key of DRILL_DOWN_FILTERS) {
			const value = page.url.searchParams.get(key);
			if (value) filters[key] = value;
		}
		return filters;
	}

	let activeTab = $state<MyTab>('assigned');
	const initialDrillDownFilters = initialFilters();
	let filters = $state<ViewFilter>(initialDrillDownFilters);
	let drillDownMode = $state(Object.keys(initialDrillDownFilters).length > 0);
	let layout = $state<ViewLayout>(viewLayoutState.layout);
	let teams = $state<Team[]>([]);
	let projects = $state<Project[]>([]);
	let labels = $state<Label[]>([]);
	let members = $state<WorkspaceMember[]>([]);
	let showCreateIssue = $state(false);
	let quickAddStatusId = $state<string | undefined>(undefined);
	let quickAddAssigneeIds = $state<string[] | undefined>(undefined);
	let boardTeamId = $state<string | undefined>(undefined);
	let collapsedGroups = $state<Set<string>>(new Set());
	let lastSelectedId = $state<string | null>(null);
	let relationDialogOpen = $state(false);
	let relationIssue = $state<Issue | null>(null);
	let relationDefaultType = $state<RelationType>('related');

	function handleAddRelation(issue: Issue, type: RelationType) {
		relationIssue = issue;
		relationDefaultType = type;
		relationDialogOpen = true;
	}

	// Follow the preferred default view once preferences sync (session
	// memory captures it at mount when the local cache is warm).
	$effect(() => {
		if (!viewLayoutState.overridden && viewLayoutState.layout !== layout) {
			layout = viewLayoutState.layout;
			loadIssues();
		}
	});

	onMount(() => {
		void loadIssues();
		void Promise.all([workspaceTeams(slug), workspaceProjects(slug), workspaceLabels(slug), workspaceMembers(slug)]).then(
			([t, p, l, m]) => {
				teams = t;
				projects = p;
				labels = l;
				members = m;
			}
		);
	});

	async function loadIssues() {
		if (!authState.user) return;
		const params: Record<string, string> = {};

		// Tab-specific filter
		if (!drillDownMode && activeTab === 'assigned') {
			params.assignee = authState.user.id;
		} else if (!drillDownMode) {
			params.creator = authState.user.id;
		}

		// Apply user filters
		for (const [key, value] of Object.entries(filters)) {
			if (value !== undefined && value !== '') {
				params[key] = value;
			}
		}

		// Keep server pagination aligned with the status-grouped rendering.
		params.group_by = 'status';
		params.sort = 'priority';
		params.order = 'asc';

		if (layout === 'board') {
			// Boards render whole columns with no "load more": follow every
			// page so issues beyond the first page don't vanish when a move
			// changes their sort order.
			params.per_page = '100';
		}

		issuesState.groupBy = 'status';
		await issuesState.load(slug, params, true, layout === 'board');

		// Load team statuses from the first issue's team for status pickers
		const firstTeamId = issuesState.issues[0]?.team_id;
		if (firstTeamId) {
			boardTeamId = firstTeamId;
			teamStatusesState.load(slug, firstTeamId);
		}
	}

	// Every team present in the loaded issues gets its own board section, so
	// cross-team issues render with their own team's columns instead of
	// silently dropping off the board.
	const boardTeamIds = $derived([
		...new Set(issuesState.issues.map((i) => i.team_id).filter(Boolean))
	]);

	$effect(() => {
		if (layout !== 'board') return;
		const slugValue = slug;
		for (const teamId of boardTeamIds) {
			void teamStatusesState.loadTeam(slugValue, teamId).catch(() => {});
		}
	});

	function issuesByStatusForTeam(teamId: string): Record<string, Issue[]> {
		const grouped: Record<string, Issue[]> = {};
		for (const issue of issuesState.issues) {
			if (issue.team_id !== teamId) continue;
			const key = issue.status_id ?? issue.status;
			(grouped[key] ??= []).push(issue);
		}
		return grouped;
	}

	function handleTabChange(tab: string) {
		activeTab = tab as MyTab;
		loadIssues();
	}

	function handleFilterChange(f: ViewFilter) {
		filters = f;
		if (!DRILL_DOWN_FILTERS.some((key) => !!f[key])) drillDownMode = false;
		loadIssues();
	}

	function handleLayoutChange(l: ViewLayout) {
		layout = l;
		setSessionViewLayout(l);
		loadIssues();
	}

	function handleIssueClick(issue: Issue) {
		lastSelectedId = issue.id;
		goto(`/${slug}/issue/${issue.identifier}`);
	}

	function handleBoardQuickAdd(statusId: string) {
		quickAddStatusId = statusId;
		// The create dialog must land on the team that owns the column.
		const ownerTeamId = boardTeamIds.find((teamId) =>
			(teamStatusesState.statusesByTeam[teamId] ?? []).some((s) => s.id === statusId)
		);
		if (ownerTeamId) boardTeamId = ownerTeamId;
		// On the assigned tab an unassigned issue would vanish at
		// the post-create reload (the tab filters by assignee), so
		// quick-adds there default to the current user.
		quickAddAssigneeIds =
			!drillDownMode && activeTab === 'assigned' && authState.user ? [authState.user.id] : undefined;
		showCreateIssue = true;
	}

	const keyHandler = createKeyboardHandler([
		{ key: 'a', ctrl: true, handler: () => issuesState.selectAll() },
		{ key: 'Escape', handler: () => issuesState.clearSelection() }
	]);

	onMount(() => {
		document.addEventListener('keydown', keyHandler);
		return () => document.removeEventListener('keydown', keyHandler);
	});

	function toggleGroup(key: string) {
		const next = new Set(collapsedGroups);
		if (next.has(key)) {
			next.delete(key);
		} else {
			next.add(key);
		}
		collapsedGroups = next;
	}
</script>

<div class="flex h-full flex-col">
	<!-- Header -->
	<div class="flex h-[49px] items-center justify-between border-b border-[var(--app-border)] px-6">
		<div class="flex items-center gap-2">
			<SidebarToggle />
			<h1 class="text-sm font-medium text-[var(--color-text-primary)]">{m['my_issues.title']()}</h1>
		</div>
		<ViewSwitcher bind:layout onchange={handleLayoutChange} />
	</div>

	<!-- Tabs -->
	<Tabs.Root value={activeTab} onValueChange={handleTabChange}>
		<Tabs.List class="w-full justify-start gap-1.5 rounded-none border-none bg-transparent px-2 pt-4 pb-2">
			<Tabs.Trigger
				value="assigned"
				class="flex-none h-auto rounded-full border border-[var(--app-border)] px-2.5 py-1 text-xs text-[var(--color-text-tertiary)] shadow-none data-[state=active]:border-[var(--app-accent)]/30 data-[state=active]:bg-[var(--app-accent)]/10 data-[state=active]:text-[var(--app-accent-light)] data-[state=active]:shadow-none"
			>
				<CircleUser size={13} class="mr-1" />
				{m['my_issues.tab.assigned']()}
			</Tabs.Trigger>
			<Tabs.Trigger
				value="created"
				class="flex-none h-auto rounded-full border border-[var(--app-border)] px-2.5 py-1 text-xs text-[var(--color-text-tertiary)] shadow-none data-[state=active]:border-[var(--app-accent)]/30 data-[state=active]:bg-[var(--app-accent)]/10 data-[state=active]:text-[var(--app-accent-light)] data-[state=active]:shadow-none"
			>
				<PenLine size={13} class="mr-1" />
				{m['my_issues.tab.created']()}
			</Tabs.Trigger>
		</Tabs.List>
	</Tabs.Root>

	<!-- Filter bar -->
	<FilterBuilder bind:filters {projects} {labels} {members} onchange={handleFilterChange} />

	<!-- Content -->
	{#if layout === 'list'}
		<div class="flex-1 overflow-y-auto">
			{#if !issuesState.loading && issuesState.issues.length === 0}
				<EmptyState
					title={activeTab === 'assigned'
						? m['my_issues.empty.assigned.title']()
						: m['my_issues.empty.created.title']()}
					description={activeTab === 'assigned'
						? m['my_issues.empty.assigned.description']()
						: m['my_issues.empty.created.description']()}
				/>
			{:else if issuesState.groupBy}
				{#each issuesState.groupedIssues as group (group.key)}
					<section>
						<IssueGroupHeader
							groupKey={group.key}
							groupBy={issuesState.groupBy}
							groupLabel={group.label}
							count={group.issues.length}
							collapsed={collapsedGroups.has(group.key)}
							ontoggle={() => toggleGroup(group.key)}
						/>
						{#if !collapsedGroups.has(group.key)}
							{#each group.issues as issue (issue.id)}
								<IssueRow
									{issue}
									{slug}
									{members}
									{labels}
									{projects}
									onclick={handleIssueClick}
									{lastSelectedId}
									onlastselected={(id) => (lastSelectedId = id)}
									onaddrelation={handleAddRelation}
								/>
							{/each}
						{/if}
					</section>
				{/each}
			{:else}
				{#each issuesState.issues as issue (issue.id)}
					<IssueRow
						{issue}
						{slug}
						{members}
						{labels}
						{projects}
						onclick={handleIssueClick}
						{lastSelectedId}
						onlastselected={(id) => (lastSelectedId = id)}
						onaddrelation={handleAddRelation}
					/>
				{/each}
			{/if}

			<IssueListLoadMore />

			<BulkActionBar
				{slug}
				{labels}
				{members}
				onlabelcreated={(label) => (labels = [label, ...labels.filter((existing) => existing.id !== label.id)])}
			/>
		</div>
		{:else if issuesState.issues.length > 0 || !issuesState.loading}
		{#if issuesState.issues.length === 0}
			<div class="flex-1 overflow-y-auto">
				<EmptyState
					title={activeTab === 'assigned'
						? m['my_issues.empty.assigned.title']()
						: m['my_issues.empty.created.title']()}
					description={activeTab === 'assigned'
						? m['my_issues.empty.assigned.description']()
						: m['my_issues.empty.created.description']()}
				/>
			</div>
		{:else if boardTeamIds.length === 1}
			<div class="flex-1 overflow-hidden">
				<KanbanBoard
					issuesByStatus={issuesByStatusForTeam(boardTeamIds[0])}
					columns={teamStatusesState.statusesByTeam[boardTeamIds[0]] ?? []}
					{slug}
					{members}
					{labels}
					onissueclick={handleIssueClick}
					onquickadd={handleBoardQuickAdd}
				/>
			</div>
		{:else}
			<div class="flex-1 overflow-y-auto">
				{#each boardTeamIds as teamId (teamId)}
					{@const teamIssues = issuesByStatusForTeam(teamId)}
					{@const teamCount = Object.values(teamIssues).reduce((n, list) => n + list.length, 0)}
					<section>
						<div class="sticky top-0 z-10 flex items-center gap-2 border-b border-[var(--app-border)] bg-[var(--color-bg-primary)] px-6 py-2">
							<span class="text-xs font-medium tracking-wide uppercase text-[var(--color-text-tertiary)]">
								{teams.find((t) => t.id === teamId)?.name ?? teamId}
							</span>
							<span class="text-xs text-[var(--color-text-tertiary)]">{teamCount}</span>
						</div>
						<div class="h-[60vh] min-h-[420px]">
							<KanbanBoard
								issuesByStatus={teamIssues}
								columns={teamStatusesState.statusesByTeam[teamId] ?? []}
								{slug}
								{members}
								{labels}
								onissueclick={handleIssueClick}
								onquickadd={handleBoardQuickAdd}
							/>
						</div>
					</section>
				{/each}
			</div>
		{/if}
	{:else}
		<!-- First load only; later refetches keep the current board on screen
			instead of blanking it (the old blank-flash). -->
		<div class="flex flex-1 items-center justify-center">
			<div class="h-5 w-5 animate-spin rounded-full border-2 border-[var(--color-text-tertiary)] border-t-transparent"></div>
		</div>
	{/if}
</div>

<AddRelationDialog
	bind:open={relationDialogOpen}
	{slug}
	identifier={relationIssue?.identifier ?? ''}
	defaultType={relationDefaultType}
	oncreated={loadIssues}
/>

<CreateIssueDialog
	bind:open={showCreateIssue}
	{slug}
	{teams}
	{projects}
	{labels}
	{members}
	defaultTeamId={boardTeamId}
	defaultStatusId={quickAddStatusId}
	defaultAssigneeIds={quickAddAssigneeIds}
	onlabelcreated={(label) => (labels = [label, ...labels.filter((existing) => existing.id !== label.id)])}
	onsubmit={async (req) => {
		try {
			const created = await issuesState.create(slug, req);
			showIssueCreatedToast(slug, created);
			void loadIssues();
		} catch (err: any) {
			appToast.apiError(err, 'Failed to create issue');
		}
	}}
/>
