/**
 * Screen-share signaling over the workspace WebSocket.
 *
 * Media is peer-to-peer WebRTC: viewers send offers to the presenter,
 * the presenter answers, and ICE candidates trickle both ways. The ku
 * backend only relays opaque signaling messages between share members
 * (see BE/internal/realtime/hub.go); no media server exists.
 */

export type ShareRole = 'presenter' | 'viewer';

/** Outgoing message shape accepted by the workspace layout's `ws:send`. */
function wsSend(type: string, payload: unknown): void {
	window.dispatchEvent(new CustomEvent('ws:send', { detail: { type, payload } }));
}

/** Announce (or retract) this tab's role in a share. */
export function announceSharePresence(shareId: string, role: ShareRole, action: 'join' | 'leave'): void {
	wsSend('share.presence', { share_id: shareId, role, action });
}

/** Send an opaque SDP/ICE message to one recipient user. */
export function sendShareSignal(shareId: string, to: string, data: unknown): void {
	wsSend('share.signal', { share_id: shareId, to, data });
}

/** A relayed signaling message (server stamps the authenticated sender). */
export interface ShareSignalEvent {
	share_id: string;
	from: string;
	data: SignalData;
}

export type SignalData =
	| { type: 'offer'; sdp: string }
	| { type: 'answer'; sdp: string }
	| { type: 'candidate'; candidate: RTCIceCandidateInit | null };

/** Live membership summary broadcast on every join/leave/disconnect. */
export interface SharePresenceEvent {
	share_id: string;
	presenter_online: boolean;
	viewers: number;
}

export function onShareSignal(handler: (event: ShareSignalEvent) => void): () => void {
	const listener = (e: Event) => handler((e as CustomEvent<ShareSignalEvent>).detail);
	window.addEventListener('ws:share.signal', listener);
	return () => window.removeEventListener('ws:share.signal', listener);
}

export function onSharePresence(handler: (event: SharePresenceEvent) => void): () => void {
	const listener = (e: Event) => handler((e as CustomEvent<SharePresenceEvent>).detail);
	window.addEventListener('ws:share.presence', listener);
	return () => window.removeEventListener('ws:share.presence', listener);
}

/**
 * Subscribe to the layout's WebSocket reconnects: presence lives on the
 * server per-connection, so a reconnect must re-announce and re-negotiate.
 */
export function onWebSocketReconnected(handler: () => void): () => void {
	const listener = () => handler();
	window.addEventListener('ws:reconnected', listener);
	return () => window.removeEventListener('ws:reconnected', listener);
}

/**
 * Subscribe to WebSocket drops: established P2P media keeps flowing, but
 * signaling and presence are down until the reconnect event fires.
 */
export function onWebSocketDisconnected(handler: () => void): () => void {
	const listener = () => handler();
	window.addEventListener('ws:disconnected', listener);
	return () => window.removeEventListener('ws:disconnected', listener);
}

/**
 * ICE configuration for direct peer-to-peer connections: public STUN for
 * NAT discovery, deliberately no TURN relay — connections that no path can
 * carry fail visibly instead of transiting a server.
 */
export const ICE_SERVERS: RTCIceServer[] = [{ urls: ['stun:stun.l.google.com:19302'] }];
