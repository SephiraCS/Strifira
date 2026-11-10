import { browser } from '$app/environment';
import {
	deleteMailAccount,
	deleteMailDraftAttachment,
	deleteMailMessage,
	getMailDraft,
	getMailMessage,
	listMailAccounts,
	listMailMessages,
	syncMailAccount,
	updateMailDraft,
	updateMailMessage,
	uploadMailDraftAttachments
} from '$lib/api/mail';
import { createMailDraft } from '$lib/api/mail';
import type { MailAccount, MailAttachmentMeta, MailDraft, MailFolder, MailMessageDetail, MailMessageSummary, MailThread } from '$lib/types/mail';

interface MailUpdatedPayload {
	account_id: string;
	folder?: string;
	new_count?: number;
}

export type MailListFilter = 'inbox' | 'sent' | 'starred' | 'archived' | 'spam';

/**
 * Mail state: accounts, the cached message list per folder and the open
 * message. REST loads plus the mail.updated WebSocket event the workspace
 * layout dispatches when the background syncer (or a send) changes mail.
 */
class MailState {
	accounts = $state<MailAccount[]>([]);
	messages = $state<MailMessageSummary[]>([]);
	loading = $state(false);
	loadedFor = $state('');
	selectedAccountId = $state<string | null>(null);
	folder = $state<MailListFilter>('inbox');
	search = $state('');
	selectedMessage = $state<MailMessageDetail | null>(null);
	selecting = $state(false);
	syncing = $state(false);

	private slug = '';
	private loadSeq = 0;
	private reloadTimer: ReturnType<typeof setTimeout> | null = null;
	private searchTimer: ReturnType<typeof setTimeout> | null = null;

	constructor() {
		if (!browser) return;
		window.addEventListener(
			'ws:mail.updated',
			((e: CustomEvent<MailUpdatedPayload>) => this.handleMailUpdated(e.detail)) as EventListener
		);
		window.addEventListener('ws:reconnected', (() => {
			if (this.loadedFor) void this.reload();
		}) as EventListener);
	}

	/** Grouping: one row per conversation, newest message first. */
	threads = $derived.by(() => {
		const byKey = new Map<string, MailThread>();
		for (const message of this.messages) {
			let thread = byKey.get(message.thread_key);
			if (!thread) {
				thread = {
					key: message.thread_key,
					messages: [],
					unreadCount: 0,
					last: message,
					participants: [],
					hasAttachments: false
				};
				byKey.set(message.thread_key, thread);
			}
			thread.messages.push(message);
			if (message.unread) thread.unreadCount++;
			if (message.has_attachments) thread.hasAttachments = true;
			const name = message.from.address || '';
			if (name && !thread.participants.includes(name)) thread.participants.push(name);
		}
		const list = [...byKey.values()];
		for (const thread of list) {
			thread.messages.sort((a, b) => a.sent_at.localeCompare(b.sent_at));
			thread.last = thread.messages[thread.messages.length - 1];
		}
		list.sort((a, b) => b.last.sent_at.localeCompare(a.last.sent_at));
		return list;
	});

	unreadInboxCount = $derived(
		this.folder === 'inbox' ? this.threads.reduce((sum, t) => sum + t.unreadCount, 0) : 0
	);

	async ensureWorkspace(slug: string): Promise<void> {
		if (this.loadedFor === slug && this.accounts.length >= 0 && this.messages.length >= 0) return;
		if (this.loadedFor !== slug) {
			this.selectedAccountId = null;
			this.folder = 'inbox';
			this.search = '';
			this.selectedMessage = null;
		}
		this.slug = slug;
		this.loadedFor = slug;
		await this.reload();
	}

	async reload(): Promise<void> {
		const slug = this.slug;
		if (!slug) return;
		const seq = ++this.loadSeq;
		this.loading = true;
		try {
			const [accounts] = await Promise.all([listMailAccounts(slug)]);
			if (seq !== this.loadSeq) return;
			this.accounts = accounts.accounts;
			if (this.selectedAccountId && !this.accounts.some((a) => a.id === this.selectedAccountId)) {
				this.selectedAccountId = null;
			}
			await this.loadMessages(seq);
		} catch {
			// Keep stale data on transient failures; the toast paths report.
		} finally {
			if (seq === this.loadSeq) this.loading = false;
		}
	}

	private async loadMessages(seq: number): Promise<void> {
		if (!this.slug) return;
		const query = {
			accountId: this.selectedAccountId ?? undefined,
			folder: this.folder,
			q: this.search || undefined,
			limit: 100
		};
		const response = await listMailMessages(this.slug, query);
		if (seq !== this.loadSeq) return;
		this.messages = response.messages;
		// A deleted/filtered selection clears itself quietly.
		if (this.selectedMessage && !this.messages.some((m) => m.id === this.selectedMessage?.id)) {
			this.selectedMessage = null;
		}
	}

	setFolder(folder: MailListFilter): void {
		if (this.folder === folder) return;
		this.folder = folder;
		this.selectedMessage = null;
		void this.loadWithSeq();
	}

	setAccount(accountId: string | null): void {
		if (this.selectedAccountId === accountId) return;
		this.selectedAccountId = accountId;
		this.selectedMessage = null;
		void this.loadWithSeq();
	}

