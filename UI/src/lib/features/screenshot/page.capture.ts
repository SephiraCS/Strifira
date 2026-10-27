/**
 * Full-page capture: scrolls the page's scroll container through its whole
 * content and stitches display-capture frames onto one canvas.
 *
 * The stream captures the current tab (preferCurrentTab where supported), so
 * nothing may be rendered in-page while chunks are taken: no progress
 * overlay, no toast — the scrolling itself is the feedback. Fixed and sticky
 * elements are hidden for the duration (they would repeat in every chunk)
 * and restored afterwards, along with the original scroll position.
 */
import { planCapture, type ScrollGeometry } from './geometry';
import { classifyCaptureError, openDisplayStream } from './screen.capture';
import {
	type CapturedFrame,
	nextFrame,
	playStream,
	stopStream,
	wait,
	encodeCappedPng
} from './video-frame';

const PRE_SCROLL_PAUSE_MS = 160;
const SETTLE_PAUSE_MS = 60;

interface HiddenElement {
	element: HTMLElement;
	previousStyle: string | null;
}

export async function captureFullPage(): Promise<CapturedFrame> {
	// displaySurface 'browser' asks the picker to highlight tabs; the user
	// confirms the source like in the screen/window modes, which keeps the
	// flow predictable in every browser that implements getDisplayMedia.
	const stream = await openDisplayStream('browser');
	let video: HTMLVideoElement | null = null;
	try {
		video = await playStream(stream);
		const container = findScrollContainer();
		const savedScroll = { x: container.scrollLeft, y: container.scrollTop };
		await preScroll(container);
		const hidden = hideOverlayElements();
		try {
			return await stitchChunks(container, video);
		} finally {
			restoreHidden(hidden);
			container.scrollTo(savedScroll.x, savedScroll.y);
		}
	} catch (err) {
		if (classifyCaptureError(err) === 'cancelled') {
			throw Object.assign(new Error('capture cancelled'), { name: 'NotAllowedError' });
		}
		throw err;
	} finally {
		stopStream(stream);
		if (video) video.srcObject = null;
	}
}

function geometryOf(container: HTMLElement): ScrollGeometry {
	return {
		viewportWidth: container.clientWidth,
		viewportHeight: container.clientHeight,
		contentWidth: container.scrollWidth,
		contentHeight: container.scrollHeight
	};
}

/**
 * The app scrolls inside layout containers (`overflow-auto` panes), not the
 * document, so pick whichever scroller actually overflows: the document when
 * it does, otherwise the inner container with the largest scrollable area.
 * Panes smaller than half the viewport are ignored (toolbar rows, chips) —
 * a page scroller is a large region. A page that fits one viewport falls
 * back to the document: the chunk plan is then a single full-viewport shot.
 */
function findScrollContainer(): HTMLElement {
	const doc = document.scrollingElement;
	if (doc && doc.scrollHeight - doc.clientHeight > 1) return doc as HTMLElement;
	let best: HTMLElement | null = null;
	let bestDelta = 0;
	for (const element of document.body.querySelectorAll<HTMLElement>('*')) {
		const style = getComputedStyle(element);
		if (element.clientWidth < window.innerWidth * 0.5) continue;
		if (element.clientHeight < window.innerHeight * 0.5) continue;
		const deltaY = element.scrollHeight - element.clientHeight;
		const deltaX = element.scrollWidth - element.clientWidth;
		const scrollsY = /(auto|scroll)/.test(style.overflowY) && deltaY > 1;
		const scrollsX = /(auto|scroll)/.test(style.overflowX) && deltaX > 1;
		if (!scrollsY && !scrollsX) continue;
		const delta = Math.max(deltaY, deltaX);
		if (delta > bestDelta) {
			best = element;
			bestDelta = delta;
		}
	}
	if (best) return best;
	return (doc as HTMLElement) ?? document.documentElement;
}

/** One fast pass through the content so lazy-loaded sections are present. */
async function preScroll(container: HTMLElement): Promise<void> {
	const step = Math.max(1, container.clientHeight - 40);
	const maxY = () => container.scrollHeight - container.clientHeight;
	let y = 0;
	while (y < maxY()) {
		y = Math.min(y + step, maxY());
		container.scrollTop = y;
		await wait(PRE_SCROLL_PAUSE_MS);
	}
	await wait(PRE_SCROLL_PAUSE_MS);
	container.scrollTop = 0;
	await wait(80);
}

/** Hides fixed/sticky elements; they would otherwise repeat in every chunk. */
function hideOverlayElements(): HiddenElement[] {
	const hidden: HiddenElement[] = [];
	for (const element of document.body.querySelectorAll<HTMLElement>('*')) {
		const position = getComputedStyle(element).position;
		if (position !== 'fixed' && position !== 'sticky') continue;
		const rect = element.getBoundingClientRect();
		if (rect.width === 0 || rect.height === 0) continue;
		hidden.push({ element, previousStyle: element.getAttribute('style') });
		element.style.setProperty('visibility', 'hidden');
	}
	return hidden;
}

function restoreHidden(hidden: HiddenElement[]): void {
	for (const { element, previousStyle } of hidden) {
		if (previousStyle === null) element.removeAttribute('style');
		else element.setAttribute('style', previousStyle);
	}
}

async function stitchChunks(
	container: HTMLElement,
	video: HTMLVideoElement
): Promise<CapturedFrame> {
	const plan = planCapture(geometryOf(container), window.devicePixelRatio || 1);
	const canvas = document.createElement('canvas');
	canvas.width = plan.canvasWidth;
	canvas.height = plan.canvasHeight;
	const ctx = canvas.getContext('2d');
	if (!ctx) throw new Error('canvas unavailable');
	ctx.fillStyle = '#000';
	ctx.fillRect(0, 0, canvas.width, canvas.height);

	let cancelled = false;
	const track = video.srcObject instanceof MediaStream ? video.srcObject.getVideoTracks()[0] : null;
	const onEnded = () => (cancelled = true);
	track?.addEventListener('ended', onEnded);

	try {
		for (const chunk of plan.chunks) {
			container.scrollTo(chunk.scrollX, chunk.scrollY);
			await nextFrame(video);
			await wait(SETTLE_PAUSE_MS);
			if (cancelled) {
				const err = new Error('capture cancelled');
				err.name = 'NotAllowedError';
				throw err;
			}
			// The video frame covers the whole viewport, so frame pixels scale
			// with the window (not the container): container rect maps the
			// container into viewport coordinates, then the frame ratio
			// (video px per CSS px) maps those into the frame.
			const rect = container.getBoundingClientRect();
			const ratio = video.videoWidth / window.innerWidth;
			const sx = rect.left * ratio + (chunk.contentX - chunk.scrollX) * ratio;
			const sy = rect.top * ratio + (chunk.contentY - chunk.scrollY) * ratio;
			ctx.drawImage(
				video,
				sx,
				sy,
				chunk.contentWidth * ratio,
				chunk.contentHeight * ratio,
				chunk.contentX * plan.scale,
				chunk.contentY * plan.scale,
				chunk.contentWidth * plan.scale,
				chunk.contentHeight * plan.scale
			);
		}
	} finally {
		track?.removeEventListener('ended', onEnded);
	}

	return {
		blob: await encodeCappedPng(canvas),
		width: canvas.width,
		height: canvas.height
	};
}
