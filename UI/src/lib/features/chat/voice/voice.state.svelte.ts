import { browser } from '$app/environment';
import { listVoiceRooms } from '$lib/api/chat';
import type { VoiceParticipant } from '$lib/types/chat';
import {
	announceVoicePresence,
	announceVoiceState,
	onVoicePresence,
	onVoiceSignal,
	onVoiceSync,
	onWebSocketReconnected,
	sendVoiceSignal,
	VOICE_ICE_SERVERS,
	type VoiceSignalData
} from './signaling';

/**
 * Voice rooms state: the workspace's live rosters plus this tab's media
 * session. Module scope, like the screen-share presenter session — route
 * components unmount freely, an active voice session does not.
 *
 * Media is a P2P WebRTC mesh signaled over the workspace socket. Offers are
 * deterministic per pair (the lexicographically smaller peer id offers), so
 * glare cannot happen and every participant converges without a server
 * round-trip.
 *
 * Input handling (push-to-talk vs voice activity, noise suppression, gain)
 * is a per-user, per-browser preference — persisted in localStorage, never
 * a deploy setting.
 */

export type VoiceInputMode = 'vad' | 'ptt';

export interface VoiceSettings {
	inputMode: VoiceInputMode;
	pttKey: string; // KeyboardEvent.code
	noiseSuppression: boolean;
	echoCancellation: boolean;
	gain: number; // 0.5 – 2, applied to the outbound mic path
	outputVolume: number; // 0 – 2, applied to every incoming stream
}

const SETTINGS_KEY = 'strifira-voice-settings';

const DEFAULT_SETTINGS: VoiceSettings = {
	inputMode: 'vad',
	pttKey: 'Space',
	noiseSuppression: true,
	echoCancellation: true,
	gain: 1,
	outputVolume: 1
};

// Speaking detection (RMS on an AnalyserNode): enter above SPEAK_ENTER,
// leave below SPEAK_EXIT after SPEAK_HOLD_MS of quiet — hysteresis keeps a
// Speaking ring from flickering on consonant gaps.
const SPEAK_ENTER = 0.03;
const SPEAK_EXIT = 0.015;
const SPEAK_HOLD_MS = 250;
const ANALYSE_INTERVAL_MS = 120;

// How long a join waits for the server's targeted voice.sync before the
// attempt is reported as failed (socket up but the join was refused/lost).
const JOIN_SYNC_TIMEOUT_MS = 6000;

interface VoiceSession {
	channelId: string;
	slug: string;
	peerId: string;
	muted: boolean;
	deafened: boolean;
	connecting: boolean;
	error: string | null;
}

class VoiceState {
	/** Live rosters per channel (visible to everyone in the workspace). */
	rooms = $state<Map<string, VoiceParticipant[]>>(new Map());
	/** This tab's session; null when not in a room. */
	session = $state<VoiceSession | null>(null);
	/** Peer ids (own included) whose audio is currently above the floor. */
	speaking = $state<Set<string>>(new Set());
	/** True while the push-to-talk key is held. */
	pttActive = $state(false);

	settings = $state<VoiceSettings>({ ...DEFAULT_SETTINGS });

	// Plain bookkeeping — mirrors the presenter session's structure.
	private slug = '';
	private wired = false;
	private stream: MediaStream | null = null;
	private outboundTrack: MediaStreamTrack | null = null;
	private audioCtx: AudioContext | null = null;
	private gainNode: GainNode | null = null;
	private outAnalyser: AnalyserNode | null = null;
	private inAnalysers = new Map<string, { analyser: AnalyserNode; buf: Float32Array<ArrayBuffer> }>();
	private audioEls = new Map<string, HTMLAudioElement>();
	private peers = new Map<string, RTCPeerConnection>();
	private negotiations = new Map<string, string>();
	private pendingCandidates = new Map<string, RTCIceCandidateInit[]>();
	private answeredOffers = new Map<string, string>();
	private levels = new Map<string, { open: boolean; quietSince: number }>();
	private analyseTimer: ReturnType<typeof setInterval> | null = null;
	private heartbeatTimer: ReturnType<typeof setInterval> | null = null;
	private joinTimer: ReturnType<typeof setTimeout> | null = null;
	private pttBound = false;
	private generation = 0;

