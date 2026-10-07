// Whether the current history entry has a known previous app entry. The
// workspace layout tracks this in page.state, including back/forward and
// shallow navigation. Fresh loads and replacements of the initial entry
// must fall back to normal navigation instead of history.back().
export const inAppNavigation = $state({ happened: false });
