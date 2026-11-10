import { browser } from '$app/environment';
import {
	createChatChannel,
	createChatMessage,
	deleteChatMessage,
	ensureChatDM,
	getChatPresence,
	listChatChannels,
	listChatMessages,
	updateChatMessage
} from '$lib/api/chat';
import { authState } from '$lib/features/auth/auth.state.svelte';
import type { ChatChannel, ChatMessage } from '$lib/types/chat';
import type { WorkspaceMember } from '$lib/types/workspace';

const TYPING_TTL_MS = 6000;
const TYPING_SEND_THROTTLE_MS = 3000;
// Messages from the same author within this window collapse into one group.
export const MESSAGE_GROUP_WINDOW_MS = 5 * 60 * 1000;

interface MessageCreatedPayload {
	channel_id: string;
	message_id: string;
	message: ChatMessage;
}

interface MessageUpdatedPayload {
	channel_id: string;
	message_id: string;
	message: ChatMessage;
}

interface MessageDeletedPayload {
	channel_id: string;
	message_id: string;
}

interface TypingPayload {
	channel_id: string;
	user_id: string;
}

/**
 * Chat state: channels, timelines, unread counts, typing hints and the
 * online set. REST loads plus the chat.* / presence.* WebSocket events the
 * workspace layout dispatches; every mutation is idempotent because the
 * sender also receives (or applies locally) its own event.
 */
class ChatState {
	channels = $state<ChatChannel[]>([]);
	loading = $state(false);
	loadedFor = $state('');
	activeChannelId = $state<string | null>(null);
	messages = $state<Map<string, ChatMessage[]>>(new Map());
	hasMore = $state<Map<string, boolean>>(new Map());
	unread = $state<Map<string, number>>(new Map());
	typing = $state<Map<string, Map<string, number>>>(new Map());
	onlineUsers = $state<Set<string>>(new Set());
	members = $state<WorkspaceMember[]>([]);

	private slug = '';
	private loadSeq = 0;
	private channelSeq = 0;
	private loadInFlight: Promise<void> | null = null;
	private lastTypingSent = 0;
	private typingPruneTimer: ReturnType<typeof setInterval> | null = null;

	constructor() {
		if (!browser) return;
		window.addEventListener('ws:chat.message.created', ((e: CustomEvent<MessageCreatedPayload>) =>
			this.handleMessageCreated(e.detail)) as EventListener);
		window.addEventListener('ws:chat.message.updated', ((e: CustomEvent<MessageUpdatedPayload>) =>
			this.handleMessageUpdated(e.detail)) as EventListener);
		window.addEventListener('ws:chat.message.deleted', ((e: CustomEvent<MessageDeletedPayload>) =>
			this.handleMessageDeleted(e.detail)) as EventListener);
		window.addEventListener('ws:chat.typing', ((e: CustomEvent<TypingPayload>) =>
			this.handleTyping(e.detail)) as EventListener);
		window.addEventListener('ws:presence.online', ((e: CustomEvent<{ user_id: string }>) =>
			this.handlePresenceOnline(e.detail)) as EventListener);
		window.addEventListener('ws:presence.offline', ((e: CustomEvent<{ user_id: string }>) =>
			this.handlePresenceOffline(e.detail)) as EventListener);
		window.addEventListener('ws:reconnected', () => {
			// Events missed while the socket was down are reconciled with full
			// reloads rather than trying to replay a gap.
			if (this.slug) {
				void this.reload();
				void this.refreshPresence();
			}
		});
	}

	get unreadTotal(): number {
		let total = 0;
		for (const count of this.unread.values()) total += count;
		return total;
	}

	channelById(id: string | null): ChatChannel | undefined {
		if (!id) return undefined;
		return this.channels.find((c) => c.id === id);
	}

	memberName(userId: string): string {
		const member = this.members.find((m) => m.user_id === userId);
		return member?.name ?? '';
	}

	/** Loads the workspace's channels (and the online snapshot) once per slug. */
	async ensureWorkspace(slug: string) {
		if (!slug || this.loadedFor === slug) return;
		if (this.slug === slug && this.loadInFlight) return this.loadInFlight;
		this.slug = slug;
		this.loadInFlight = this.reload().finally(() => {
			if (this.slug === slug) this.loadInFlight = null;
		});
		return this.loadInFlight;
	}

