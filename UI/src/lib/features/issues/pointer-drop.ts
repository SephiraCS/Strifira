import { DRAGGED_ELEMENT_ID, SHADOW_PLACEHOLDER_ITEM_ID } from 'svelte-dnd-action';

export interface PointerDropTarget {
	statusId: string;
	beforeId: string | null;
}

interface ObservedDropSlot {
	statusId: string;
	issueId: string;
	currentIds: ReadonlySet<string>;
}

/** Resolve the final visible destination once, independently of hover polling. */
export function pointerDropTarget(
	board: HTMLElement,
	x: number,
	y: number,
	observed?: ObservedDropSlot
): PointerDropTarget | null {
	const hits = document.elementsFromPoint(x, y).filter((element) => !element.closest(`#${DRAGGED_ELEMENT_ID}`));
	const zone = hits[0]?.closest<HTMLElement>('[data-issue-drop-zone]');
	if (!zone || !board.contains(zone) || zone.dataset.dropDisabled === 'true') return null;
	const children = Array.from(zone.children).filter(
		(element): element is HTMLElement => element instanceof HTMLElement
	);
	if (observed && observed.statusId === zone.dataset.issueDropZone) {
		const shadow = children.findIndex(
			(element) =>
				element.dataset.issueShadow === 'true' &&
				(element.dataset.issueCard === observed.issueId || element.dataset.issueCard === SHADOW_PLACEHOLDER_ITEM_ID)
		);
		if (shadow >= 0) {
			// With an unchanged pointer, scrolling or FLIP can move a real card
			// under the cursor without changing the displayed insertion slot.
			// Skip successors deleted or moved by a background refresh.
			const anchor = children
				.slice(shadow + 1)
				.find(
					(element) => element.dataset.issueShadow !== 'true' && observed.currentIds.has(element.dataset.issueCard!)
				);
			return { statusId: observed.statusId, beforeId: anchor?.dataset.issueCard ?? null };
		}
	}
	let child = hits[0]?.closest<HTMLElement>('[data-issue-card]') ?? undefined;
	if (child?.parentElement !== zone) child = undefined;
	if (!child && children.length > 0) {
		// Match the visible nearest card for gaps, reading geometry only at
		// release. Hovering below the final card appends to the column.
		let distance = Number.POSITIVE_INFINITY;
		for (const element of children) {
			const rect = element.getBoundingClientRect();
			if (element === children.at(-1) && y > rect.bottom) {
				child = undefined;
				break;
			}
			const next = Math.hypot(x - (rect.left + rect.right) / 2, y - (rect.top + rect.bottom) / 2);
			if (next < distance) {
				distance = next;
				child = element;
			}
		}
	}
	const index = child ? children.indexOf(child) : children.length;
	const anchor = children.slice(index).find((element) => element.dataset.issueShadow !== 'true');
	return { statusId: zone.dataset.issueDropZone!, beforeId: anchor?.dataset.issueCard ?? null };
}
