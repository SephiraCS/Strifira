<script lang="ts">
	import type { Issue, UpdateIssueRequest } from '$lib/types/issue';
	import type { TeamStatus } from '$lib/types/team-status';
	import type { WorkspaceMember } from '$lib/types/workspace';
	import type { Label } from '$lib/types/label';
	import { visibleFlip } from './visible-flip';
	import { pointerDropTarget, type PointerDropTarget } from './pointer-drop';
	import { cubicOut } from 'svelte/easing';
	import KanbanColumn from './KanbanColumn.svelte';
	import { sortColumnIssues, type ColumnSortKey } from './column-sort';
	import { compareBoardOrder, placementSortOrders } from './sort-order';
	import {
		dragHandleZone,
		TRIGGERS,
		SOURCES,
		SHADOW_ITEM_MARKER_PROPERTY_NAME,
		SHADOW_PLACEHOLDER_ITEM_ID,
		type DndEventInfo
	} from 'svelte-dnd-action';
	import { MediaQuery, SvelteSet } from 'svelte/reactivity';
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

	const reducedMotion = new MediaQuery('(prefers-reduced-motion: reduce)', true);
	// Visual motion is independent of the library's faster hit-testing cadence.
	const flipDurationMs = $derived(reducedMotion.current ? 0 : 100);
	const boardId = $props.id();
	const issueDndType = `issue:${boardId}`;
	// Plain coordinates: pointer moves must not invalidate the board's UI.
	let pointerPosition: { issueId: string; x: number; y: number } | null = null;
	let pointerDrag: {
		issueId: string;
		columns: Set<string>;
		captured: boolean;
		target: PointerDropTarget | null;
		observed: { statusId: string; x: number; y: number } | null;
	} | null = null;
	let completedPointerId: string | null = null;
	let destroyed = false;

	function releaseCapture(node: HTMLElement) {
		destroyed = false;
		let gesture: { kind: 'mouse' } | { kind: 'touch'; id: number } | null = null;
		function start(event: MouseEvent | TouchEvent, point: MouseEvent | Touch) {
			if (pointerDrag) return;
			const card = event.target instanceof Element ? event.target.closest<HTMLElement>('[data-issue-card]') : null;
			const zone = card?.parentElement;
			if (!zone?.hasAttribute('data-issue-drop-zone') || !node.contains(zone) || zone.dataset.dragDisabled === 'true')
				return;
			gesture = 'touches' in event ? { kind: 'touch', id: (point as Touch).identifier } : { kind: 'mouse' };
			pointerPosition = { issueId: card!.dataset.issueCard!, x: point.clientX, y: point.clientY };
		}
		function mouseStart(event: MouseEvent) {
			if (event.button === 0 && gesture?.kind !== 'touch') start(event, event);
		}
		function touchStart(event: TouchEvent) {
			if (!gesture && event.touches[0]) start(event, event.touches[0]);
		}
		function move(point: MouseEvent | Touch) {
			if (!pointerPosition) return;
			pointerPosition.x = point.clientX;
			pointerPosition.y = point.clientY;
		}
		function mouseMove(event: MouseEvent) {
			if (gesture?.kind === 'mouse') move(event);
		}
		function ownedTouch(touches: TouchList) {
			if (gesture?.kind !== 'touch') return;
			for (let i = 0; i < touches.length; i++) {
				if (touches[i].identifier === gesture.id) return touches[i];
			}
		}
		function touchMove(event: TouchEvent) {
			const point = ownedTouch(event.touches);
			if (point) move(point);
		}
		function capture(point: MouseEvent | Touch) {
			if (pointerDrag && !pointerDrag.captured) {
				const { observed, issueId } = pointerDrag;
				const unchanged = observed && observed.x === point.clientX && observed.y === point.clientY;
				pointerDrag.captured = true;
				pointerDrag.target = pointerDropTarget(
					node,
					point.clientX,
					point.clientY,
					unchanged
						? {
								statusId: observed.statusId,
								issueId,
								currentIds: new Set(
									currentColumn(observed.statusId)
										.filter((issue) => issue.id !== issueId)
										.map((issue) => issue.id)
								)
							}
						: undefined
				);
				pointerDrag.observed = null;
			}
			gesture = null;
			pointerPosition = null;
		}
		function mouseEnd(event: MouseEvent) {
			if (gesture?.kind === 'mouse' && event.button === 0) capture(event);
		}
		function touchEnd(event: TouchEvent) {
			const point = ownedTouch(event.changedTouches);
			if (point) capture(point);
		}
		function cancel() {
			if (!pointerDrag && !gesture) return;
			gesture = null;
			pointerPosition = null;
			if (pointerDrag) {
				pointerDrag.captured = true;
				pointerDrag.target = null;
				pointerDrag.observed = null;
			}
			// The dependency handles mouseup for both mouse and touch drags, but
			// has no touchcancel listener. Finish its normal cleanup without saving.
			window.dispatchEvent(new MouseEvent('mouseup'));
		}
		function touchCancel(event: TouchEvent) {
			if (gesture?.kind === 'touch' && (event.changedTouches.length === 0 || ownedTouch(event.changedTouches)))
				cancel();
		}
		window.addEventListener('mousemove', mouseMove, { capture: true, passive: true });
		window.addEventListener('touchmove', touchMove, { capture: true, passive: true });
		window.addEventListener('mouseup', mouseEnd, true);
		window.addEventListener('touchend', touchEnd, true);
		window.addEventListener('touchcancel', touchCancel, true);
		node.addEventListener('mousedown', mouseStart, true);
		node.addEventListener('touchstart', touchStart, true);
		return {
			destroy() {
				destroyed = true;
				cancel();
				pointerPosition = null;
				pointerDrag = null;
				completedPointerId = null;
				window.removeEventListener('mousemove', mouseMove, true);
				window.removeEventListener('touchmove', touchMove, true);
				window.removeEventListener('mouseup', mouseEnd, true);
				window.removeEventListener('touchend', touchEnd, true);
				window.removeEventListener('touchcancel', touchCancel, true);
				node.removeEventListener('mousedown', mouseStart, true);
				node.removeEventListener('touchstart', touchStart, true);
			}
		};
	}

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

	// Columns a drag is in progress over: svelte-dnd-action owns their order
	// until the drop, so refreshes must not touch them. Reactive, so a
	// refresh skipped meanwhile applies as soon as the drag lets go.
	const draggingColumns = new SvelteSet<string>();

	function handleDragState(statusId: string, dragging: boolean) {
		if (dragging) draggingColumns.add(statusId);
		else draggingColumns.delete(statusId);
	}

	/** A column's canonical order is sort_order (the server's board order),
	 * not the raw list order: a just-dropped issue takes its new sort_order
	 * optimistically, so the card stays where it was dropped instead of
	 * flashing back to its old spot until the next full refresh. */
	function canonicalColumn(statusId: string): Issue[] {
		return [...(issuesByStatus[statusId] ?? [])].sort(compareBoardOrder);
	}

	function columnSignature(issues: Issue[]): string[] {
		// updated_at is the server's per-issue version (every mutation bumps
		// it), so an unchanged signature means nothing to re-render.
		return issues.map((issue) => `${issue.id}@${issue.updated_at}`);
	}

	function sameSignature(a: string[] | undefined, b: string[]): boolean {
		return a !== undefined && a.length === b.length && a.every((sig, i) => sig === b[i]);
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
		if (!draggingColumns.has(unmappedStatus.id) && !sameSignature(lastUnmapped, stuckSignature)) {
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
		void teamStatusesState.reorder(
			slug,
			columns.map((ts) => ts.id)
		);
	}

	function displayedColumn(statusId: string) {
		return statusId === unmappedStatus.id ? unmapped : (localByStatus[statusId] ?? []);
	}

	function setColumn(statusId: string, items: Issue[]) {
		if (statusId === unmappedStatus.id) unmapped = items;
		else localByStatus[statusId] = items;
	}

	function currentColumn(statusId: string): Issue[] {
		if (statusId !== unmappedStatus.id) {
			return sortColumnIssues(canonicalColumn(statusId), columnSorts[statusId] ?? null);
		}
		const mapped = new Set(columns.map((status) => status.id));
		return Object.entries(issuesByStatus).flatMap(([id, issues]) => (mapped.has(id) ? [] : issues));
	}

	function handleConsider(statusId: string, items: Issue[], info: DndEventInfo) {
		if (info.source === SOURCES.POINTER && info.trigger === TRIGGERS.DRAG_STARTED) {
			const issue = Object.values(issuesByStatus)
				.flat()
				.find((issue) => issue.id === info.id);
			pointerDrag = issue
				? { issueId: issue.id, columns: new Set(), captured: false, target: null, observed: null }
				: null;
			completedPointerId = null;
		}
		if (info.source === SOURCES.POINTER && pointerDrag?.issueId === info.id) {
			pointerDrag.columns.add(statusId);
			const positive =
				info.trigger === TRIGGERS.DRAG_STARTED ||
				info.trigger === TRIGGERS.DRAGGED_ENTERED ||
				info.trigger === TRIGGERS.DRAGGED_OVER_INDEX;
			const hasShadow =
				positive &&
				items.some(
					(issue) =>
						Boolean((issue as Issue & Record<string, unknown>)[SHADOW_ITEM_MARKER_PROPERTY_NAME]) &&
						(issue.id === info.id || issue.id === SHADOW_PLACEHOLDER_ITEM_ID)
				);
			if (positive && hasShadow && pointerPosition?.issueId === info.id) {
				pointerDrag.observed = { statusId, x: pointerPosition.x, y: pointerPosition.y };
			} else if (
				info.trigger === TRIGGERS.DRAGGED_LEFT_ALL ||
				(pointerDrag.observed?.statusId === statusId &&
					(info.trigger === TRIGGERS.DRAGGED_LEFT || info.trigger === TRIGGERS.DRAGGED_ENTERED_ANOTHER))
			) {
				pointerDrag.observed = null;
			}
		}
		setColumn(statusId, items);
	}

	function handleFinalize(statusId: string, items: Issue[], info: DndEventInfo) {
		if (destroyed || (info.source === SOURCES.POINTER && completedPointerId === info.id)) return;
		if (info.source === SOURCES.POINTER && pointerDrag?.issueId === info.id) {
			const drag = pointerDrag;
			pointerDrag = null;
			pointerPosition = null;
			completedPointerId = info.id;
			queueMicrotask(() => {
				if (completedPointerId === info.id) completedPointerId = null;
			});
			const latestIssue = Object.values(issuesByStatus)
				.flat()
				.find((issue) => issue.id === drag.issueId);
			const target = latestIssue ? drag.target : null;
			const clean = (issues: Issue[]) =>
				issues.filter(
					(issue) =>
						issue.id !== drag.issueId && !(issue as Issue & Record<string, unknown>)[SHADOW_ITEM_MARKER_PROPERTY_NAME]
				);
			const destination = target ? clean(currentColumn(target.statusId)) : null;
			for (const id of drag.columns) {
				// A previously visited column may have unlocked and accepted a
				// background refresh. Reconcile against current canonical data,
				// preserving its new cards and issue versions instead of restoring
				// the first-visit snapshot behind the board's signature memo.
				const incoming = target ? clean(currentColumn(id)) : currentColumn(id);
				const displayed = displayedColumn(id);
				const hasShadow = displayed.some((issue) =>
					Boolean((issue as Issue & Record<string, unknown>)[SHADOW_ITEM_MARKER_PROPERTY_NAME])
				);
				if (hasShadow || !sameSignature(columnSignature(displayed), columnSignature(incoming))) {
					setColumn(id, incoming);
				}
			}
			draggingColumns.clear();
			if (!target || !destination) return;
			const before = destination.findIndex((issue) => issue.id === target.beforeId);
			destination.splice(before < 0 ? destination.length : before, 0, latestIssue!);
			setColumn(target.statusId, destination);
			saveDrop(target.statusId, destination, info.id);
			return;
		}
		setColumn(statusId, items);
		// Only the column the card was dropped into saves anything, and only
		// for that card: the source of a cross-column move just lost it, and
		// every other card keeps its sort_order. The unmapped bucket has no
		// real status behind it, so dropping inside it saves nothing.
		if (info.trigger !== TRIGGERS.DROPPED_INTO_ZONE || statusId === unmappedStatus.id) return;
		saveDrop(statusId, items, info.id);
	}

	function saveDrop(statusId: string, items: Issue[], issueId: string) {
		const index = items.findIndex((issue) => issue.id === issueId);
		if (index === -1) return;

		const dropped = items[index];
		const changedColumn = (dropped.status_id ?? dropped.status) !== statusId;
		const unchangedOrder = (a: Issue[], b: Issue[]) =>
			a.length === b.length && a.every((issue, i) => issue.id === b[i].id);
		if (!changedColumn && unchangedOrder(items, canonicalColumn(statusId))) return;

		const saves = [...placementSortOrders(items, index)].map(([id, sortOrder]) => {
			const issue = items.find((it) => it.id === id)!;
			const req: UpdateIssueRequest = { sort_order: sortOrder };
			if (issue === dropped && changedColumn) req.status_id = statusId;
			return issuesState.update(slug, issue.identifier, req);
		});
		// A failed save rolls its card back; report the drop once.
		void Promise.allSettled(saves).then((results) => {
			if (results.some((result) => result.status === 'rejected')) {
				appToast.error(m['common.toast.failed_move_issue']());
			}
		});
	}
</script>

{#snippet boardColumns()}
	{#each columns as ts (ts.id)}
		<div
			class="flex shrink-0"
			aria-label="{ts.name} column"
			animate:visibleFlip={{ duration: flipDurationMs, easing: cubicOut }}
		>
			<KanbanColumn
				statusId={ts.id}
				{issueDndType}
				teamStatus={ts}
				issues={localByStatus[ts.id] ?? []}
				{slug}
				{members}
				{labels}
				statuses={columns}
				{onissueclick}
				onconsider={handleConsider}
				onfinalize={handleFinalize}
				ondragstate={handleDragState}
				{onquickadd}
				sortKey={columnSorts[ts.id] ?? null}
				onsortchange={handleSortChange}
				onarchiveall={handleArchiveAll}
				{flipDurationMs}
			/>
		</div>
	{/each}
	{#if unmapped.length > 0}
		<!-- Read-only bucket: keeps status-orphaned issues visible (and draggable
				out into a real column) instead of dropping them from the board. -->
		<KanbanColumn
			statusId={unmappedStatus.id}
			{issueDndType}
			dropDisabled
			teamStatus={unmappedStatus}
			issues={unmapped}
			{slug}
			{members}
			{labels}
			{onissueclick}
			onconsider={handleConsider}
			onfinalize={handleFinalize}
			ondragstate={handleDragState}
			{flipDurationMs}
		/>
	{/if}
{/snippet}

{#if canReorderColumns}
	<div
		class="flex h-full gap-4 overflow-x-auto p-4"
		use:releaseCapture
		use:dragHandleZone={{
			items: columns,
			type: 'column',
			flipDurationMs: 0,
			morphDisabled: true,
			dropAnimationDisabled: true,
			dropTargetStyle: {}
		}}
		onconsider={handleColumnConsider}
		onfinalize={handleColumnFinalize}
	>
		{@render boardColumns()}
	</div>
{:else}
	<div class="flex h-full gap-4 overflow-x-auto p-4" use:releaseCapture>
		{@render boardColumns()}
	</div>
{/if}
