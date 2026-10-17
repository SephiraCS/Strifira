import assert from 'node:assert/strict';
import test from 'node:test';
import {
	nextFocus,
	reconcileOrder,
	sortTilesByOrder,
	spotlightOrder
} from '../../src/lib/features/shares/wall-layout.ts';

function tile(id, extra = {}) {
	return { id, ...extra };
}

test('sortTilesByOrder orders known ids and appends unknowns in arrival order', () => {
	const tiles = [tile('c'), tile('a'), tile('d'), tile('b')];
	assert.deepEqual(
		sortTilesByOrder(tiles, ['b', 'a']).map((t) => t.id),
		['b', 'a', 'c', 'd']
	);
	// An empty order keeps the arrival order.
	assert.deepEqual(
		sortTilesByOrder(tiles, []).map((t) => t.id),
		['c', 'a', 'd', 'b']
	);
});

test('sortTilesByOrder does not mutate the input', () => {
	const tiles = [tile('b'), tile('a')];
	sortTilesByOrder(tiles, ['a', 'b']);
	assert.deepEqual(tiles.map((t) => t.id), ['b', 'a']);
});

test('reconcileOrder keeps saved relative order, appends new and drops ended ids', () => {
	assert.deepEqual(reconcileOrder(['a', 'b', 'c'], ['c', 'x', 'a']), ['a', 'c', 'x']);
	assert.deepEqual(reconcileOrder([], ['a', 'b']), ['a', 'b']);
	assert.deepEqual(reconcileOrder(['a', 'b'], []), []);
	// Duplicates in the live ids never leak into the order.
	assert.deepEqual(reconcileOrder(['a'], ['a', 'a']), ['a']);
});

test('nextFocus cycles with wrap-around in both directions', () => {
	const ids = ['a', 'b', 'c'];
	assert.equal(nextFocus(ids, 'a', 1), 'b');
	assert.equal(nextFocus(ids, 'c', 1), 'a');
	assert.equal(nextFocus(ids, 'a', -1), 'c');
	assert.equal(nextFocus(ids, 'b', -1), 'a');
});

test('nextFocus starts at the first id and yields null with nothing to focus', () => {
	assert.equal(nextFocus(['a', 'b'], null, 1), 'a');
	assert.equal(nextFocus(['a', 'b'], 'gone', -1), 'a');
	assert.equal(nextFocus([], 'a', 1), null);
	assert.equal(nextFocus([], null, 1), null);
});

test('spotlightOrder puts the focused tile first and keeps the wall order after it', () => {
	const tiles = [tile('a'), tile('b'), tile('c')];
	assert.deepEqual(
		spotlightOrder(tiles, 'b').map((t) => t.id),
		['b', 'a', 'c']
	);
	// Unknown focus falls back to the first tile.
	assert.deepEqual(
		spotlightOrder(tiles, 'gone').map((t) => t.id),
		['a', 'b', 'c']
	);
	assert.deepEqual(spotlightOrder([], 'a'), []);
	assert.deepEqual(
		spotlightOrder([tile('a')], 'a').map((t) => t.id),
		['a']
	);
});
