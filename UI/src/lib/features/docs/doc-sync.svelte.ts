import { browser } from '$app/environment';
import * as Y from 'yjs';
import {
	absolutePositionToRelativePosition,
	relativePositionToAbsolutePosition
} from 'y-prosemirror';
import { ySyncPluginKey } from '@tiptap/y-tiptap';
import type { Editor } from '@tiptap/core';
import type { DocCollabPeer, DocSyncStatus } from '$lib/types/docs';

/**
 * DocSyncSession connects one open document to the backend's per-document
 * collaboration WebSocket: it replays the Yjs update log, relays local edits,
 * and exchanges cursor presence (Yjs relative positions, so remote carets
 * survive concurrent edits). The server is a dumb ordered relay — all
 * document merging happens client-side inside Yjs.
 */
export class DocSyncSession {
	readonly ydoc = new Y.Doc();

	status = $state<DocSyncStatus>('connecting');
	peers = $state<DocCollabPeer[]>([]);
	canWrite = $state(false);
	syncedOnce = $state(false);
	/** True when the server replay (or any later relay) carried Yjs updates. */
	hasRemoteUpdates = $state(false);
	/** Sticky server refusal: updates were pushed without write access. */
	readOnly = $state(false);

	private ws: WebSocket | null = null;
	private destroyed = false;
	private backoffMs = 800;
	private reconnectTimer: ReturnType<typeof setTimeout> | null = null;
	private readonly docId: string;
	private readonly url: string;
	private readonly identity: { name: string; color: string };
	private readonly onRemoteUpdate: (update: Uint8Array) => void;
	private readonly onCursorActivity: () => void;
	private readonly onReset: (() => void) | undefined;
	private cursorThrottleTimer: ReturnType<typeof setTimeout> | null = null;
	private lastCursorKey = '';

	constructor(options: {
		slug: string;
		docId: string;
		name: string;
		color: string;
		onRemoteUpdate: (update: Uint8Array) => void;
		/** Called after remote updates so the host can re-render remote carets. */
		onCursorActivity: () => void;
		/** Called when the server wiped the update log (revision restore). */
		onReset?: () => void;
	}) {
		this.docId = options.docId;
		this.identity = { name: options.name, color: options.color };
		this.onRemoteUpdate = options.onRemoteUpdate;
		this.onCursorActivity = options.onCursorActivity;
		this.onReset = options.onReset;
		const protocol = browser && window.location.protocol === 'https:' ? 'wss:' : 'ws:';
		this.url = `${protocol}//${window.location.host}/api/workspaces/${options.slug}/docs/${options.docId}/collab`;

		// Local edits flow out (origin marks our own socket so we never echo).
		this.ydoc.on('update', (update: Uint8Array, origin: unknown) => {
			if (origin === this || origin === 'server') return;
			this.send({ type: 'update', payload: { update: toBase64(update) } });
		});

		this.connect();
	}

	private connect() {
		if (this.destroyed || !browser) return;
		this.status = 'connecting';
		const socket = new WebSocket(this.url);
		this.ws = socket;

		socket.onopen = () => {
			this.backoffMs = 800;
			this.send({ type: 'join', payload: this.identity });
		};

		socket.onmessage = (event) => {
			if (socket !== this.ws) return;
			try {
				const msg = JSON.parse(event.data as string) as {
					type: string;
					payload: Record<string, unknown>;
				};
				this.handleMessage(msg);
			} catch {
				// Ignore malformed frames.
			}
		};

		socket.onclose = () => {
			if (socket !== this.ws || this.destroyed) return;
			this.status = 'offline';
			this.scheduleReconnect();
		};
		socket.onerror = () => socket.close();
	}

