import { redirect } from '@sveltejs/kit';
import type { PageLoad } from './$types';

// The board is a layout of the team page (ViewSwitcher), so old /board links
// land on the team page instead of rendering an empty page first.
export const load: PageLoad = ({ params }) => {
	redirect(307, `/${params.workspaceSlug}/teams/${params.teamId}`);
};
