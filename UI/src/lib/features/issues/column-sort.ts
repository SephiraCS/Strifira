import type { Issue } from '$lib/types/issue';

/** Display-only sort keys for a kanban column. `null` is the board's own
 * manual (sort_order) arrangement; any other key disables drag & drop in
 * that column, like Linear's per-column sorting. */
export type ColumnSortKey = 'priority' | 'due_date' | 'updated' | 'created' | 'title';

export const COLUMN_SORT_KEYS: ColumnSortKey[] = ['priority', 'due_date', 'updated', 'created', 'title'];

function comparePriorityDesc(a: Issue, b: Issue): number {
	return b.priority - a.priority;
}

function compareDueDateAsc(a: Issue, b: Issue): number {
	// Soonest first; issues without a due date always sink to the bottom.
	if (!a.due_date && !b.due_date) return 0;
	if (!a.due_date) return 1;
	if (!b.due_date) return -1;
	return a.due_date.localeCompare(b.due_date);
}

function compareUpdatedDesc(a: Issue, b: Issue): number {
	return b.updated_at.localeCompare(a.updated_at);
}

function compareCreatedDesc(a: Issue, b: Issue): number {
	return b.created_at.localeCompare(a.created_at);
}

function compareTitleAsc(a: Issue, b: Issue): number {
	return a.title.localeCompare(b.title);
}

const COMPARATORS: Record<ColumnSortKey, (a: Issue, b: Issue) => number> = {
	priority: comparePriorityDesc,
	due_date: compareDueDateAsc,
	updated: compareUpdatedDesc,
	created: compareCreatedDesc,
	title: compareTitleAsc
};

/** Sort one column's issues by a sort key, falling back to identifier for a
 * stable order when the key ties. Returns the input array unchanged for an
 * unknown key. */
export function sortColumnIssues(issues: Issue[], key: ColumnSortKey | null): Issue[] {
	if (!key || !(key in COMPARATORS)) return issues;
	const comparator = COMPARATORS[key];
	return [...issues].sort((a, b) => comparator(a, b) || a.identifier.localeCompare(b.identifier));
}
