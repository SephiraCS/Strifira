// Pure zoom math for the focused share stage: the automatic legibility zoom
// applied when a share takes the stage, and the pan limits that keep the
// picture covering the stage while the user pans or zooms. Keep this module
// free of DOM/Svelte imports so it can be exercised by the node:test suite.

export const SHARE_ZOOM_CAP = 2;
export const SHARE_ZOOM_STEP = 0.25;

/**
 * The zoom applied automatically when a share takes the stage: scale up just
 * enough to fill the stage width — letterboxed sides are the wasted space —
 * capped so extreme aspect ratios never zoom absurdly. Streams that already
 * fill the width stay at 1.
 */
export function autoZoomScale(
	stageAspect: number,
	videoAspect: number,
	cap: number = SHARE_ZOOM_CAP
): number {
	if (
		!Number.isFinite(stageAspect) ||
		!Number.isFinite(videoAspect) ||
		stageAspect <= 0 ||
		videoAspect <= 0
	) {
		return 1;
	}
	if (videoAspect >= stageAspect) return 1;
	return Math.min(cap, stageAspect / videoAspect);
}

/**
 * How far the zoomed picture may pan on each axis while still covering the
 * stage. The picture is the letterboxed video content inside the stage
 * element, scaled around its center.
 */
export function panBounds(
	stageWidth: number,
	stageHeight: number,
	videoAspect: number,
	scale: number
): { maxX: number; maxY: number } {
	if (
		!Number.isFinite(stageWidth) ||
		!Number.isFinite(stageHeight) ||
		!Number.isFinite(videoAspect) ||
		stageWidth <= 0 ||
		stageHeight <= 0 ||
		videoAspect <= 0 ||
		!Number.isFinite(scale) ||
		scale <= 1
	) {
		return { maxX: 0, maxY: 0 };
	}
	const stageAspect = stageWidth / stageHeight;
	const contentWidth = videoAspect >= stageAspect ? stageWidth : stageHeight * videoAspect;
	const contentHeight = videoAspect >= stageAspect ? stageWidth / videoAspect : stageHeight;
	return {
		maxX: Math.max(0, (contentWidth * scale - stageWidth) / 2),
		maxY: Math.max(0, (contentHeight * scale - stageHeight) / 2)
	};
}

/** Clamps a pan offset so the zoomed picture keeps covering the stage. */
export function clampPan(
	x: number,
	y: number,
	bounds: { maxX: number; maxY: number }
): { x: number; y: number } {
	const clamp = (value: number, max: number) =>
		Number.isNaN(value) ? 0 : Math.min(max, Math.max(-max, value));
	return { x: clamp(x, bounds.maxX), y: clamp(y, bounds.maxY) };
}
