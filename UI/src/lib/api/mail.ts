import { api } from './client';
import type {
	MailAccount,
	MailAccountListResponse,
	MailAttachmentMeta,
	MailDraft,
	MailDraftListResponse,
	MailMessageDetail,
	MailMessageListResponse
} from '$lib/types/mail';

export function listMailAccounts(slug: string): Promise<MailAccountListResponse> {
	return api.get<MailAccountListResponse>(`/api/workspaces/${slug}/mail/accounts`);
}

export interface MailAccountInput {
	email_address: string;
	display_name?: string;
	password?: string;
	signature?: string;
}

export interface MailDiscovery {
	oauth_provider?: 'google' | 'microsoft';
	email_address: string;
	provider: string;
	source: string;
	authentication: 'password' | 'app_password' | 'oauth2';
	help_url?: string;
	authorization_url?: string;
	token_url?: string;
	incoming: { host: string; port: number; security: 'tls' | 'starttls'; username: string };
	outgoing: { host: string; port: number; security: 'tls' | 'starttls'; username: string };
}

export function discoverMailAccount(slug: string, emailAddress: string): Promise<MailDiscovery> {
	return api.post<MailDiscovery>(`/api/workspaces/${slug}/mail/accounts/discover`, { email_address: emailAddress });
}

export function startMailOAuth(
	slug: string,
	body: { email_address: string; account_id?: string; display_name?: string; signature?: string }
): Promise<{ authorization_url: string; state: string }> {
	return api.post(`/api/workspaces/${slug}/mail/oauth/start`, body);
}

export function completeMailOAuth(
	slug: string,
	body: { state: string; code: string; provider_error: string }
): Promise<MailAccount> {
	return api.post(`/api/workspaces/${slug}/mail/oauth/complete`, body);
}

export function createMailAccount(slug: string, body: MailAccountInput): Promise<MailAccount> {
	return api.post<MailAccount>(`/api/workspaces/${slug}/mail/accounts`, body);
}

export function updateMailAccount(
	slug: string,
	accountId: string,
	body: Partial<MailAccountInput>
): Promise<MailAccount> {
	return api.patch<MailAccount>(`/api/workspaces/${slug}/mail/accounts/${accountId}`, body);
}

export function deleteMailAccount(slug: string, accountId: string): Promise<{ deleted: boolean }> {
	return api.delete<{ deleted: boolean }>(`/api/workspaces/${slug}/mail/accounts/${accountId}`);
}

export function syncMailAccount(slug: string, accountId: string): Promise<{ synced: boolean }> {
	return api.post<{ synced: boolean }>(`/api/workspaces/${slug}/mail/accounts/${accountId}/sync`, {});
}

export interface MailListQuery {
	accountId?: string;
	folder?: string;
	q?: string;
	unread?: boolean;
	starred?: boolean;
	archived?: boolean;
	spam?: boolean;
	limit?: number;
	offset?: number;
}

function mailListQueryString(query: MailListQuery): string {
	const params = new URLSearchParams();
	if (query.accountId) params.set('accountId', query.accountId);
	if (query.folder) params.set('folder', query.folder);
	if (query.q) params.set('q', query.q);
	if (query.unread !== undefined) params.set('unread', String(query.unread));
	if (query.starred !== undefined) params.set('starred', String(query.starred));
	if (query.archived !== undefined) params.set('archived', String(query.archived));
	if (query.spam !== undefined) params.set('spam', String(query.spam));
	if (query.limit) params.set('limit', String(query.limit));
	if (query.offset) params.set('offset', String(query.offset));
	const encoded = params.toString();
	return encoded ? `?${encoded}` : '';
}

export function listMailMessages(slug: string, query: MailListQuery): Promise<MailMessageListResponse> {
	return api.get<MailMessageListResponse>(`/api/workspaces/${slug}/mail/messages${mailListQueryString(query)}`);
}

export function getMailMessage(slug: string, messageId: string): Promise<MailMessageDetail> {
	return api.get<MailMessageDetail>(`/api/workspaces/${slug}/mail/messages/${messageId}`);
}

export function updateMailMessage(
	slug: string,
	messageId: string,
	body: { unread?: boolean; starred?: boolean; archived?: boolean; spam?: boolean }
): Promise<MailMessageDetail> {
	return api.patch<MailMessageDetail>(`/api/workspaces/${slug}/mail/messages/${messageId}`, body);
}

export function deleteMailMessage(slug: string, messageId: string): Promise<{ deleted: boolean }> {
	return api.delete<{ deleted: boolean }>(`/api/workspaces/${slug}/mail/messages/${messageId}`);
}

export function mailMessageAttachmentUrl(slug: string, attachmentId: string): string {
	return `/api/workspaces/${slug}/mail/attachments/${attachmentId}`;
}

export function downloadMailAttachment(
	slug: string,
	attachmentId: string
): Promise<{ blob: Blob; filename: string | null }> {
	return api.blob(mailMessageAttachmentUrl(slug, attachmentId));
}

export function listMailDrafts(slug: string): Promise<MailDraftListResponse> {
	return api.get<MailDraftListResponse>(`/api/workspaces/${slug}/mail/drafts`);
}

export interface MailDraftInput {
	account_id?: string;
	to?: { name: string; address: string }[];
	cc?: { name: string; address: string }[];
	bcc?: { name: string; address: string }[];
	subject?: string;
	body?: string;
	in_reply_to_message_id?: string;
	forward_from_message_id?: string;
}

export function createMailDraft(slug: string, body: MailDraftInput): Promise<MailDraft> {
	return api.post<MailDraft>(`/api/workspaces/${slug}/mail/drafts`, body);
}

export function updateMailDraft(slug: string, draftId: string, body: Partial<MailDraftInput>): Promise<MailDraft> {
	return api.patch<MailDraft>(`/api/workspaces/${slug}/mail/drafts/${draftId}`, body);
}

export function getMailDraft(slug: string, draftId: string): Promise<MailDraft> {
	return api.get<MailDraft>(`/api/workspaces/${slug}/mail/drafts/${draftId}`);
}

export function deleteMailDraft(slug: string, draftId: string): Promise<{ deleted: boolean }> {
	return api.delete<{ deleted: boolean }>(`/api/workspaces/${slug}/mail/drafts/${draftId}`);
}

export function sendMailDraft(slug: string, draftId: string): Promise<{ message_id: string }> {
	return api.post<{ message_id: string }>(`/api/workspaces/${slug}/mail/drafts/${draftId}/send`, {});
}

export function uploadMailDraftAttachments(
	slug: string,
	draftId: string,
	files: File[]
): Promise<{ attachments: MailAttachmentMeta[] }> {
	const form = new FormData();
	for (const file of files) {
		form.append('attachments', file);
	}
	return api.postForm<{ attachments: MailAttachmentMeta[] }>(
		`/api/workspaces/${slug}/mail/drafts/${draftId}/attachments`,
		form
	);
}

export function deleteMailDraftAttachment(
	slug: string,
	draftId: string,
	attachmentId: string
): Promise<{ deleted: boolean }> {
	return api.delete<{ deleted: boolean }>(`/api/workspaces/${slug}/mail/drafts/${draftId}/attachments/${attachmentId}`);
}
