import { startMailOAuth } from '$lib/api/mail';

export const mailOAuthStorageKey = 'strifira:mail-oauth';

export async function authorizeMailbox(
	slug: string,
	body: { email_address: string; account_id?: string; display_name?: string; signature?: string }
): Promise<void> {
	const result = await startMailOAuth(slug, body);
	// No credential or code is persisted in browser storage. The server binds
	// the random, single-use state to the current user, workspace and PKCE.
	sessionStorage.setItem(
		mailOAuthStorageKey,
		JSON.stringify({ workspace: slug, state: result.state, email_address: body.email_address })
	);
	window.location.assign(result.authorization_url);
}