	private handleMessage(msg: { type: string; payload: Record<string, unknown> }) {
		switch (msg.type) {
			case 'init': {
				this.canWrite = msg.payload.can_write === true;
				const updates = (msg.payload.updates as string[] | undefined) ?? [];
				let applied = 0;
				for (const encoded of updates) {
					const bytes = fromBase64(encoded);
					if (bytes) {
						Y.applyUpdate(this.ydoc, bytes, 'server');
						applied++;
					}
				}
				// Docs whose content only exists as persisted HTML (markdown
				// imports, template copies) replay an empty log; the host
				// seeds the editor from content_html in that case.
				this.hasRemoteUpdates = applied > 0;
				this.syncedOnce = true;
				this.status = 'live';
				// Push our full state once after the replay: Yjs merges
				// idempotently, and this carries any edits made while the
				// socket was down (offline drafts) up to the server.
				this.send({ type: 'update', payload: { update: toBase64(Y.encodeStateAsUpdate(this.ydoc)) } });
				break;
			}
			case 'update': {
				const bytes = fromBase64(msg.payload.update as string | undefined);
				if (bytes) {
					Y.applyUpdate(this.ydoc, bytes, 'server');
					this.hasRemoteUpdates = true;
					this.onRemoteUpdate(bytes);
					this.onCursorActivity();
				}
				break;
			}
			case 'error': {
				// A read-only client tried to push updates: the server
				// refused them, so drop to a read-only session locally too.
				if (msg.payload.code === 'FORBIDDEN') {
					this.canWrite = false;
					this.readOnly = true;
				}
				break;
			}
			case 'reset': {
				// The server wiped the update log (revision restore). Local
				// state is stale by definition; the host tears the session
				// down and rebuilds it from the persisted snapshot.
				this.onReset?.();
				break;
			}
			case 'peers': {
				this.peers = ((msg.payload.peers as DocCollabPeer[] | undefined) ?? []).map((p) => ({
					...p,
					name: String(p.name ?? ''),
					color: String(p.color ?? '#71717a')
				}));
				this.onCursorActivity();
				break;
			}
			case 'peer.join': {
				const peer = msg.payload as unknown as DocCollabPeer;
				if (peer.client_id && !this.peers.some((p) => p.client_id === peer.client_id)) {
					this.peers = [...this.peers, { ...peer, name: String(peer.name ?? ''), color: String(peer.color ?? '#71717a') }];
				}
				break;
			}
			case 'peer.leave': {
				const id = msg.payload.client_id as string | undefined;
				if (id) this.peers = this.peers.filter((p) => p.client_id !== id);
				break;
			}
			case 'cursor': {
				const peer = msg.payload as unknown as DocCollabPeer;
				const existing = this.peers.find((p) => p.client_id === peer.client_id);
				if (existing) {
					this.peers = this.peers.map((p) => (p.client_id === peer.client_id ? { ...peer } : p));
				} else {
					this.peers = [...this.peers, peer];
				}
				this.onCursorActivity();
				break;
			}
			case 'compact.request': {
				// Answer with the full current state; the server swaps its log
				// for this single snapshot.
				this.send({
					type: 'snapshot',
					payload: { update: toBase64(Y.encodeStateAsUpdate(this.ydoc)) }
				});
				break;
			}
		}
	}

	private scheduleReconnect() {
		if (this.destroyed) return;
		if (this.reconnectTimer) clearTimeout(this.reconnectTimer);
		const delay = this.backoffMs * (0.75 + Math.random() * 0.5);
		this.backoffMs = Math.min(this.backoffMs * 2, 15_000);
		this.reconnectTimer = setTimeout(() => this.connect(), delay);
	}

	private send(message: unknown) {
		if (this.ws?.readyState === WebSocket.OPEN) {
			this.ws.send(JSON.stringify(message));
		}
	}

	/** Broadcast the local caret as Yjs relative positions (throttled). */
	sendCursor(editor: Editor) {
		if (!this.ws || this.ws.readyState !== WebSocket.OPEN || this.destroyed) return;
		if (this.cursorThrottleTimer) return;
		this.cursorThrottleTimer = setTimeout(() => {
			this.cursorThrottleTimer = null;
			const mapping = syncPluginMapping(editor);
			if (!mapping) return;
			const fragment = this.ydoc.getXmlFragment('default');
			const { head, anchor } = editor.state.selection;
			const headRel = safeRelative(head, fragment, mapping);
			const anchorRel = safeRelative(anchor, fragment, mapping);
			if (!headRel || !anchorRel) return;
			const key = `${headRel}|${anchorRel}`;
			if (key === this.lastCursorKey) return;
			this.lastCursorKey = key;
			this.send({ type: 'cursor', payload: { anchor: anchorRel, head: headRel } });
		}, 80);
	}

