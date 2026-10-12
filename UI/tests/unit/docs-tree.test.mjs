import { test } from 'node:test';
import assert from 'node:assert/strict';
import { positionForIndex, positionAfter, positionBetween } from '../../src/lib/features/docs/position.ts';
import { buildTree, breadcrumbOf } from '../../src/lib/features/docs/tree.ts';

function node(id, parentId, position, title = id) {
	return {
		id,
		parent_id: parentId,
		title,
		icon: undefined,
		position,
		is_template: false,
		updated_at: '2026-01-01T00:00:00Z',
		tags: []
	};
}

test('buildTree nests children under parents in position order', () => {
	const tree = buildTree([
		node('b', undefined, 2048),
		node('a', undefined, 1024),
		node('a1', 'a', 1024),
		node('a2', 'a', 2048)
	]);
	assert.equal(tree.length, 2);
	assert.equal(tree[0].id, 'a');
	assert.deepEqual(tree[0].children.map((c) => c.id), ['a1', 'a2']);
	assert.equal(tree[1].id, 'b');
});

test('buildTree promotes orphans to roots instead of dropping them', () => {
	const tree = buildTree([node('orphan', 'gone', 1), node('root', undefined, 2)]);
	assert.deepEqual(tree.map((e) => e.id).sort(), ['orphan', 'root']);
});

test('buildTree ignores cycles without hanging', () => {
	const tree = buildTree([node('x', 'y', 1), node('y', 'x', 2)]);
	// Neither node is reachable from a root parent; one is promoted and the
	// other stays visible beneath it instead of vanishing.
	const count = (entries) => entries.reduce((n, e) => n + 1 + count(e.children), 0);
	assert.equal(count(tree), 2);
	const selfParent = buildTree([node('s', 's', 1), node('r', undefined, 2)]);
	assert.equal(count(selfParent), 2);
});

test('breadcrumbOf walks ancestors oldest first', () => {
	const docs = [node('root', undefined, 1), node('mid', 'root', 1), node('leaf', 'mid', 1)];
	assert.deepEqual(
		breadcrumbOf(docs, 'leaf').map((d) => d.id),
		['root', 'mid', 'leaf']
	);
	assert.deepEqual(breadcrumbOf(docs, 'root').map((d) => d.id), ['root']);
	assert.deepEqual(breadcrumbOf(docs, 'missing'), []);
});

test('positionAfter appends beyond the last sibling', () => {
	assert.equal(positionAfter(undefined), 1024);
	assert.equal(positionAfter(2048), 3072);
});

test('positionBetween lands strictly between neighbors', () => {
	const mid = positionBetween(1024, 2048);
	assert.ok(mid > 1024 && mid < 2048);
	assert.equal(positionBetween(undefined, 1024), 512);
	assert.equal(positionBetween(2048, undefined), 3072);
});

test('positionForIndex places first, middle, and last', () => {
	const positions = [1024, 2048, 3072];
	const first = positionForIndex(positions, 0);
	const middle = positionForIndex(positions, 2);
	const last = positionForIndex(positions, 3);
	assert.ok(first < 1024);
	// Index 2 sits between the other siblings at 2048 and 3072.
	assert.ok(middle > 2048 && middle < 3072);
	assert.ok(last > 3072);
});
