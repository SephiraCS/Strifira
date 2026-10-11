import { redirect } from '@sveltejs/kit';
import { myIssuesPath } from '$lib/routes';
import type { PageLoad } from './$types';

// GitHub Actions now live on each issue; keep old links working.
export const load: PageLoad = ({ params }) => {
	redirect(307, myIssuesPath(params.workspaceSlug));
};