	constructor() {
		if (!browser) return;
		this.loadSettings();
		this.wire();
	}

	// ---- derived ------------------------------------------------------------

	get connected(): boolean {
		return this.session !== null && !this.session.connecting && this.session.error === null;
	}

	participantsOf(channelId: string): VoiceParticipant[] {
		return this.rooms.get(channelId) ?? [];
	}

	/** The room this tab occupies, or null. */
	get currentRoom(): VoiceParticipant[] {
		return this.session ? this.participantsOf(this.session.channelId) : [];
	}

	roomActivity(channelId: string): number {
		const roster = this.rooms.get(channelId);
		if (!roster) return 0;
		const users = new Set(roster.map((p) => p.user_id));
		return users.size;
	}

	isSpeaking(peerId: string): boolean {
		return this.speaking.has(peerId);
	}

	// ---- membership ----------------------------------------------------------

	/** Loads the workspace's rosters once per slug (snapshots follow live). */
	async ensureWorkspace(slug: string) {
		if (!slug) return;
		if (this.slug === slug) return;
		this.handleWorkspaceChange(slug);
		this.slug = slug;
		await this.reloadRooms();
	}

	async reloadRooms() {
		if (!this.slug) return;
		try {
			const list = await listVoiceRooms(this.slug);
			this.rooms = new Map(list.rooms.map((room) => [room.channel_id, room.participants]));
		} catch {
			// Transient; snapshots and the reconnect refetch heal the view.
		}
	}

	/** Entering a different workspace tears the session down (rooms are
	 * workspace-scoped). An empty slug is the layout's goodbye. */
	handleWorkspaceChange(slug: string) {
		if (this.session && this.session.slug !== slug) void this.leave();
		this.slug = slug;
	}

	async join(channelId: string): Promise<boolean> {
		if (!browser || !this.slug) return false;
		if (this.session) {
			if (this.session.channelId === channelId && !this.session.error) return true;
			await this.leave();
		}
		const generation = ++this.generation;
		this.session = {
			channelId,
			slug: this.slug,
			peerId: '',
			muted: false,
			deafened: false,
			connecting: true,
			error: null
		};
		try {
			await this.openMic();
			if (generation !== this.generation) return false;
		} catch {
			if (generation !== this.generation) return false;
			this.failSession('mic');
			return false;
		}
		announceVoicePresence(channelId, 'join', false, false);
		// The server answers with a targeted voice.sync carrying our peer id;
		// silence means the join was refused or lost.
		if (this.joinTimer) clearTimeout(this.joinTimer);
		this.joinTimer = setTimeout(() => {
			if (this.generation === generation && this.session && !this.session.peerId) {
				this.failSession('join');
			}
		}, JOIN_SYNC_TIMEOUT_MS);
		this.startHeartbeat();
		this.bindPtt();
		return true;
	}

	async leave(): Promise<void> {
		const session = this.session;
		if (!session) return;
		this.generation++;
		if (session.peerId) {
			announceVoicePresence(session.channelId, 'leave', session.muted, session.deafened);
		}
		this.teardownMedia();
		this.session = null;
		this.speaking = new Set();
	}

	async toggleMute(): Promise<void> {
		const session = this.session;
		if (!session || session.connecting) return;
		session.muted = !session.muted;
		this.applyGate();
		announceVoiceState(session.channelId, session.muted, session.deafened);
	}

	async toggleDeafen(): Promise<void> {
		const session = this.session;
		if (!session || session.connecting) return;
		session.deafened = !session.deafened;
		if (session.deafened && !session.muted) {
			session.muted = true;
		}
		this.applyGate();
		this.applyOutputVolume();
		announceVoiceState(session.channelId, session.muted, session.deafened);
	}

