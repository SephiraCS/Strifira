/**
 * Shared video-frame and canvas helpers for the capture pipeline.
 */

export interface CapturedFrame {
	blob: Blob;
	width: number;
	height: number;
}

export class OversizedCaptureError extends Error {
	constructor() {
		super('screenshot oversized');
		this.name = 'OversizedCaptureError';
	}
}

// The upload endpoint accepts 10MB; stay under it so MIME validation sees a
// well-formed request and the server never has to reject a real capture.
export const MAX_PNG_BYTES = 9 * 1024 * 1024;

export function wait(ms: number): Promise<void> {
	return new Promise((resolve) => setTimeout(resolve, ms));
}

export function stopStream(stream: MediaStream | null | undefined): void {
	stream?.getTracks().forEach((track) => track.stop());
}

/**
 * Attaches a display stream to an off-DOM video element and waits for the
 * first decodable frames.
 */
export async function playStream(stream: MediaStream): Promise<HTMLVideoElement> {
	const video = document.createElement('video');
	video.muted = true;
	video.playsInline = true;
	video.srcObject = stream;
	await video.play();
	if (video.readyState < 2) {
		await new Promise<void>((resolve, reject) => {
			const done = () => {
				video.removeEventListener('loadeddata', done);
				video.removeEventListener('error', fail);
				resolve();
			};
			const fail = () => {
				video.removeEventListener('loadeddata', done);
				video.removeEventListener('error', fail);
				reject(new Error('capture video failed to load'));
			};
			video.addEventListener('loadeddata', done);
			video.addEventListener('error', fail);
		});
	}
	await wait(250);
	return video;
}

/**
 * Waits for a fresh video frame after a scroll (or for the guard timeout
 * when the stream cannot promise frame callbacks, e.g. canvas-backed test
 * streams).
 */
export async function nextFrame(video: HTMLVideoElement, timeoutMs = 400): Promise<void> {
	await new Promise<void>((resolve) => {
		let settled = false;
		const done = () => {
			if (settled) return;
			settled = true;
			resolve();
		};
		const callbackVideo = video as HTMLVideoElement & {
			requestVideoFrameCallback?: (cb: () => void) => number;
		};
		if (typeof callbackVideo.requestVideoFrameCallback === 'function') {
			callbackVideo.requestVideoFrameCallback(() => done());
		} else {
			requestAnimationFrame(() => requestAnimationFrame(() => done()));
		}
		setTimeout(done, timeoutMs);
	});
	await wait(40);
}

export function canvasToBlob(canvas: HTMLCanvasElement): Promise<Blob> {
	return new Promise((resolve, reject) => {
		canvas.toBlob(
			(blob) => (blob ? resolve(blob) : reject(new Error('canvas encoding failed'))),
			'image/png'
		);
	});
}

function shrinkCanvas(canvas: HTMLCanvasElement, factor: number): HTMLCanvasElement {
	const next = document.createElement('canvas');
	next.width = Math.max(1, Math.round(canvas.width * factor));
	next.height = Math.max(1, Math.round(canvas.height * factor));
	const ctx = next.getContext('2d');
	if (ctx) ctx.drawImage(canvas, 0, 0, next.width, next.height);
	return next;
}

/**
 * Encodes a canvas as PNG, halving resolution while the result stays above
 * the upload limit. Throws OversizedCaptureError when even the floor scale
 * does not fit.
 */
export async function encodeCappedPng(source: HTMLCanvasElement): Promise<Blob> {
	let canvas = source;
	for (let attempt = 0; attempt < 5; attempt++) {
		const blob = await canvasToBlob(canvas);
		if (blob.size <= MAX_PNG_BYTES) return blob;
		canvas = shrinkCanvas(canvas, 0.75);
	}
	throw new OversizedCaptureError();
}

/** Draws one video frame into a PNG blob sized to the video resolution. */
export async function videoToFrame(video: HTMLVideoElement): Promise<CapturedFrame> {
	const canvas = document.createElement('canvas');
	canvas.width = video.videoWidth;
	canvas.height = video.videoHeight;
	const ctx = canvas.getContext('2d');
	if (!ctx) throw new Error('canvas unavailable');
	ctx.fillStyle = '#000';
	ctx.fillRect(0, 0, canvas.width, canvas.height);
	ctx.drawImage(video, 0, 0);
	return {
		blob: await encodeCappedPng(canvas),
		width: canvas.width,
		height: canvas.height
	};
}

/** Crops a captured image blob to a natural-pixel rect. */
export async function cropBlob(
	blob: Blob,
	rect: { x: number; y: number; width: number; height: number }
): Promise<CapturedFrame> {
	const bitmap = await createImageBitmap(blob);
	try {
		const x = Math.max(0, Math.round(rect.x));
		const y = Math.max(0, Math.round(rect.y));
		const width = Math.max(1, Math.round(rect.width));
		const height = Math.max(1, Math.round(rect.height));
		const canvas = document.createElement('canvas');
		canvas.width = width;
		canvas.height = height;
		const ctx = canvas.getContext('2d');
		if (!ctx) throw new Error('canvas unavailable');
		ctx.drawImage(bitmap, x, y, width, height, 0, 0, width, height);
		return { blob: await canvasToBlob(canvas), width, height };
	} finally {
		bitmap.close();
	}
}
