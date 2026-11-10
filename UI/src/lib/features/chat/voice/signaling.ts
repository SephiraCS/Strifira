/**
 * Voice-room signaling over the workspace WebSocket.
 *
 * Audio is a peer-to-peer WebRTC mesh: every participant connects to every
 * other participant. The strifira backend relays opaque signaling messages
 * between room members and announces rosters (see
 * BE/internal/realtime/voice.go); no media transits the server — the same
 * transport pattern the screen shares use, mesh-shaped instead of
 * presenter-to-viewers.
 */

import type { VoiceParticipant } from '$lib/types/chat';

/** Outgoing message shape accepted by the workspace layout's `ws:send`. */
export function wsSend(type: string, payload: unknown): void {
	window.dispatchEvent(new CustomEvent('ws:send', { detail: { type, payload } }));
}

/** Announce (or retract) this tab's membership in a channel's voice room. */
export function announceVoicePresence(
	channelId: string,
	action: 'join' | 'leave',
	muted: boolean,
	deafened: boolean
): void {
	wsSend('voice.presence', { channel_id: channelId, action, muted, deafened });
}

/** Push a mute/deafen flag change for the room this tab has joined. */
export function announceVoiceState(channelId: string, muted: boolean, deafened: boolean): void {
	wsSend('voice.state', { channel_id: channelId, muted, deafened });
}

/** Relay an SDP offer/answer or ICE candidate to one room member. */
export function sendVoiceSignal(
	channelId: string,
	toPeer: string,
	negotiationId: string,
	data: VoiceSignalData
): void {
	wsSend('voice.signal', { channel_id: channelId, to_peer: toPeer, negotiation_id: negotiationId, data });
}

export type VoiceSignalData =
	| { type: 'offer'; sdp: string }
	| { type: 'answer'; sdp: string }
	| { type: 'candidate'; candidate: RTCIceCandidateInit | null };

/** A relayed signaling message (server stamps the authenticated sender). */
export interface VoiceSignalEvent {
	channel_id: string;
	from: string;
	from_peer: string;
	to_peer: string;
	negotiation_id: string;
	data: VoiceSignalData;
}

/** Full roster snapshot, broadcast on every membership or flag change. */
export interface VoicePresenceEvent {
	channel_id: string;
	participants: VoiceParticipant[];
}

/** Targeted join confirmation carrying this tab's own peer id. */
export interface VoiceSyncEvent {
	channel_id: string;
	peer_id: string;
	participants: VoiceParticipant[];
}

export function onVoiceSignal(handler: (event: VoiceSignalEvent) => void): () => void {
	const listener = (e: Event) => handler((e as CustomEvent<VoiceSignalEvent>).detail);
	window.addEventListener('ws:voice.signal', listener);
	return () => window.removeEventListener('ws:voice.signal', listener);
}

export function onVoicePresence(handler: (event: VoicePresenceEvent) => void): () => void {
	const listener = (e: Event) => handler((e as CustomEvent<VoicePresenceEvent>).detail);
	window.addEventListener('ws:voice.presence', listener);
	return () => window.removeEventListener('ws:voice.presence', listener);
}

export function onVoiceSync(handler: (event: VoiceSyncEvent) => void): () => void {
	const listener = (e: Event) => handler((e as CustomEvent<VoiceSyncEvent>).detail);
	window.addEventListener('ws:voice.sync', listener);
	return () => window.removeEventListener('ws:voice.sync', listener);
}

/** Re-announcements and renegotiation ride the layout's reconnect event. */
export function onWebSocketReconnected(handler: () => void): () => void {
	const listener = () => handler();
	window.addEventListener('ws:reconnected', listener);
	return () => window.removeEventListener('ws:reconnected', listener);
}

/**
 * ICE configuration for direct peer-to-peer audio: public STUN for NAT
 * discovery, deliberately no TURN relay — connections that no path can
 * carry fail visibly instead of transiting a server (matches the shares).
 */
export const VOICE_ICE_SERVERS: RTCIceServer[] = [{ urls: ['stun:stun.l.google.com:19302'] }];
