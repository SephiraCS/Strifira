// Named builders for workspace routes that code navigates to programmatically.
export function myIssuesPath(workspaceSlug: string): string {
	return `/${encodeURIComponent(workspaceSlug)}/my-issues`;
}
