<script lang="ts">
	import type { Issue } from '$lib/types/issue';
	import type { TeamStatus } from '$lib/types/team-status';
	import type { WorkspaceMember } from '$lib/types/workspace';
	import type { Label } from '$lib/types/label';
	import KanbanColumn from './KanbanColumn.svelte';
	import { sortColumnIssues, type ColumnSortKey } from './column-sort';
	import { dragHandleZone } from 'svelte-dnd-action';
	import { issuesState } from './issues.state.svelte';
	import { teamStatusesState } from './team-statuses.state.svelte';
	import { appToast } from '$lib/features/toast/toast';
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

	// Per-column display sorts; a column without an entry keeps the board's
	// manual (sort_order) arrangement.
	let columnSorts = $state<Record<string, ColumnSortKey>>({});
	let archiveInFlight = false;

	// Last id/version sequence applied to each column. Background refreshes
	// replace the issues array wholesale even when nothing changed; without
	// this memo every one of them hands every column a new array,
	// svelte-dnd-action re-initializes each zone (hundreds of DOM writes per
	// refresh), and the whole board flickers.
	const lastApplied = new Map<string, string[]>();

	// Columns whose zone is mid-drag: svelte-dnd-action owns their order
	// until finalize, so refreshes must not touch them.
	const draggingColumns = new Set<string>();

	function handleDragState(statusId: string, dragging: boolean) {
		if (dragging) draggingColumns.add(statusId);
		else draggingColumns.delete(statusId);
	}

	/** A column's canonical order is sort_order (the server's board order),
	 * not the raw list order: a just-dropped issue gets its re-averaged
	 * sort_order back from the PATCH response immediately, so the card stays
	 * where it was dropped instead of flashing back to its old spot until
	 * the next full refresh. */
	function canonicalColumn(statusId: string): Issue[] {
		return [...(issuesByStatus[statusId] ?? [])].sort((a, b) => a.sort_order - b.sort_order);
	}

	function columnSignature(issues: Issue[]): string[] {
		// updated_at is the server's per-issue version (every mutation bumps
		// it), so an unchanged signature means nothing to re-render.
		return issues.map((issue) => `${issue.id}@${issue.updated_at}`);
	}

	function sameSignature(a: string[] | undefined, b: string[]): boolean {
		return (
			a !== undefined && a.length === b.length && a.every((sig, i) => sig === b[i])
		);
	}

	$effect(() => {
		const columnIds = new Set(columns.map((ts) => ts.id));
		for (const ts of columns) {
			if (draggingColumns.has(ts.id)) continue;
			// Background refreshes re-copy the server order; re-apply the
			// column's active sort so it survives every reload.
			const incoming = sortColumnIssues(canonicalColumn(ts.id), columnSorts[ts.id] ?? null);
			const signature = columnSignature(incoming);
			if (sameSignature(lastApplied.get(ts.id), signature)) continue;
			lastApplied.set(ts.id, signature);
			localByStatus[ts.id] = incoming;
		}
		for (const key of [...lastApplied.keys()]) {
			if (!columnIds.has(key)) {
				lastApplied.delete(key);
				delete localByStatus[key];
			}
		}
		const stuck: Issue[] = [];
		for (const [key, issues] of Object.entries(issuesByStatus)) {
			if (!columnIds.has(key)) stuck.push(...issues);
		}
		const stuckSignature = columnSignature(stuck);
		if (!sameSignature(lastUnmapped, stuckSignature)) {
			lastUnmapped = stuckSignature;
			unmapped = stuck;
		}
	});

	let lastUnmapped: string[] | undefined;

	function handleSortChange(statusId: string, key: ColumnSortKey | null) {
		if (key === null) {
			delete columnSorts[statusId];
			columnSorts = { ...columnSorts };
			localByStatus[statusId] = canonicalColumn(statusId);
		} else {
			columnSorts = { ...columnSorts, [statusId]: key };
			localByStatus[statusId] = sortColumnIssues(localByStatus[statusId] ?? [], key);
		}
	}

	async function handleArchiveAll(statusId: string) {
		const ids = (localByStatus[statusId] ?? []).map((issue) => issue.id);
		if (ids.length === 0 || archiveInFlight) return;
		archiveInFlight = true;
		try {
			const archived = await issuesState.bulkArchive(slug, ids);
			appToast.success(m['issues.archive_all.toast']({ n: archived, s: archived > 1 ? 's' : '' }));
		} catch (err: any) {
			appToast.apiError(err, m['issues.archive_all.failed']());
		} finally {
			archiveInFlight = false;
		}
	}

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
			ondragstate={handleDragState}
			{onquickadd}
			sortKey={columnSorts[ts.id] ?? null}
			onsortchange={handleSortChange}
			onarchiveall={handleArchiveAll}
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
				ondragstate={handleDragState}
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
