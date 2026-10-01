import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { registerHooks } from 'node:module';
import test from 'node:test';
import { compileModule } from 'svelte/compiler';
import ts from 'typescript';

const source = await readFile(
	new URL('../../src/lib/features/shares/presenter.session.svelte.ts', import.meta.url),
	'utf8'
);
const javascript = ts.transpileModule(source, {
	compilerOptions: { target: ts.ScriptTarget.ESNext, module: ts.ModuleKind.ESNext }
}).outputText;
const compiled = compileModule(javascript, { filename: 'presenter.session.svelte.js', generate: 'client' }).js.code;
const svelteRuntimeURL = import.meta.resolve('svelte/internal/client');
let fixtureId = 0;

function deferred() {
	let resolve;
	const promise = new Promise((finish) => {
		resolve = finish;
	});
	return { promise, resolve };
}

class Track extends EventTarget {
	constructor(kind) {
		super();
		this.kind = kind;
		this.readyState = 'live';
		this.stops = 0;
	}
	stop() {
		this.stops++;
		this.readyState = 'ended';
	}
}

class Capture {
	constructor(withAudio) {
		this.tracks = [new Track('video'), ...(withAudio ? [new Track('audio')] : [])];
	}
	getTracks() {
		return this.tracks;
	}
	getVideoTracks() {
		return this.tracks.filter((track) => track.kind === 'video');
	}
	getAudioTracks() {
		return this.tracks.filter((track) => track.kind === 'audio');
	}
}

function capture(withAudio = false) {
	return new Capture(withAudio);
}

async function setup(t) {
	const rig = {
		peers: [],
		messages: [],
		creates: [],
		ends: [],
		errors: [],
		capture: async () => capture(),
		create: async (slug) => ({ share: { id: `share-${slug}`, status: 'active' }, presenter_token: `token-${slug}` }),
		end: async () => {},
		createShare(slug) {
			this.creates.push(slug);
			return this.create(slug);
		},
		endShare(...args) {
			this.ends.push(args);
			return this.end(...args);
		}
	};
	class Peer {
		constructor() {
			this.transceivers = [];
			this.remoteDescription = null;
			this.localDescription = null;
			this.closed = false;
			rig.peers.push(this);
		}
		addTransceiver(kind, { direction = 'sendrecv' } = {}) {
			const sender = {
				track: null,
				replaceTrack: async (track) => {
					sender.track = track;
				},
				getParameters: () => ({}),
				setParameters: async () => {}
			};
			const transceiver = {
				mid: null,
				direction,
				currentDirection: null,
				sender,
				receiver: { track: new Track(kind) },
				fromAddTrack: false
			};
			this.transceivers.push(transceiver);
			return transceiver;
		}
		addTrack(track) {
			const transceiver = this.addTransceiver(track.kind);
			transceiver.sender.track = track;
			transceiver.fromAddTrack = true;
			return transceiver.sender;
		}
		getSenders() {
			return this.transceivers.map((transceiver) => transceiver.sender);
		}
		getTransceivers() {
			return this.transceivers;
		}
		async setRemoteDescription(description) {
			this.remoteDescription = description;
			// An incoming recvonly offer can reuse an addTrack transceiver,
			// but cannot associate an unoffered addTransceiver transceiver.
			for (const [index, kind] of ['video', 'audio'].entries()) {
				const transceiver =
					this.transceivers.find(
						(candidate) => candidate.mid === null && candidate.fromAddTrack && candidate.receiver.track.kind === kind
					) ?? this.addTransceiver(kind, { direction: 'recvonly' });
				transceiver.mid = String(index);
			}
		}
		async createAnswer() {
			this.answerDirections = this.transceivers
				.filter((transceiver) => transceiver.mid !== null)
				.map((transceiver) => ({
					kind: transceiver.receiver.track.kind,
					direction: ['sendonly', 'sendrecv'].includes(transceiver.direction) ? 'sendonly' : 'inactive'
				}));
			return { type: 'answer', sdp: JSON.stringify(this.answerDirections) };
		}
		async setLocalDescription(description) {
			this.localDescription = description;
			for (const transceiver of this.transceivers) {
				if (transceiver.mid !== null) {
					transceiver.currentDirection = ['sendonly', 'sendrecv'].includes(transceiver.direction)
						? 'sendonly'
						: 'inactive';
				}
			}
		}
		close() {
			this.closed = true;
		}
	}
	const globals = { window: new EventTarget(), navigator: { mediaDevices: { getDisplayMedia: () => rig.capture() } } };
	const previous = new Map();
	for (const [key, value] of Object.entries({ ...globals, RTCPeerConnection: Peer, __presenterRig: rig })) {
		previous.set(key, Object.getOwnPropertyDescriptor(globalThis, key));
		Object.defineProperty(globalThis, key, { configurable: true, value });
	}
	t.after(() => {
		for (const [key, descriptor] of previous) {
			if (descriptor) Object.defineProperty(globalThis, key, descriptor);
			else delete globalThis[key];
		}
	});
	const stub = `
		const rig = globalThis.__presenterRig;
		export const browser = true;
		export const createShare = (...args) => rig.createShare(...args);
		export const endShare = (...args) => rig.endShare(...args);
		export const getShare = async () => ({ status: 'active' });
		export const appToast = { apiError: (...args) => rig.errors.push(args) };
		export const m = new Proxy({}, { get: (_, key) => () => key });
		export const sharesState = { reload: async () => {} };
		export const ICE_SERVERS = [];
		export const announceSharePresence = (...args) => rig.messages.push({ type: 'presence', args });
		export const sendShareSignal = (...args) => rig.messages.push({ type: 'signal', args });
		export const onShareSignal = (handler) => { rig.signal = handler; };
		export const onSharePresence = (handler) => { rig.presence = handler; };
		export const onWebSocketReconnected = (handler) => { rig.reconnect = handler; };
	`;
	const identity = ++fixtureId;
	const hook = registerHooks({
		resolve(specifier, context, nextResolve) {
			if (specifier === 'svelte/internal/client') return nextResolve(svelteRuntimeURL, context);
			if (specifier.startsWith('$') || specifier === './signaling' || specifier === './shares.state.svelte') {
				return { url: `data:text/javascript,${encodeURIComponent(stub)}#fixture-${identity}`, shortCircuit: true };
			}
			return nextResolve(specifier, context);
		}
	});
	try {
		const { presenterSession: session } = await import(
			`data:text/javascript,${encodeURIComponent(compiled)}#fixture-${identity}`
		);
		return { session, rig };
	} finally {
		hook.deregister();
	}
}

