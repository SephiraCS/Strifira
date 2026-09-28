<script lang="ts">
	import type { Issue } from '$lib/types/issue';
	import type { TeamStatus } from '$lib/types/team-status';
	import type { WorkspaceMember } from '$lib/types/workspace';
	import type { Label } from '$lib/types/label';
	import KanbanColumn from './KanbanColumn.svelte';
	import { dragHandleZone } from 'svelte-dnd-action';
	import { issuesState } from './issues.state.svelte';
	import { teamStatusesState } from './team-statuses.state.svelte';
	import { m } from '$lib/paraglide/messages.js';

	let {
		issuesByStatus,
		slug = '',
		members = [],
		labels = [],
		columns: columnsProp,
		onissueclick,
		onquickadd
	}: {
		issuesByStatus: Record<string, Issue[]>;
		slug?: string;
		members?: WorkspaceMember[];
		labels?: Label[];
		/** Explicit column set (e.g. one team's statuses on the cross-team My
		 * Issues board). Falls back to the shared single-team status state. */
		columns?: TeamStatus[];
		onissueclick: (issue: Issue) => void;
		onquickadd?: (statusId: string) => void;
	} = $props();

	// Local mutable copy for drag state
	let localByStatus = $state<Record<string, Issue[]>>({});

	// Local column order so the board can be reordered by dragging the headers.
	// Seeded eagerly so there is no empty frame when statuses are already loaded.
	let columns = $state<TeamStatus[]>([...teamStatusesState.statusOrder]);

	// Column reordering only applies to the shared per-team state; boards with
	// explicit columns (My Issues) keep their given order.
	const canReorderColumns = $derived(columnsProp === undefined);

	// Issues whose status key matches no column (stale status_id, legacy slug
	// keys) render in a trailing bucket instead of silently disappearing.
	let unmapped = $state<Issue[]>([]);
	const unmappedStatus = $derived<TeamStatus>({
		id: '__unmapped__',
		team_id: '',
		name: m['board.unmapped_status'](),
		slug: '__unmapped__',
		category: 'backlog',
		color: null,
		position: Number.MAX_SAFE_INTEGER,
		is_default: false,
		project_ids: [],
		created_at: '',
		updated_at: ''
	});

	$effect(() => {
		columns = [...(columnsProp ?? teamStatusesState.statusOrder)];
	});

	$effect(() => {
		const copy: Record<string, Issue[]> = {};
		for (const ts of columns) {
			copy[ts.id] = [...(issuesByStatus[ts.id] ?? [])];
		}
		const columnIds = new Set(columns.map((ts) => ts.id));
		const stuck: Issue[] = [];
		for (const [key, issues] of Object.entries(issuesByStatus)) {
			if (!columnIds.has(key)) stuck.push(...issues);
		}
		localByStatus = copy;
		unmapped = stuck;
	});

	function handleColumnConsider(e: CustomEvent<{ items: TeamStatus[] }>) {
		columns = e.detail.items;
	}

	function handleColumnFinalize(e: CustomEvent<{ items: TeamStatus[] }>) {
		columns = e.detail.items;
		void teamStatusesState.reorder(slug, columns.map((ts) => ts.id));
	}

	function handleConsider(statusId: string, items: Issue[]) {
		localByStatus[statusId] = items;
	}

	function handleFinalize(statusId: string, items: Issue[]) {
		localByStatus[statusId] = items;
		// The unmapped bucket has no real status behind it; issues only ever
		// leave it (by dropping into a real column), never enter it.
		if (statusId === unmappedStatus.id) return;

		// Detect moved issues and update
		for (let i = 0; i < items.length; i++) {
			const issue = items[i];
			const originalStatusId = issue.status_id ?? issue.status;
			if (originalStatusId !== statusId) {
				// Cross-column move: update status_id
				const sortOrder = calculateSortOrder(items, i);
				issuesState.update(slug, issue.identifier, { status_id: statusId, sort_order: sortOrder });
			} else {
				// Same column reorder: check if position changed
				const originalItems = issuesByStatus[statusId] ?? [];
				const originalIdx = originalItems.findIndex((it) => it.id === issue.id);
				if (originalIdx !== i) {
					const sortOrder = calculateSortOrder(items, i);
					issuesState.update(slug, issue.identifier, { sort_order: sortOrder });
				}
			}
		}
	}

	function calculateSortOrder(items: Issue[], index: number): number {
		const prev = index > 0 ? items[index - 1].sort_order : undefined;
		const next = index < items.length - 1 ? items[index + 1].sort_order : undefined;
		if (prev !== undefined && next !== undefined) return (prev + next) / 2;
		if (prev !== undefined) return prev + 1000;
		if (next !== undefined) return next - 1000;
		return 0;
	}
</script>

{#snippet boardColumns()}
	{#each columns as ts (ts.id)}
		<KanbanColumn
			statusId={ts.id}
			teamStatus={ts}
			issues={localByStatus[ts.id] ?? []}
			{slug}
			{members}
			{labels}
			{onissueclick}
			onconsider={handleConsider}
			onfinalize={handleFinalize}
			{onquickadd}
		/>
	{/each}
	{#if unmapped.length > 0}
		<!-- Read-only bucket: keeps status-orphaned issues visible (and draggable
			out into a real column) instead of dropping them from the board. -->
		<KanbanColumn
			statusId={unmappedStatus.id}
			teamStatus={unmappedStatus}
			issues={unmapped}
			{slug}
			{members}
			{labels}
			{onissueclick}
			onconsider={handleConsider}
			onfinalize={handleFinalize}
		/>
	{/if}
{/snippet}

{#if canReorderColumns}
	<div
		class="flex h-full gap-4 overflow-x-auto p-4"
		use:dragHandleZone={{
			items: columns,
			type: 'column',
			dropTargetStyle: {}
		}}
		onconsider={handleColumnConsider}
		onfinalize={handleColumnFinalize}
	>
		{@render boardColumns()}
	</div>
{:else}
	<div class="flex h-full gap-4 overflow-x-auto p-4">
		{@render boardColumns()}
	</div>
{/if}