	/** Push-to-talk press/release (bound key). Ignored while muted. */
	setPtt(active: boolean): void {
		if (this.pttActive === active) return;
		this.pttActive = active;
		this.applyGate();
	}

	// ---- settings --------------------------------------------------------------

	updateSettings(patch: Partial<VoiceSettings>): void {
		this.settings = { ...this.settings, ...patch };
		try {
			localStorage.setItem(SETTINGS_KEY, JSON.stringify(this.settings));
		} catch {
			// Persistence is best-effort; the session keeps working.
		}
		this.applySettingsToMedia();
	}

	private loadSettings(): void {
		try {
			const raw = localStorage.getItem(SETTINGS_KEY);
			if (!raw) return;
			const data = JSON.parse(raw) as Partial<VoiceSettings>;
			this.settings = {
				...DEFAULT_SETTINGS,
				...data,
				gain: clamp(data.gain ?? DEFAULT_SETTINGS.gain, 0.5, 2),
				outputVolume: clamp(data.outputVolume ?? DEFAULT_SETTINGS.outputVolume, 0, 2)
			};
		} catch {
			// Corrupt settings fall back to defaults.
		}
	}

	// ---- WebSocket handlers -----------------------------------------------------

	private wire(): void {
		if (this.wired) return;
		this.wired = true;

		onVoicePresence((event) => {
			this.rooms = new Map(this.rooms).set(event.channel_id, event.participants ?? []);
			if (this.session?.channelId === event.channel_id) this.reconcilePeers();
		});

		onVoiceSync((event) => {
			const session = this.session;
			if (!session || event.channel_id !== session.channelId) return;
			if (this.joinTimer) {
				clearTimeout(this.joinTimer);
				this.joinTimer = null;
			}
			session.peerId = event.peer_id;
			session.connecting = false;
			this.rooms = new Map(this.rooms).set(event.channel_id, event.participants ?? []);
			this.reconcilePeers();
		});

		onVoiceSignal((event) => void this.handleSignal(event));

		onWebSocketReconnected(() => {
			const session = this.session;
			if (!session) return;
			// Presence lives per socket: tear the half-open mesh down, then
			// re-announce and let the fresh roster (everyone's peer ids
			// changed) rebuild it.
			for (const [peerId, pc] of this.peers) this.dropPeer(peerId, pc);
			session.peerId = '';
			session.connecting = true;
			announceVoicePresence(session.channelId, 'join', session.muted, session.deafened);
			if (this.joinTimer) clearTimeout(this.joinTimer);
			this.joinTimer = setTimeout(() => {
				if (this.session && !this.session.peerId) this.failSession('join');
			}, JOIN_SYNC_TIMEOUT_MS);
		});

		window.addEventListener('beforeunload', () => {
			const session = this.session;
			if (session?.peerId) announceVoicePresence(session.channelId, 'leave', session.muted, session.deafened);
		});
	}

	private startHeartbeat(): void {
		if (this.heartbeatTimer) return;
		this.heartbeatTimer = setInterval(() => {
			const session = this.session;
			if (!session?.peerId) return;
			// Idempotent re-join: refreshes this node's roster timestamps so a
			// quiet room never looks abandoned, and heals a missed join.
			announceVoicePresence(session.channelId, 'join', session.muted, session.deafened);
		}, 30_000);
	}

	// ---- mesh -------------------------------------------------------------------

	/** Syncs peer connections with the roster. The smaller peer id of each
	 * pair offers; the other waits for the offer and answers. */
	private reconcilePeers(): void {
		const session = this.session;
		if (!session?.peerId) return;
		const roster = this.participantsOf(session.channelId);
		const live = new Set<string>();
		for (const participant of roster) {
			if (participant.peer_id === session.peerId) continue;
			live.add(participant.peer_id);
		}
		for (const [peerId, pc] of this.peers) {
			if (live.has(peerId)) continue;
			this.dropPeer(peerId, pc);
		}
		for (const peerId of live) {
			if (this.peers.has(peerId)) continue;
			// The waiter side creates only a dormant placeholder-less entry:
			// its pc comes into being when the offer arrives, so the mesh
			// forms without double offers.
			if (session.peerId < peerId) this.createOfferer(peerId);
		}
	}