	/**
	 * Remote carets mapped to ProseMirror positions for the existing
	 * remote-cursor overlay in RichEditor; unresolved positions (peer in a
	 * region we do not know yet) are skipped.
	 */
	remoteCursors(editor: Editor): Array<{ name: string; color: string; position: number; anchor?: number }> {
		const mapping = syncPluginMapping(editor);
		if (!mapping) return [];
		const fragment = this.ydoc.getXmlFragment('default');
		const out: Array<{ name: string; color: string; position: number; anchor?: number }> = [];
		for (const peer of this.peers) {
			if (!peer.head) continue;
			const head = resolvePosition(this.ydoc, fragment, peer.head, editor, mapping);
			if (head === null) continue;
			const anchor = peer.anchor ? resolvePosition(this.ydoc, fragment, peer.anchor, editor, mapping) : undefined;
			out.push({ name: peer.name || '?', color: peer.color, position: head, anchor: anchor ?? head });
		}
		return out;
	}

	/** True when any peer is another user (for the presence avatar stack). */
	peerUsers(): Array<{ user_id: string; name: string; color: string }> {
		const seen = new Map<string, { user_id: string; name: string; color: string }>();
		for (const peer of this.peers) {
			if (!seen.has(peer.user_id)) seen.set(peer.user_id, { user_id: peer.user_id, name: peer.name, color: peer.color });
		}
		return [...seen.values()];
	}

	destroy() {
		this.destroyed = true;
		if (this.reconnectTimer) {
			clearTimeout(this.reconnectTimer);
			this.reconnectTimer = null;
		}
		if (this.cursorThrottleTimer) {
			clearTimeout(this.cursorThrottleTimer);
			this.cursorThrottleTimer = null;
		}
		this.ws?.close();
		this.ws = null;
		this.ydoc.destroy();
	}
}

/**
 * The collaboration binding's live Y-type → ProseMirror-node mapping. The
 * y-prosemirror position conversions walk the Y tree and need each block's
 * ProseMirror nodeSize, which only the binding that rendered the document
 * knows — with an empty map every non-text block counts as size 0 and carets
 * mis-place inside lists, quotes and tables.
 */
function syncPluginMapping(editor: Editor): Map<Y.AbstractType<any>, any> | undefined {
	if (editor.isDestroyed) return undefined;
	const state = ySyncPluginKey.getState(editor.state) as
		| { binding?: { mapping?: Map<Y.AbstractType<any>, any> } }
		| undefined;
	return state?.binding?.mapping;
}

function safeRelative(
	pos: number,
	fragment: Y.XmlFragment,
	mapping: Map<Y.AbstractType<any>, any>
): string | null {
	try {
		const rel = absolutePositionToRelativePosition(pos, fragment, mapping);
		return rel ? toBase64(Y.encodeRelativePosition(rel)) : null;
	} catch {
		return null;
	}
}

function resolvePosition(
	ydoc: Y.Doc,
	fragment: Y.XmlFragment,
	encoded: string,
	editor: Editor,
	mapping: Map<Y.AbstractType<any>, any>
): number | null {
	const bytes = fromBase64(encoded);
	if (!bytes) return null;
	try {
		const rel = Y.decodeRelativePosition(bytes);
		if (!rel) return null;
		const abs = relativePositionToAbsolutePosition(ydoc, fragment, rel, mapping);
		if (abs === null || abs === undefined) return null;
		return Math.max(0, Math.min(abs, editor.state.doc.content.size));
	} catch {
		return null;
	}
}

export function toBase64(bytes: Uint8Array): string {
	let binary = '';
	const chunk = 0x8000;
	for (let i = 0; i < bytes.length; i += chunk) {
		binary += String.fromCharCode(...bytes.subarray(i, i + chunk));
	}
	return btoa(binary);
}

export function fromBase64(value: string | undefined): Uint8Array | null {
	if (!value) return null;
	try {
		const binary = atob(value);
		const bytes = new Uint8Array(binary.length);
		for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i);
		return bytes;
	} catch {
		return null;
	}
}
