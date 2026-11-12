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
import type {
	MailAccount,
	MailAttachmentMeta,
	MailDraft,
	MailFolder,
	MailMessageDetail,
	MailMessageSummary,
	MailThread
} from '$lib/types/mail';

interface MailUpdatedPayload {
	workspaceSlug?: string;
	account_id: string;
	folder?: string;
	new_count?: number;
	deleted?: boolean;
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
	private listController: AbortController | null = null;
	private inboxUnread = $state(0);
	private pendingRead: { slug: string; id: string } | null = null;
	private loadSeq = 0;
	private selectionSeq = 0;
	private removedAccounts = new Set<string>();
	private reloadTimer: ReturnType<typeof setTimeout> | null = null;
	private searchTimer: ReturnType<typeof setTimeout> | null = null;

	/** Subscriptions belong to the mounted mailbox, never to a previous session. */
	activate(slug: string): () => void {
		const onUpdated = (event: Event) => {
			const payload = (event as CustomEvent<MailUpdatedPayload>).detail;
			if (payload.workspaceSlug && payload.workspaceSlug !== slug) return;
			this.handleMailUpdated(payload);
		};
		const onReconnect = (event: Event) => {
			const workspaceSlug = (event as CustomEvent<{ workspaceSlug?: string }>).detail?.workspaceSlug;
			if (!workspaceSlug || workspaceSlug === slug) this.queueRefresh();
		};
		const onVisible = () => {
			if (document.visibilityState === 'visible') this.queueRefresh();
		};
		window.addEventListener('ws:mail.updated', onUpdated);
		window.addEventListener('ws:reconnected', onReconnect);
		window.addEventListener('online', onVisible);
		document.addEventListener('visibilitychange', onVisible);
		// Reconcile the cache even when an otherwise open socket missed an event.
		const timer = setInterval(onVisible, 45_000);
		void this.ensureWorkspace(slug);
		return () => {
			window.removeEventListener('ws:mail.updated', onUpdated);
			window.removeEventListener('ws:reconnected', onReconnect);
			window.removeEventListener('online', onVisible);
			document.removeEventListener('visibilitychange', onVisible);
			clearInterval(timer);
			this.stop();
		};
	}