	private createOfferer(peerId: string): void {
		const session = this.session;
		if (!session) return;
		const negotiationId = crypto.randomUUID();
		const pc = this.createPeer(peerId, negotiationId);
		// The outbound transceiver is established by addTrack before the
		// offer; onnegotiationneeded fires synchronously enough to drive it.
		if (this.outboundTrack) pc.addTrack(this.outboundTrack, this.outboundStream());
		pc.onnegotiationneeded = () => {
			void this.negotiate(peerId, pc);
		};
	}

	private outboundStream(): MediaStream {
		// A minimal stream wrapper for addTrack's second argument.
		return this.stream ?? new MediaStream([this.outboundTrack!]);
	}

	private createPeer(peerId: string, negotiationId: string): RTCPeerConnection {
		const session = this.session!;
		const pc = new RTCPeerConnection({ iceServers: VOICE_ICE_SERVERS });
		this.peers.set(peerId, pc);
		this.negotiations.set(peerId, negotiationId);
		pc.onicecandidate = (e) => {
			if (e.candidate) {
				sendVoiceSignal(session.channelId, peerId, negotiationId, {
					type: 'candidate',
					candidate: e.candidate.toJSON()
				});
			}
		};
		// The offerer's track event carries no stream id (the answerer's msid
		// differs from the offer's), so fall back to wrapping the bare track —
		// otherwise only one side would ever hear the other.
		pc.ontrack = (e) => this.attachRemoteAudio(peerId, e.streams[0] ?? new MediaStream([e.track]));
		pc.onconnectionstatechange = () => {
			if (pc.connectionState === 'failed' || pc.connectionState === 'closed') {
				if (this.peers.get(peerId) === pc) this.dropPeer(peerId, pc);
			}
		};
		return pc;
	}

	private dropPeer(peerId: string, pc: RTCPeerConnection): void {
		this.peers.delete(peerId);
		this.negotiations.delete(peerId);
		this.pendingCandidates.delete(peerId);
		this.answeredOffers.delete(peerId);
		this.levels.delete(peerId);
		const analyser = this.inAnalysers.get(peerId);
		if (analyser) {
			analyser.analyser.disconnect();
			this.inAnalysers.delete(peerId);
		}
		const el = this.audioEls.get(peerId);
		if (el) {
			el.srcObject = null;
			el.remove();
			this.audioEls.delete(peerId);
		}
		this.clearSpeaking(peerId);
		pc.close();
	}

	private async negotiate(peerId: string, pc: RTCPeerConnection): Promise<void> {
		const session = this.session;
		const negotiationId = this.negotiations.get(peerId);
		if (!session || !negotiationId) return;
		try {
			const offer = await pc.createOffer();
			await pc.setLocalDescription(offer);
			sendVoiceSignal(session.channelId, peerId, negotiationId, {
				type: 'offer',
				sdp: pc.localDescription!.sdp!
			});
		} catch {
			// A failed negotiation leaves the pair dead; the roster's next
			// change (heartbeat or reconnect) rebuilds it.
		}
	}

