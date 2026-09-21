import type { ViewLayout } from '$lib/types/view';

// Session-scoped memory of the last list/board layout, so returning to a
// view (e.g. browser back from an issue) reopens it in the same layout.
// `overridden` marks an explicit in-session switch; until then pages follow
// the preferred default view from preferences.
export const viewLayoutState = $state({ layout: 'list' as ViewLayout, overridden: false });

export function setSessionViewLayout(layout: ViewLayout) {
	viewLayoutState.layout = layout;
	viewLayoutState.overridden = true;
}
