export interface MailAddress {
	name: string;
	address: string;
}

export interface MailAccount {
	id: string;
	email_address: string;
	display_name: string;
	imap_host: string;
	imap_port: number;
	imap_security: 'none' | 'starttls' | 'tls';
	smtp_host: string;
	smtp_port: number;
	smtp_security: 'none' | 'starttls' | 'tls';
	username: string;
	signature: string;
	last_synced_at?: string | null;
	last_sync_error: string;
	created_at: string;
}

export interface MailAccountListResponse {
	accounts: MailAccount[];
}

export type MailFolder = 'inbox' | 'sent' | 'starred' | 'archived' | 'spam';

export interface MailMessageSummary {
	id: string;
	account_id: string;
	folder: 'inbox' | 'sent';
	thread_key: string;
	subject: string;
	from: MailAddress;
	to: MailAddress[];
	cc?: MailAddress[];
	snippet: string;
	unread: boolean;
	starred: boolean;
	archived: boolean;
	spam: boolean;
	has_attachments: boolean;
	sent_at: string;
}

export interface MailMessageListResponse {
	messages: MailMessageSummary[];
}

export interface MailAttachmentMeta {
	id: string;
	message_id?: string;
	draft_id?: string;
	filename: string;
	content_type: string;
	size_bytes: number;
}

export interface MailMessageDetail extends MailMessageSummary {
	reply_to: string;
	body_text: string;
	body_html: string;
	in_reply_to: string;
	attachments: MailAttachmentMeta[];
}

export interface MailDraft {
	id: string;
	account_id?: string;
	to: MailAddress[];
	cc?: MailAddress[];
	bcc?: MailAddress[];
	subject: string;
	body: string;
	in_reply_to_message_id?: string;
	forward_from_message_id?: string;
	attachments: MailAttachmentMeta[];
	updated_at: string;
}

export interface MailDraftListResponse {
	drafts: MailDraft[];
}

export interface MailThread {
	key: string;
	messages: MailMessageSummary[];
	unreadCount: number;
	last: MailMessageSummary;
	participants: string[];
	hasAttachments: boolean;
}