	private stop(): void {
		this.listController?.abort();
		if (this.reloadTimer) clearTimeout(this.reloadTimer);
		if (this.searchTimer) clearTimeout(this.searchTimer);
		this.reloadTimer = null;
		this.searchTimer = null;
		this.loadSeq++;
		this.selectionSeq++;
		this.pendingRead = null;
		this.slug = '';
		this.loadedFor = '';
		this.accounts = [];
		this.messages = [];
		this.selectedMessage = null;
		this.inboxUnread = 0;
		this.loading = false;
		this.selecting = false;
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

	unreadInboxCount = $derived(this.inboxUnread);

	async ensureWorkspace(slug: string): Promise<void> {
		if (this.loadedFor === slug && this.accounts.length >= 0 && this.messages.length >= 0) return;
		if (this.loadedFor !== slug) {
			this.loadSeq++;
			this.selectionSeq++;
			this.selecting = false;
			this.listController?.abort();
			this.inboxUnread = 0;
			this.removedAccounts.clear();
			this.accounts = [];
			this.messages = [];
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
		this.listController?.abort();
		const controller = new AbortController();
		this.listController = controller;
		const seq = ++this.loadSeq;
		this.loading = true;
		try {
			const accounts = await listMailAccounts(slug, controller.signal);
			if (seq !== this.loadSeq) return;
			this.accounts = accounts.accounts.filter((a) => !this.removedAccounts.has(a.id));
			if (this.selectedMessage && !this.accounts.some((a) => a.id === this.selectedMessage?.account_id)) {
				this.clearSelection();
			}
			if (this.selectedAccountId && !this.accounts.some((a) => a.id === this.selectedAccountId)) {
				this.selectedAccountId = null;
			}
			await this.loadMessages(seq, controller.signal);
		} catch {
			// Keep stale data on transient failures; the toast paths report.
		} finally {
			if (seq === this.loadSeq) this.loading = false;
		}
	}

	private async loadMessages(seq: number, signal: AbortSignal): Promise<void> {
		if (!this.slug) return;
		const query = {
			accountId: this.selectedAccountId ?? undefined,
			folder: this.folder === 'sent' ? 'sent' : 'inbox',
			starred: this.folder === 'starred' ? true : undefined,
			archived: this.folder === 'archived' ? true : this.folder === 'inbox' ? false : undefined,
			spam: this.folder === 'spam' ? true : ['inbox', 'archived'].includes(this.folder) ? false : undefined,
			q: this.search || undefined,
			limit: 100
		};
		const response = await listMailMessages(this.slug, query, signal);
		if (seq !== this.loadSeq) return;
		this.messages = response.messages.filter((m) => !this.removedAccounts.has(m.account_id));
		this.inboxUnread =
			response.unread_inbox_count ??
			this.messages.filter((m) => m.folder === 'inbox' && m.unread && !m.archived && !m.spam).length;
		// The open message may fall outside the first page or current search.
		// Keep its body and read state until the user changes the selection.
	}

	setFolder(folder: MailListFilter): void {
		if (this.folder === folder) return;
		this.folder = folder;
		this.selectionSeq++;
		this.selecting = false;
		this.selectedMessage = null;
		void this.loadWithSeq();
	}

	setAccount(accountId: string | null): void {
		if (this.selectedAccountId === accountId) return;
		this.selectedAccountId = accountId;
		this.selectionSeq++;
		this.selecting = false;
		this.selectedMessage = null;
		void this.loadWithSeq();
	}

	/** Debounced search: typing never fires a request per keystroke. */
	setSearch(search: string): void {
		this.search = search;
		this.listController?.abort();
		this.loadSeq++;
		this.loading = false;
		if (this.searchTimer) clearTimeout(this.searchTimer);
		this.searchTimer = setTimeout(() => void this.loadWithSeq(), 300);
	}

	private async loadWithSeq(): Promise<void> {
		this.listController?.abort();
		const controller = new AbortController();
		this.listController = controller;
		const seq = ++this.loadSeq;
		this.loading = true;
		try {
			await this.loadMessages(seq, controller.signal);
		} catch {
			// A transient refresh failure keeps the previous mailbox visible.
		} finally {
			if (seq === this.loadSeq) this.loading = false;
		}
	}

	async selectMessage(messageId: string): Promise<void> {
		if (!this.slug || this.selecting) return;
		this.selecting = true;
		const slug = this.slug;
		const seq = ++this.selectionSeq;
		try {
			const detail = await getMailMessage(slug, messageId);
			if (seq !== this.selectionSeq || slug !== this.slug || this.removedAccounts.has(detail.account_id)) return;
			this.selectedMessage = detail;
			if (detail.unread) {
				this.applyListPatch(messageId, { unread: false });
				this.listController?.abort();
				this.loadSeq++;
				this.loading = false;
				const pending = { slug, id: messageId };
				this.pendingRead = pending;
				void updateMailMessage(slug, messageId, { unread: false })
					.catch(() => {})
					.finally(() => {
						if (this.pendingRead !== pending) return;
						this.pendingRead = null;
						if (this.slug === slug) this.queueRefresh();
					});
			}
		} finally {
			if (seq === this.selectionSeq) this.selecting = false;
		}
	}

	clearSelection(): void {
		this.selectionSeq++;
		this.selecting = false;
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
		const slug = this.slug;
		if (!slug || this.removedAccounts.has(accountId)) return;
		this.syncing = true;
		try {
			await syncMailAccount(slug, accountId);
			if (slug === this.slug && !this.removedAccounts.has(accountId)) await this.reload();
		} finally {
			this.syncing = false;
		}
	}

	async removeAccount(accountId: string): Promise<void> {
		if (!this.slug) return;
		const slug = this.slug;
		await deleteMailAccount(slug, accountId);
		if (slug !== this.slug) return;
		this.forgetAccount(accountId);
		await this.reload();
	}

	private forgetAccount(accountId: string): void {
		this.removedAccounts.add(accountId);
		if (this.selectedMessage?.account_id === accountId) this.pendingRead = null;
		// Invalidate list/detail responses issued before the DELETE succeeded.
		this.listController?.abort();
		this.loadSeq++;
		this.selectionSeq++;
		this.selecting = false;
		this.loading = false;
		this.accounts = this.accounts.filter((a) => a.id !== accountId);
		this.messages = this.messages.filter((m) => m.account_id !== accountId);
		if (this.selectedAccountId === accountId) this.selectedAccountId = null;
		if (this.selectedMessage?.account_id === accountId) this.selectedMessage = null;
	}

	private applyListPatch(messageId: string, patch: Partial<MailMessageSummary>): void {
		const current = this.messages.find((m) => m.id === messageId);
		if (current) {
			const counts = (m: MailMessageSummary) => Number(m.folder === 'inbox' && m.unread && !m.archived && !m.spam);
			this.inboxUnread = Math.max(0, this.inboxUnread + counts({ ...current, ...patch }) - counts(current));
		}
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
		if (!this.slug || this.removedAccounts.has(payload.account_id)) return;
		if (payload.deleted) this.forgetAccount(payload.account_id);
		this.queueRefresh();
	}

	private queueRefresh(): void {
		if (!this.slug || this.reloadTimer) return;
		// Bound bursts without postponing refresh indefinitely on a busy inbox.
		this.reloadTimer = setTimeout(() => {
			this.reloadTimer = null;
			if (this.loading || this.pendingRead?.slug === this.slug) this.queueRefresh();
			else void this.reload();
		}, 200);
	}
}

export const mailState = new MailState();
