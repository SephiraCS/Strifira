import test from 'node:test';
import assert from 'node:assert/strict';
import { compareBoardOrder, placementSortOrders } from '../../src/lib/features/issues/sort-order.ts';

const column = (...orders) => orders.map((sort_order, i) => ({ id: `i${i}`, sort_order }));
const placed = (sort_order) => ({ id: 'moved', sort_order });

function insert(orders, index, stale = 999_999) {
	const ordered = column(...orders);
	ordered.splice(index, 0, placed(stale));
	return ordered;
}

test('a drop between two neighbours takes their midpoint and changes nothing else', () => {
	assert.deepEqual(placementSortOrders(insert([0, 2000, 3500], 2), 2), new Map([['moved', 2750]]));
});

test('the placed issue\'s own stale sort_order is ignored', () => {
	assert.deepEqual(placementSortOrders(insert([0, 2000], 1, -5), 1), new Map([['moved', 1000]]));
});

test('a drop at either end steps past the outermost issue', () => {
	assert.deepEqual(placementSortOrders(insert([0, 2000], 0), 0), new Map([['moved', -1000]]));
	assert.deepEqual(placementSortOrders(insert([0, 2000], 2), 2), new Map([['moved', 3000]]));
});

test('a drop into an empty column starts at zero', () => {
	assert.deepEqual(placementSortOrders([placed(42)], 0), new Map([['moved', 0]]));
});

test('a drop between equal sort_orders renumbers the column in display order', () => {
	const ordered = insert([1000, 1000, 1000, 1000], 2);
	const changes = placementSortOrders(ordered, 2);
	const result = ordered.map((issue) => changes.get(issue.id) ?? issue.sort_order);
	assert.deepEqual(result, [1000, 2000, 3000, 4000, 5000]);
	// The first issue already sits at the start value and is left alone.
	assert.equal(changes.has('i0'), false);
	assert.equal(changes.get('moved'), 3000);
});

test('a gap halved down to float precision renumbers instead of tying', () => {
	const low = 1;
	const high = low + Number.EPSILON;
	const ordered = insert([low, high], 1);
	const changes = placementSortOrders(ordered, 1);
	const result = ordered.map((issue) => changes.get(issue.id) ?? issue.sort_order);
	for (let i = 1; i < result.length; i++) assert.ok(result[i - 1] < result[i], `${result} is strictly increasing`);
});

test('board order breaks sort_order ties like the server: newest first, then id descending', () => {
	const issues = [
		{ id: 'a', sort_order: 5, created_at: '2026-01-01T00:00:00.1Z' },
		{ id: 'b', sort_order: 5, created_at: '2026-01-01T00:00:00.12Z' },
		{ id: 'c', sort_order: 5, created_at: '2026-01-01T00:00:00.12Z' },
		{ id: 'd', sort_order: 1, created_at: '2025-01-01T00:00:00Z' }
	];
	// 00.12 is newer than 00.1 even though it sorts lower as a string.
	assert.deepEqual(issues.sort(compareBoardOrder).map((issue) => issue.id), ['d', 'c', 'b', 'a']);
});