test('route ownership allows capture in a workspace whose socket has not opened', async (t) => {
	const { session, rig } = await setup(t);
	session.setWorkspace('a');
	rig.reconnect('a');
	session.setWorkspace('b');
	await session.start('b');
	assert.equal(session.active, true);
	assert.equal(session.share.id, 'share-b');
	assert.deepEqual(rig.creates, ['b']);
	rig.reconnect('a');
	assert.equal(session.active, true);
	assert.equal(rig.messages.filter((message) => message.type === 'presence').length, 1);
	rig.reconnect('b');
	assert.equal(rig.messages.filter((message) => message.type === 'presence').length, 2);
	assert.deepEqual(rig.ends, []);
});

test('A to B to A invalidates the old picker without clearing a newer pending start', async (t) => {
	const { session, rig } = await setup(t);
	const oldPicker = deferred();
	const newPicker = deferred();
	rig.capture = () => oldPicker.promise;
	session.setWorkspace('a');
	const oldStart = session.start('a');
	session.setWorkspace('b');
	assert.equal(session.starting, false);
	session.setWorkspace('a');
	rig.capture = () => newPicker.promise;
	const newStart = session.start('a');
	const abandoned = capture();
	oldPicker.resolve(abandoned);
	await oldStart;
	assert.equal(abandoned.getVideoTracks()[0].readyState, 'ended');
	assert.deepEqual(rig.creates, []);
	assert.equal(session.starting, true);
	newPicker.resolve(capture());
	await newStart;
	assert.equal(session.active, true);
	assert.deepEqual(rig.creates, ['a']);
});

test('leaving the workspace layout immediately invalidates a pending capture', async (t) => {
	const { session, rig } = await setup(t);
	const picker = deferred();
	rig.capture = () => picker.promise;
	session.setWorkspace('a');
	const start = session.start('a');
	session.setWorkspace('');
	const abandoned = capture();
	picker.resolve(abandoned);
	await start;
	assert.equal(abandoned.getVideoTracks()[0].readyState, 'ended');
	assert.equal(session.active, false);
	assert.equal(session.starting, false);
	assert.deepEqual(rig.creates, []);
});