	/** Debounced search: typing never fires a request per keystroke. */
	setSearch(search: string): void {
		this.search = search;
		if (this.searchTimer) clearTimeout(this.searchTimer);
		this.searchTimer = setTimeout(() => void this.loadWithSeq(), 300);
	}

	private async loadWithSeq(): Promise<void> {
		const seq = ++this.loadSeq;
		this.loading = true;
		try {
			await this.loadMessages(seq);
		} finally {
			if (seq === this.loadSeq) this.loading = false;
		}
	}

	async selectMessage(messageId: string): Promise<void> {
		if (!this.slug || this.selecting) return;
		this.selecting = true;
		try {
			const detail = await getMailMessage(this.slug, messageId);
			this.selectedMessage = detail;
			if (detail.unread) {
				this.applyListPatch(messageId, { unread: false });
				void updateMailMessage(this.slug, messageId, { unread: false }).catch(() => {});
			}
		} finally {
			this.selecting = false;
		}
	}

	clearSelection(): void {
		this.selectedMessage = null;
	}

	async toggleStar(messageId: string): Promise<void> {
		const current = this.messages.find((m) => m.id === messageId);
		if (!current || !this.slug) return;
		const starred = !current.starred;
		this.applyListPatch(messageId, { starred });
		try {
			await updateMailMessage(this.slug, messageId, { starred });
		} catch {
			this.applyListPatch(messageId, { starred: !starred });
		}
	}

	async setArchived(messageId: string, archived: boolean): Promise<void> {
		if (!this.slug) return;
		try {
			await updateMailMessage(this.slug, messageId, { archived });
			this.applyListPatch(messageId, { archived });
			if (this.folder !== 'archived' && archived) this.selectedMessage = null;
		} catch {
			// Server state stays authoritative on failure.
		}
	}

	async setSpam(messageId: string, spam: boolean): Promise<void> {
		if (!this.slug) return;
		try {
			await updateMailMessage(this.slug, messageId, { spam });
			this.applyListPatch(messageId, { spam });
			this.selectedMessage = null;
		} catch {
			// Server state stays authoritative on failure.
		}
	}

	async markUnread(messageId: string): Promise<void> {
		if (!this.slug) return;
		try {
			await updateMailMessage(this.slug, messageId, { unread: true });
			this.applyListPatch(messageId, { unread: true });
		} catch {
			// Server state stays authoritative on failure.
		}
	}

	async deleteMessage(messageId: string): Promise<void> {
		if (!this.slug) return;
		try {
			await deleteMailMessage(this.slug, messageId);
			this.messages = this.messages.filter((m) => m.id !== messageId);
			if (this.selectedMessage?.id === messageId) this.selectedMessage = null;
		} catch {
			// Keep the row if the server refused.
		}
	}

	async syncAccount(accountId: string): Promise<void> {
		if (!this.slug) return;
		this.syncing = true;
		try {
			await syncMailAccount(this.slug, accountId);
			await this.reload();
		} finally {
			this.syncing = false;
		}
	}

	async removeAccount(accountId: string): Promise<void> {
		if (!this.slug) return;
		await deleteMailAccount(this.slug, accountId);
		if (this.selectedAccountId === accountId) this.selectedAccountId = null;
		await this.reload();
	}

	private applyListPatch(messageId: string, patch: Partial<MailMessageSummary>): void {
		this.messages = this.messages.map((m) => (m.id === messageId ? { ...m, ...patch } : m));
		if (this.selectedMessage?.id === messageId) {
			this.selectedMessage = { ...this.selectedMessage, ...patch };
		}
	}

	// --- Drafts (autosave + attachments) ---

	/**
	 * Persist a draft via create-then-patch; the compose dialog calls this on
	 * a debounce and on send. The first save mints the draft id, later saves
	 * patch so attachments staged meanwhile survive.
	 */
	async saveDraft(
		slug: string,
		existing: MailDraft | null,
		content: {
			account_id?: string;
			to: { name: string; address: string }[];
			cc: { name: string; address: string }[];
			bcc: { name: string; address: string }[];
			subject: string;
			body: string;
			in_reply_to_message_id?: string;
			forward_from_message_id?: string;
		}
	): Promise<MailDraft> {
		if (existing) {
			return updateMailDraft(slug, existing.id, {
				account_id: content.account_id,
				to: content.to,
				cc: content.cc,
				bcc: content.bcc,
				subject: content.subject,
				body: content.body
			});
		}
		return createMailDraft(slug, content);
	}

	async addDraftAttachments(slug: string, draftId: string, files: File[]): Promise<MailAttachmentMeta[]> {
		const response = await uploadMailDraftAttachments(slug, draftId, files);
		return response.attachments;
	}

	async draftAttachments(slug: string, draftId: string): Promise<MailAttachmentMeta[]> {
		const draft = await getMailDraft(slug, draftId);
		return draft.attachments;
	}

	async removeDraftAttachment(slug: string, draftId: string, attachmentId: string): Promise<void> {
		await deleteMailDraftAttachment(slug, draftId, attachmentId);
	}

	private handleMailUpdated(payload: MailUpdatedPayload): void {
		// Coalesce bursts (a sync can insert dozens) into one reload.
		if (this.reloadTimer) clearTimeout(this.reloadTimer);
		this.reloadTimer = setTimeout(() => {
			void this.reload();
		}, 500);
	}
}

export const mailState = new MailState();