	async reload() {
		if (!this.slug) return;
		const seq = ++this.loadSeq;
		const slug = this.slug;
		this.loading = !this.loadedFor;
		try {
			const [list, presence] = await Promise.all([
				listChatChannels(slug),
				this.loadedFor ? Promise.resolve(null) : getChatPresence(slug).catch(() => null)
			]);
			if (seq !== this.loadSeq || slug !== this.slug) return;
			this.channels = list.channels;
			if (presence) this.onlineUsers = new Set(presence.online);
			this.loadedFor = slug;
		} catch {
			// Keep whatever we had; navigation or the next WS event retries.
		} finally {
			if (seq === this.loadSeq) this.loading = false;
		}
	}

	async refreshPresence() {
		if (!this.slug) return;
		try {
			const presence = await getChatPresence(this.slug);
			this.onlineUsers = new Set(presence.online);
		} catch {
			// Transient; the next presence event or reconnect refreshes.
		}
	}

	/** Opens a channel: marks it read and loads its timeline if needed. */
	async openChannel(channelId: string) {
		if (!this.slug) return;
		this.activeChannelId = channelId;
		if (this.unread.has(channelId)) {
			const next = new Map(this.unread);
			next.delete(channelId);
			this.unread = next;
		}
		if (!this.messages.has(channelId)) {
			await this.loadMessages(channelId);
		}
	}

	async loadMessages(channelId: string) {
		const seq = ++this.channelSeq;
		try {
			const page = await listChatMessages(this.slug, channelId);
			if (seq !== this.channelSeq || this.activeChannelId !== channelId) return;
			this.messages = new Map(this.messages).set(channelId, page.messages);
			this.hasMore = new Map(this.hasMore).set(channelId, page.has_more);
		} catch {
			// The channel page renders the error empty state.
		}
	}

	/** Paginates backwards; returns true when older messages were prepended. */
	async loadOlder(channelId: string): Promise<boolean> {
		const current = this.messages.get(channelId);
		if (!current || current.length === 0 || !this.hasMore.get(channelId)) return false;
		try {
			const page = await listChatMessages(this.slug, channelId, current[0].id);
			if (page.messages.length === 0) {
				this.hasMore = new Map(this.hasMore).set(channelId, false);
				return false;
			}
			const merged = [...page.messages, ...current];
			this.messages = new Map(this.messages).set(channelId, merged);
			this.hasMore = new Map(this.hasMore).set(channelId, page.has_more);
			return true;
		} catch {
			return false;
		}
	}

	async send(channelId: string, body: string): Promise<boolean> {
		const text = body.trim();
		if (!text || !this.slug) return false;
		try {
			const message = await createChatMessage(this.slug, channelId, text);
			// The workspace-wide broadcast also reaches this browser; insert is
			// idempotent, so this only covers the targeted (DM/private) path.
			this.insertMessage(channelId, message);
			return true;
		} catch {
			return false;
		}
	}

	async edit(channelId: string, messageId: string, body: string): Promise<boolean> {
		const text = body.trim();
		if (!text || !this.slug) return false;
		try {
			const updated = await updateChatMessage(this.slug, messageId, text);
			this.handleMessageUpdated({
				channel_id: channelId,
				message_id: messageId,
				message: updated
			});
			return true;
		} catch {
			return false;
		}
	}

	async remove(channelId: string, messageId: string): Promise<boolean> {
		if (!this.slug) return false;
		try {
			await deleteChatMessage(this.slug, messageId);
			this.dropMessage(channelId, messageId);
			return true;
		} catch {
			return false;
		}
	}

	async createChannel(name: string, type: 'public' | 'private'): Promise<ChatChannel | null> {
		if (!this.slug) return null;
		try {
			const channel = await createChatChannel(this.slug, { name, type });
			await this.reload();
			return channel;
		} catch {
			return null;
		}
	}

	/** Returns the DM channel's id, creating it on first use. */
	async startDM(userId: string): Promise<string | null> {
		if (!this.slug) return null;
		try {
			const channel = await ensureChatDM(this.slug, userId);
			await this.reload();
			return channel.id;
		} catch {
			return null;
		}
	}

	setMembers(members: WorkspaceMember[]) {
		this.members = members;
	}

