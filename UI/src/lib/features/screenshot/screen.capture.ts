/**
 * Screen, window and area capture over getDisplayMedia.
 *
 * The app is browser-only (SPA, adapter-static), so "screen" and "window"
 * modes cannot go native: they request a display surface from the platform
 * picker and grab a single frame. Chromium honors the displaySurface
 * preference; other browsers show their own picker and the user chooses.
 */
import {
	type CapturedFrame,
	nextFrame,
	playStream,
	stopStream,
	videoToFrame
} from './video-frame';

export type CaptureSurface = 'monitor' | 'window' | 'browser';

export function displayCaptureSupported(): boolean {
	return typeof navigator !== 'undefined' && !!navigator.mediaDevices?.getDisplayMedia;
}

export type CaptureFailure = 'cancelled' | 'unavailable' | 'failed';

/** Maps getDisplayMedia rejections to user-facing failure kinds. */
export function classifyCaptureError(err: unknown): CaptureFailure {
	const name = (err as { name?: string } | undefined)?.name ?? '';
	if (name === 'NotAllowedError' || name === 'AbortError') return 'cancelled';
	if (name === 'NotFoundError' || name === 'NotReadableError' || name === 'OverconstrainedError')
		return 'unavailable';
	return 'failed';
}

export function buildDisplayConstraints(surface?: CaptureSurface): DisplayMediaStreamOptions {
	const video: MediaTrackConstraints & { displaySurface?: string } = { frameRate: 30 };
	if (surface) video.displaySurface = surface;
	return {
		video,
		audio: false,
		selfBrowserSurface: 'include',
		surfaceSwitching: 'exclude'
	} as unknown as DisplayMediaStreamOptions;
}

export async function openDisplayStream(surface?: CaptureSurface): Promise<MediaStream> {
	return navigator.mediaDevices.getDisplayMedia(buildDisplayConstraints(surface));
}

/** Opens the picker, grabs one frame, and immediately stops the stream. */
export async function grabDisplayFrame(surface?: CaptureSurface): Promise<CapturedFrame> {
	const stream = await openDisplayStream(surface);
	try {
		const video = await playStream(stream);
		await nextFrame(video);
		return await videoToFrame(video);
	} finally {
		stopStream(stream);
	}
}