	private async handleSignal(event: {
		channel_id: string;
		from_peer: string;
		negotiation_id: string;
		data: VoiceSignalData;
	}): Promise<void> {
		const session = this.session;
		if (!session || event.channel_id !== session.channelId || !session.peerId) return;
		const peerId = event.from_peer;
		if (!peerId || peerId === session.peerId) return;
		// Only roster members may signal us; strangers are dropped.
		const known = this.participantsOf(session.channelId).some((p) => p.peer_id === peerId);
		if (!known) return;

		const data = event.data;
		if (data.type === 'offer') {
			if (this.answeredOffers.get(peerId) === event.negotiation_id) return;
			const existing = this.peers.get(peerId);
			if (existing) this.dropPeer(peerId, existing);
			const pc = this.createPeer(peerId, event.negotiation_id);
			this.answeredOffers.set(peerId, event.negotiation_id);
			try {
				await pc.setRemoteDescription({ type: 'offer', sdp: data.sdp });
				// Claim the offer's audio section for our outbound track.
				for (const transceiver of pc.getTransceivers()) {
					if (transceiver.receiver.track.kind !== 'audio') continue;
					transceiver.direction = 'sendrecv';
					if (this.outboundTrack && transceiver.sender.track !== this.outboundTrack) {
						await transceiver.sender.replaceTrack(this.outboundTrack);
					}
				}
				const answer = await pc.createAnswer();
				await pc.setLocalDescription(answer);
				sendVoiceSignal(session.channelId, peerId, event.negotiation_id, {
					type: 'answer',
					sdp: pc.localDescription!.sdp!
				});
				await this.flushCandidates(peerId, pc);
			} catch {
				if (this.peers.get(peerId) === pc) this.dropPeer(peerId, pc);
			}
			return;
		}

		const pc = this.peers.get(peerId);
		if (!pc || this.negotiations.get(peerId) !== event.negotiation_id) return;
		if (data.type === 'answer') {
			if (pc.signalingState === 'have-local-offer') {
				await pc.setRemoteDescription({ type: 'answer', sdp: data.sdp });
				await this.flushCandidates(peerId, pc);
			}
			return;
		}
		if (data.type === 'candidate') {
			this.queueCandidate(peerId, data.candidate, pc);
		}
	}

	private queueCandidate(peerId: string, candidate: RTCIceCandidateInit | null, pc: RTCPeerConnection): void {
		if (candidate === null) return;
		if (pc.remoteDescription) {
			void pc.addIceCandidate(candidate).catch(() => {});
		} else {
			const queue = this.pendingCandidates.get(peerId) ?? [];
			queue.push(candidate);
			this.pendingCandidates.set(peerId, queue);
		}
	}

	private async flushCandidates(peerId: string, pc: RTCPeerConnection): Promise<void> {
		const queue = this.pendingCandidates.get(peerId) ?? [];
		this.pendingCandidates.delete(peerId);
		for (const candidate of queue) {
			await pc.addIceCandidate(candidate).catch(() => {});
		}
	}

	// ---- media ----------------------------------------------------------------

	private async openMic(): Promise<void> {
		const constraints: MediaStreamConstraints = {
			audio: {
				channelCount: 1,
				echoCancellation: this.settings.echoCancellation,
				noiseSuppression: this.settings.noiseSuppression,
				autoGainControl: true,
				sampleRate: 48000
			},
			video: false
		};
		const stream = await navigator.mediaDevices.getUserMedia(constraints);
		this.stream = stream;

		const ctx = new AudioContext({ sampleRate: 48000 });
		this.audioCtx = ctx;
		const source = ctx.createMediaStreamSource(stream);
		const gain = ctx.createGain();
		gain.gain.value = this.settings.gain;
		const analyser = ctx.createAnalyser();
		analyser.fftSize = 512;
		const destination = ctx.createMediaStreamDestination();
		source.connect(gain);
		gain.connect(analyser);
		gain.connect(destination);
		this.gainNode = gain;
		this.outAnalyser = analyser;
		this.outboundTrack = destination.stream.getAudioTracks()[0] ?? null;
		this.levels.set('self', { open: true, quietSince: 0 });
		this.startAnalyser();
		this.applyGate();
	}

	/** Mic mute semantics: the track gates what peers hear. VAD closes the
	 * gate on silence; PTT holds it open only while the key is down. */
	private applyGate(): void {
		const session = this.session;
		if (!session || !this.outboundTrack) return;
		const gateOpen =
			!session.muted && (this.settings.inputMode === 'ptt' ? this.pttActive : true);
		this.outboundTrack.enabled = gateOpen;
		if (gateOpen) this.clearSpeaking(session.peerId);
	}

