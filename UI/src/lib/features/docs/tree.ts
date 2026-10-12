import type { DocTreeNode } from '$lib/types/docs';

export type DocTreeEntry = DocTreeNode & { children: DocTreeEntry[] };

/**
 * Assemble sibling-ordered tree entries from the flat document list. Orphans
 * (parent missing or trashed) surface as roots so nothing disappears.
 */
export function buildTree(docs: DocTreeNode[]): DocTreeEntry[] {
	const byParent = new Map<string, DocTreeEntry[]>();
	const roots: DocTreeEntry[] = [];
	const all: DocTreeEntry[] = [];
	const known = new Set(docs.map((d) => d.id));
	const sorted = [...docs].sort(
		(a, b) => a.position - b.position || a.title.localeCompare(b.title)
	);
	for (const doc of sorted) {
		const entry: DocTreeEntry = { ...doc, children: [] };
		all.push(entry);
		if (doc.parent_id && known.has(doc.parent_id)) {
			const list = byParent.get(doc.parent_id) ?? [];
			list.push(entry);
			byParent.set(doc.parent_id, list);
		} else {
			roots.push(entry);
		}
	}
	const attach = (entry: DocTreeEntry, seen: Set<string>) => {
		seen.add(entry.id);
		// Cyclic or already-placed children are cut so the entry tree stays
		// acyclic no matter what parent_ids the data contains.
		entry.children = (byParent.get(entry.id) ?? []).filter((child) => !seen.has(child.id));
		entry.children.forEach((child) => attach(child, seen));
	};
	const seen = new Set<string>();
	roots.forEach((root) => attach(root, seen));
	// Cycles or dangling references can leave entries unreachable from any
	// root; promote them so no document silently disappears from the tree.
	for (const entry of all) {
		if (!seen.has(entry.id)) {
			roots.push(entry);
			attach(entry, seen);
		}
	}
	return roots;
}

/** Path of ancestor entries (including the node itself), oldest first. */
export function breadcrumbOf(
	docs: DocTreeNode[],
	id: string
): DocTreeNode[] {
	const byId = new Map(docs.map((d) => [d.id, d]));
	const chain: DocTreeNode[] = [];
	const guard = new Set<string>();
	let current = byId.get(id);
	while (current && !guard.has(current.id)) {
		guard.add(current.id);
		chain.unshift(current);
		current = current.parent_id ? byId.get(current.parent_id) : undefined;
	}
	return chain;
}
