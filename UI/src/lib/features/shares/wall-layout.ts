// Pure ordering and focus helpers for the share wall. Keep this module free
// of DOM/Svelte imports so it can be exercised by the node:test unit suite.

/**
 * Sorts tiles by a saved order. Ids missing from the order keep their
 * arrival order after the ordered ones, so a freshly started share never
 * jumps ahead of the user's arrangement.
 */
export function sortTilesByOrder<T extends { id: string }>(tiles: T[], order: string[]): T[] {
	const position = new Map(order.map((id, index) => [id, index]));
	return [...tiles].sort((a, b) => {
		const pa = position.get(a.id);
		const pb = position.get(b.id);
		if (pa !== undefined && pb !== undefined) return pa - pb;
		if (pa !== undefined) return -1;
		if (pb !== undefined) return 1;
		return 0;
	});
}

/**
 * Reconciles a saved order against the live share ids: kept ids stay in
 * their relative order, new ids append in arrival order, ended ids drop.
 */
export function reconcileOrder(order: string[], ids: string[]): string[] {
	const live = new Set(ids);
	const kept = order.filter((id) => live.has(id));
	const seen = new Set(kept);
	return [...kept, ...ids.filter((id) => !seen.has(id))];
}

/**
 * The id `delta` positions away from `current`, wrapping around; the first
 * id when nothing is focused yet, null when there is nothing to focus.
 */
export function nextFocus(ids: string[], current: string | null, delta: number): string | null {
	if (ids.length === 0) return null;
	const index = current ? ids.indexOf(current) : -1;
	if (index === -1) return ids[0];
	const step = delta < 0 ? -1 : 1;
	return ids[(index + step + ids.length) % ids.length];
}

/**
 * Spotlight display order: the focused tile first (it becomes the stage)
 * with the rest keeping the wall order. Falls back to the first tile when
 * the focused id is unknown.
 */
export function spotlightOrder<T extends { id: string }>(tiles: T[], focusedId: string | null): T[] {
	if (tiles.length === 0) return [];
	const focused = tiles.find((tile) => tile.id === focusedId) ?? tiles[0];
	return [focused, ...tiles.filter((tile) => tile !== focused)];
}