	private async applySettingsToMedia(): Promise<void> {
		if (this.gainNode && this.audioCtx) {
			this.gainNode.gain.setTargetAtTime(this.settings.gain, this.audioCtx.currentTime, 0.01);
		}
		if (this.stream?.getAudioTracks().length) {
			const track = this.stream.getAudioTracks()[0];
			try {
				await track.applyConstraints({
					echoCancellation: this.settings.echoCancellation,
					noiseSuppression: this.settings.noiseSuppression
				});
			} catch {
				// Browsers without flexible constraints keep the captured path.
			}
		}
		this.applyOutputVolume();
		this.applyGate();
	}

	private attachRemoteAudio(peerId: string, stream: MediaStream): void {
		if (!stream) return;
		const el = document.createElement('audio');
		el.srcObject = stream;
		el.autoplay = true;
		el.dataset.peer = peerId;
		document.body.appendChild(el);
		this.audioEls.set(peerId, el);
		void el.play().catch(() => {});

		// Speaking detection on the incoming audio itself: what a peer hears
		// is the truth their ring shows, with no extra signaling.
		if (this.audioCtx) {
			const analyser = this.audioCtx.createAnalyser();
			analyser.fftSize = 512;
			try {
				this.audioCtx.createMediaStreamSource(stream).connect(analyser);
				this.inAnalysers.set(peerId, { analyser, buf: new Float32Array(analyser.fftSize) });
			} catch {
				// A silent peer simply never lights up.
			}
		}
		this.levels.set(peerId, { open: false, quietSince: 0 });
		this.applyOutputVolume();
	}

	private applyOutputVolume(): void {
		const session = this.session;
		const muted = session?.deafened ?? false;
		for (const el of this.audioEls.values()) {
			el.muted = muted;
			el.volume = clamp(this.settings.outputVolume, 0, 1);
		}
	}

	private startAnalyser(): void {
		if (this.analyseTimer) return;
		this.analyseTimer = setInterval(() => this.analyseLevels(), ANALYSE_INTERVAL_MS);
	}

	private analyseLevels(): void {
		const session = this.session;
		if (!session?.peerId || !this.outAnalyser) return;
		const now = Date.now();
		const next = new Set(this.speaking);

		const readRms = (analyser: AnalyserNode, buf: Float32Array<ArrayBuffer>): number => {
			analyser.getFloatTimeDomainData(buf);
			let sum = 0;
			for (let i = 0; i < buf.length; i++) sum += buf[i] * buf[i];
			return Math.sqrt(sum / buf.length);
		};

		const outBuf = new Float32Array(this.outAnalyser.fftSize);
		const selfRms = readRms(this.outAnalyser, outBuf);
		const selfOpen = !session.muted && (this.settings.inputMode === 'ptt' ? this.pttActive : true);
		this.hysteresis(next, 'self', selfRms, selfOpen, now);

		for (const [peerId, entry] of this.inAnalysers) {
			const buf: Float32Array<ArrayBuffer> =
				entry.buf.length === entry.analyser.fftSize ? entry.buf : new Float32Array(entry.analyser.fftSize);
			entry.buf = buf;
			this.hysteresis(next, peerId, readRms(entry.analyser, buf), true, now);
		}

		// Drop speaking state for peers whose connections are gone.
		for (const peerId of next) {
			if (peerId !== 'self' && !this.inAnalysers.has(peerId) && !this.peers.has(peerId)) {
				next.delete(peerId);
			}
		}
		if (next.has('self') && !selfOpen) next.delete('self');

		const mapped = new Set(next);
		if (mapped.has('self')) {
			mapped.delete('self');
			mapped.add(session.peerId);
		}
		if (!setsEqual(mapped, this.speaking)) this.speaking = mapped;
	}

	/** Schmitt trigger per peer; mutates `next` and returns nothing. */
	private hysteresis(next: Set<string>, key: string, rms: number, gateOpen: boolean, now: number): void {
		let level = this.levels.get(key);
		if (!level) {
			level = { open: false, quietSince: 0 };
			this.levels.set(key, level);
		}
		if (!gateOpen) {
			level.open = false;
			next.delete(key);
			return;
		}
		if (!level.open && rms >= SPEAK_ENTER) {
			level.open = true;
			next.add(key);
		} else if (level.open) {
			if (rms >= SPEAK_EXIT) {
				level.quietSince = 0;
			} else {
				if (!level.quietSince) level.quietSince = now;
				if (now - level.quietSince > SPEAK_HOLD_MS) {
					level.open = false;
					next.delete(key);
				}
			}
		}
	}

