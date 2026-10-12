/**
 * Fractional sibling ordering. The backend stores one double per document;
 * dropping a page between two siblings is a midpoint, so reordering one
 * page never rewrites its siblings.
 */

export const POSITION_STEP = 1024;

/** Position for a page appended at the end of a sibling list. */
export function positionAfter(last: number | undefined): number {
	return (last ?? 0) + POSITION_STEP;
}

/** Position between two neighbors; a missing upper bound appends. */
export function positionBetween(before: number | undefined, after: number | undefined): number {
	if (after === undefined) return positionAfter(before);
	const lo = before ?? 0;
	const mid = lo + (after - lo) / 2;
	// Doubles have ample precision; guard the degenerate case anyway.
	if (mid <= lo || mid >= after) {
		return after + POSITION_STEP / 2;
	}
	return mid;
}

/**
 * Insert position for `index` within `positions` (sorted sibling positions).
 * Index 0 places the page first; positions.length appends at the end.
 */
export function positionForIndex(positions: number[], index: number): number {
	const sorted = [...positions].sort((a, b) => a - b);
	const clamped = Math.max(0, Math.min(index, sorted.length));
	if (clamped === 0) {
		return sorted.length === 0 ? POSITION_STEP : positionBetween(undefined, sorted[0]);
	}
	if (clamped === sorted.length) {
		return positionAfter(sorted[sorted.length - 1]);
	}
	return positionBetween(sorted[clamped - 1], sorted[clamped]);
}
