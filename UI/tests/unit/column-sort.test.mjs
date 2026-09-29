import test from 'node:test';
import assert from 'node:assert/strict';
import { sortColumnIssues, COLUMN_SORT_KEYS } from '../../src/lib/features/issues/column-sort.ts';

const base = {
	description: null,
	team_id: 't1',
	project_id: null,
	cycle_id: null,
	creator_id: 'u1',
	assignee_id: null,
	parent_id: null,
	due_date: null,
	sort_order: 0,
	labels: [],
	assignees: []
};

function issue(id, overrides = {}) {
	return {
		...base,
		id,
		identifier: `DSH-${id}`,
		title: `Issue ${id}`,
		priority: 0,
		status: 'done',
		created_at: '2026-01-01T00:00:00Z',
		updated_at: '2026-01-01T00:00:00Z',
		...overrides
	};
}

test('exposes exactly the five sort keys', () => {
	assert.deepEqual(COLUMN_SORT_KEYS, ['priority', 'due_date', 'updated', 'created', 'title']);
});

test('priority sorts urgent first and none last', () => {
	const sorted = sortColumnIssues(
		[issue('1', { priority: 0 }), issue('2', { priority: 4 }), issue('3', { priority: 2 })],
		'priority'
	);
	assert.deepEqual(
		sorted.map((i) => i.identifier),
		['DSH-2', 'DSH-3', 'DSH-1']
	);
});

test('due date sorts soonest first with undated issues last', () => {
	const sorted = sortColumnIssues(
		[
			issue('1', { due_date: null }),
			issue('2', { due_date: '2026-03-01' }),
			issue('3', { due_date: '2026-02-01' }),
			issue('4', { due_date: null })
		],
		'due_date'
	);
	assert.deepEqual(
		sorted.map((i) => i.identifier),
		['DSH-3', 'DSH-2', 'DSH-1', 'DSH-4']
	);
});

test('updated and created sort newest first', () => {
	const items = [
		issue('1', { updated_at: '2026-01-01T00:00:00Z', created_at: '2026-01-03T00:00:00Z' }),
		issue('2', { updated_at: '2026-03-01T00:00:00Z', created_at: '2026-01-01T00:00:00Z' })
	];
	assert.deepEqual(
		sortColumnIssues(items, 'updated').map((i) => i.identifier),
		['DSH-2', 'DSH-1']
	);
	assert.deepEqual(
		sortColumnIssues(items, 'created').map((i) => i.identifier),
		['DSH-1', 'DSH-2']
	);
});

test('title sorts A to Z', () => {
	const sorted = sortColumnIssues([issue('b'), issue('a'), issue('c')], 'title');
	assert.deepEqual(
		sorted.map((i) => i.title),
		['Issue a', 'Issue b', 'Issue c']
	);
});

test('manual (null) and unknown keys keep the input order', () => {
	const items = [issue('2'), issue('1')];
	assert.equal(sortColumnIssues(items, null), items);
	assert.equal(sortColumnIssues(items, 'nonsense'), items);
});

test('identifier breaks ties so the order is stable', () => {
	const sorted = sortColumnIssues(
		[issue('9', { priority: 3 }), issue('2', { priority: 3 }), issue('7', { priority: 3 })],
		'priority'
	);
	assert.deepEqual(
		sorted.map((i) => i.identifier),
		['DSH-2', 'DSH-7', 'DSH-9']
	);
});

test('sorting does not mutate the input array', () => {
	const items = [issue('2', { priority: 1 }), issue('1', { priority: 4 })];
	sortColumnIssues(items, 'priority');
	assert.deepEqual(
		items.map((i) => i.identifier),
		['DSH-2', 'DSH-1']
	);
});