	private clearSpeaking(peerId: string): void {
		if (!this.speaking.has(peerId)) return;
		const next = new Set(this.speaking);
		next.delete(peerId);
		this.speaking = next;
	}

	// ---- lifecycle ----------------------------------------------------------------

	private failSession(kind: 'mic' | 'join'): void {
		const session = this.session;
		if (!session) return;
		session.connecting = false;
		session.error = kind;
		this.teardownMedia();
	}

	private teardownMedia(): void {
		if (this.joinTimer) {
			clearTimeout(this.joinTimer);
			this.joinTimer = null;
		}
		if (this.heartbeatTimer) {
			clearInterval(this.heartbeatTimer);
			this.heartbeatTimer = null;
		}
		if (this.analyseTimer) {
			clearInterval(this.analyseTimer);
			this.analyseTimer = null;
		}
		this.unbindPtt();
		for (const [peerId, pc] of this.peers) {
			this.negotiations.delete(peerId);
			pc.close();
		}
		this.peers.clear();
		this.negotiations.clear();
		this.pendingCandidates.clear();
		this.answeredOffers.clear();
		for (const [, entry] of this.inAnalysers) entry.analyser.disconnect();
		this.inAnalysers.clear();
		for (const el of this.audioEls.values()) {
			el.srcObject = null;
			el.remove();
		}
		this.audioEls.clear();
		this.levels.clear();
		this.outboundTrack = null;
		this.gainNode = null;
		this.outAnalyser = null;
		this.stream?.getTracks().forEach((t) => t.stop());
		this.stream = null;
		void this.audioCtx?.close().catch(() => {});
		this.audioCtx = null;
		this.pttActive = false;
	}

	/** Push-to-talk binds a single key for the session's lifetime. Typing
	 * targets (inputs, textareas, editors) never trigger it. */
	private bindPtt(): void {
		if (this.pttBound) return;
		this.pttBound = true;
		window.addEventListener('keydown', this.handlePttDown);
		window.addEventListener('keyup', this.handlePttUp);
		window.addEventListener('blur', this.handlePttUp);
	}

	private unbindPtt(): void {
		if (!this.pttBound) return;
		this.pttBound = false;
		window.removeEventListener('keydown', this.handlePttDown);
		window.removeEventListener('keyup', this.handlePttUp);
		window.removeEventListener('blur', this.handlePttUp);
	}

	private handlePttDown = (e: Event): void => {
		if (!(e instanceof KeyboardEvent)) return;
		const session = this.session;
		if (!session || this.settings.inputMode !== 'ptt') return;
		if (e.code !== this.settings.pttKey || e.repeat) return;
		if (isTypingTarget(e.target)) return;
		e.preventDefault();
		this.setPtt(true);
	};

	private handlePttUp = (e: Event): void => {
		if (e instanceof FocusEvent || (e instanceof KeyboardEvent && e.code === this.settings.pttKey)) {
			this.setPtt(false);
		}
	};
}

function clamp(value: number, min: number, max: number): number {
	return Math.min(max, Math.max(min, value));
}

function isTypingTarget(target: EventTarget | null): boolean {
	if (!(target instanceof HTMLElement)) return false;
	const tag = target.tagName;
	return (
		tag === 'INPUT' ||
		tag === 'TEXTAREA' ||
		tag === 'SELECT' ||
		tag === 'TEXT' ||
		target.isContentEditable
	);
}

function setsEqual(a: Set<string>, b: Set<string>): boolean {
	if (a.size !== b.size) return false;
	for (const item of a) if (!b.has(item)) return false;
	return true;
}

export const voiceState = new VoiceState();
