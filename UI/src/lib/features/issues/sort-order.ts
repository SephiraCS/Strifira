/** Gap left between issues when a column is renumbered or an issue lands at
 * either end. */
const STEP = 1000;

type Ordered = { id: string; sort_order: number };

/**
 * The sort_order changes that place `ordered[index]` where it was dropped.
 * `ordered` is the column in its new display order, the placed issue
 * included (its own sort_order is stale and ignored).
 *
 * Normally only the placed issue changes: it takes the midpoint of its
 * neighbours. When they leave no room between them (equal values, or a gap
 * already halved down to float precision), a midpoint would tie with a
 * neighbour and the server's tie-break would move the card after the next
 * refresh, so the column is renumbered in its display order instead.
 */
export function placementSortOrders(ordered: Ordered[], index: number): Map<string, number> {
	const placed = ordered[index];
	const prev = ordered[index - 1]?.sort_order;
	const next = ordered[index + 1]?.sort_order;

	if (prev === undefined && next === undefined) return new Map([[placed.id, 0]]);
	if (prev === undefined) return new Map([[placed.id, next! - STEP]]);
	if (next === undefined) return new Map([[placed.id, prev + STEP]]);

	const middle = (prev + next) / 2;
	if (prev < middle && middle < next) return new Map([[placed.id, middle]]);

	// The placed issue is never first here (it has a previous neighbour).
	const start = ordered[0].sort_order;
	const changes = new Map<string, number>();
	ordered.forEach((issue, position) => {
		const sortOrder = start + position * STEP;
		if (issue === placed || issue.sort_order !== sortOrder) changes.set(issue.id, sortOrder);
	});
	return changes;
}

type Placed = Ordered & { created_at: string };

/** The list API's board order: sort_order, then newest first, then id
 * descending (BE/internal/repository/issue.go). Applying the same
 * tie-break keeps optimistic updates from reshuffling equal sort_orders. */
export function compareBoardOrder(a: Placed, b: Placed): number {
	return (
		a.sort_order - b.sort_order ||
		Date.parse(b.created_at) - Date.parse(a.created_at) ||
		(a.id < b.id ? 1 : a.id > b.id ? -1 : 0)
	);
}