	/** Throttled typing hint for the channel the composer is in. */
	sendTyping(channelId: string) {
		const now = Date.now();
		if (now - this.lastTypingSent < TYPING_SEND_THROTTLE_MS) return;
		this.lastTypingSent = now;
		window.dispatchEvent(
			new CustomEvent('ws:send', {
				detail: { type: 'chat.typing', payload: { channel_id: channelId } }
			})
		);
	}

	isTyping(channelId: string, userId: string): boolean {
		const hint = this.typing.get(channelId)?.get(userId);
		return hint !== undefined && hint > Date.now();
	}

	typingUsers(channelId: string): string[] {
		const hints = this.typing.get(channelId);
		if (!hints) return [];
		const now = Date.now();
		const users: string[] = [];
		for (const [userId, until] of hints) {
			if (until > now) users.push(userId);
		}
		return users;
	}

	isOnline(userId: string): boolean {
		return this.onlineUsers.has(userId);
	}

	// --- WebSocket handlers -------------------------------------------------

	private handleMessageCreated(payload: MessageCreatedPayload) {
		if (!payload?.message) return;
		this.insertMessage(payload.channel_id, payload.message);
		const fromSelf = payload.message.user_id === authState.user?.id;
		if (!fromSelf && payload.channel_id !== this.activeChannelId) {
			this.unread = new Map(this.unread).set(
				payload.channel_id,
				(this.unread.get(payload.channel_id) ?? 0) + 1
			);
		}
		// Keep the rail ordered by recency.
		this.bumpChannel(payload.channel_id, payload.message.created_at);
	}

	private handleMessageUpdated(payload: MessageUpdatedPayload) {
		if (!payload?.message) return;
		const list = this.messages.get(payload.channel_id);
		if (!list) return;
		const next = list.map((m) => (m.id === payload.message_id ? payload.message : m));
		this.messages = new Map(this.messages).set(payload.channel_id, next);
	}

	private handleMessageDeleted(payload: MessageDeletedPayload) {
		if (!payload) return;
		this.dropMessage(payload.channel_id, payload.message_id);
	}

	private handleTyping(payload: TypingPayload) {
		if (!payload || payload.user_id === authState.user?.id) return;
		const hints = new Map(this.typing.get(payload.channel_id) ?? new Map<string, number>());
		hints.set(payload.user_id, Date.now() + TYPING_TTL_MS);
		this.typing = new Map(this.typing).set(payload.channel_id, hints);
		this.scheduleTypingPrune();
	}

	private handlePresenceOnline(payload: { user_id: string }) {
		if (!payload?.user_id) return;
		const next = new Set(this.onlineUsers);
		next.add(payload.user_id);
		this.onlineUsers = next;
	}

	private handlePresenceOffline(payload: { user_id: string }) {
		if (!payload?.user_id) return;
		const next = new Set(this.onlineUsers);
		next.delete(payload.user_id);
		this.onlineUsers = next;
	}

	// --- internals ----------------------------------------------------------

	private insertMessage(channelId: string, message: ChatMessage) {
		const list = this.messages.get(channelId) ?? [];
		if (list.some((m) => m.id === message.id)) return;
		this.messages = new Map(this.messages).set(channelId, [...list, message]);
	}

	private dropMessage(channelId: string, messageId: string) {
		const list = this.messages.get(channelId);
		if (!list) return;
		const next = list.filter((m) => m.id !== messageId);
		this.messages = new Map(this.messages).set(channelId, next);
	}

	private bumpChannel(channelId: string, postAt: string) {
		const channel = this.channels.find((c) => c.id === channelId);
		if (!channel) return;
		this.channels = this.channels.map((c) =>
			c.id === channelId ? { ...c, last_post_at: postAt } : c
		);
	}

	private scheduleTypingPrune() {
		if (this.typingPruneTimer) return;
		this.typingPruneTimer = setInterval(() => {
			const now = Date.now();
			let changed = false;
			const next = new Map<string, Map<string, number>>();
			for (const [channelId, hints] of this.typing) {
				const filtered = new Map<string, number>();
				for (const [userId, until] of hints) {
					if (until > now) filtered.set(userId, until);
					else changed = true;
				}
				if (filtered.size > 0) next.set(channelId, filtered);
			}
			if (changed) this.typing = next;
			if (next.size === 0 && this.typingPruneTimer) {
				clearInterval(this.typingPruneTimer);
				this.typingPruneTimer = null;
			}
		}, 2000);
	}
}

export const chatState = new ChatState();
