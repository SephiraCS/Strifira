// Whether a client-side navigation has completed in this SPA session. The
// initial hydration ('enter') does not count: an issue URL loaded directly
// (fresh tab, deep link, reload) has no app page to go back to, so Escape
// must fall back to a normal navigation instead of history.back().
export const inAppNavigation = $state({ happened: false });