test('a stale share creation ends its own row and leaves the new workspace session intact', async (t) => {
	const { session, rig } = await setup(t);
	const creation = deferred();
	const oldCapture = capture();
	rig.capture = async () => oldCapture;
	rig.create = (slug) =>
		slug === 'a' ? creation.promise : Promise.resolve({ share: { id: 'share-b' }, presenter_token: 'token-b' });
	session.setWorkspace('a');
	const oldStart = session.start('a');
	await Promise.resolve();
	assert.deepEqual(rig.creates, ['a']);
	session.setWorkspace('b');
	assert.equal(oldCapture.getVideoTracks()[0].readyState, 'ended');
	rig.capture = async () => capture();
	await session.start('b');
	creation.resolve({ share: { id: 'share-a' }, presenter_token: 'token-a' });
	await oldStart;
	assert.equal(oldCapture.getVideoTracks()[0].readyState, 'ended');
	assert.deepEqual(rig.ends, [['a', 'share-a', 'token-a']]);
	assert.equal(session.share.id, 'share-b');
	assert.equal(session.active, true);
});

test('same-workspace navigation preserves capture and a late old stop cannot tear down a new session', async (t) => {
	const { session, rig } = await setup(t);
	session.setWorkspace('a');
	await session.start('a');
	const oldStream = session.stream;
	session.setWorkspace('a');
	assert.equal(session.stream, oldStream);
	assert.equal(oldStream.getVideoTracks()[0].readyState, 'live');
	const ending = deferred();
	rig.end = () => ending.promise;
	session.setWorkspace('b');
	assert.equal(oldStream.getVideoTracks()[0].readyState, 'ended');
	assert.equal(session.share, null);
	await session.start('b');
	ending.resolve();
	await Promise.resolve();
	await Promise.resolve();
	assert.equal(session.share.id, 'share-b');
	assert.equal(session.active, true);
	assert.deepEqual(rig.ends, [['a', 'share-a', 'token-a']]);
});

test('an initially silent capture answers the offered audio section sendonly and can later send audio', async (t) => {
	const { session, rig } = await setup(t);
	session.setWorkspace('a');
	await session.start('a');
	rig.signal({
		share_id: session.share.id,
		from_peer: 'viewer-tab',
		negotiation_id: 'negotiation',
		data: { type: 'offer', sdp: 'recvonly-video-and-audio' }
	});
	// Wait for the answer through the public signaling boundary.
	for (let i = 0; i < 20 && !rig.messages.some((message) => message.type === 'signal'); i++) await Promise.resolve();
	assert.equal(rig.messages.filter((message) => message.type === 'signal').length, 1);
	const peer = rig.peers[0];
	const audioSections = peer.getTransceivers().filter((transceiver) => transceiver.receiver.track.kind === 'audio');
	assert.equal(audioSections.length, 1);
	assert.notEqual(audioSections[0].mid, null);
	assert.equal(audioSections[0].currentDirection, 'sendonly');
	assert.equal(audioSections[0].sender.track, null);
	const withAudio = capture(true);
	rig.capture = async () => withAudio;
	await session.switchSurface();
	assert.equal(audioSections[0].sender.track, withAudio.getAudioTracks()[0]);
	assert.equal(audioSections[0].currentDirection, 'sendonly');
	assert.equal(rig.peers.length, 1);
	assert.equal(peer.closed, false);
});

test('a picker from a stopped share cannot clear a restarted share\u2019s pending switch', async (t) => {
	const { session, rig } = await setup(t);
	session.setWorkspace('a');
	await session.start('a');
	const oldPicker = deferred();
	rig.capture = () => oldPicker.promise;
	const oldSwitch = session.switchSurface();
	await session.stop();
	assert.equal(session.switching, false);
	rig.capture = async () => capture();
	await session.start('a');
	const restartedStream = session.stream;
	const newPicker = deferred();
	rig.capture = () => newPicker.promise;
	const newSwitch = session.switchSurface();
	const abandoned = capture();
	oldPicker.resolve(abandoned);
	await oldSwitch;
	assert.equal(abandoned.getVideoTracks()[0].readyState, 'ended');
	assert.equal(session.stream, restartedStream);
	assert.equal(session.switching, true);
	const replacement = capture();
	newPicker.resolve(replacement);
	await newSwitch;
	assert.equal(session.stream, replacement);
	assert.equal(session.switching, false);
	assert.equal(session.active, true);
});
