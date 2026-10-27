/**
 * Pure geometry and naming helpers for the screenshot capture feature.
 *
 * Everything here is DOM-free so it runs in the node:test unit suite;
 * the capture pipeline itself lives in screen.capture.ts / page.capture.ts.
 */

export const MAX_CANVAS_SIDE = 16384;
export const MAX_CANVAS_AREA = 268435456;

export interface ScrollGeometry {
	viewportWidth: number;
	viewportHeight: number;
	contentWidth: number;
	contentHeight: number;
}

export interface CaptureChunk {
	scrollX: number;
	scrollY: number;
	contentX: number;
	contentY: number;
	contentWidth: number;
	contentHeight: number;
}

export interface CapturePlan {
	scale: number;
	canvasWidth: number;
	canvasHeight: number;
	chunks: CaptureChunk[];
}

export interface CropRect {
	x: number;
	y: number;
	width: number;
	height: number;
}

function fits(width: number, height: number, scale: number): boolean {
	const w = Math.ceil(width * scale);
	const h = Math.ceil(height * scale);
	return (
		w <= MAX_CANVAS_SIDE &&
		h <= MAX_CANVAS_SIDE &&
		w * h <= MAX_CANVAS_AREA
	);
}

/**
 * Plans the scroll-and-stitch chunks for a full-page capture. Chunks step by
 * the visible viewport and the final (partial) chunk clamps its scroll so the
 * visible region still lines up with its content coordinates.
 */
export function planCapture(geometry: ScrollGeometry, deviceScale: number): CapturePlan {
	const viewportWidth = Math.max(1, Math.floor(geometry.viewportWidth));
	const viewportHeight = Math.max(1, Math.floor(geometry.viewportHeight));
	const contentWidth = Math.max(viewportWidth, Math.floor(geometry.contentWidth));
	const contentHeight = Math.max(viewportHeight, Math.floor(geometry.contentHeight));

	let scale = Math.min(Math.max(deviceScale || 1, 0.25), 2);
	while (scale > 0.25 && !fits(contentWidth, contentHeight, scale)) scale /= 2;

	const chunks: CaptureChunk[] = [];
	const maxScrollX = contentWidth - viewportWidth;
	const maxScrollY = contentHeight - viewportHeight;
	for (let y = 0; y < contentHeight; y += viewportHeight) {
		const contentHeightChunk = Math.min(viewportHeight, contentHeight - y);
		const scrollY = Math.min(y, maxScrollY);
		for (let x = 0; x < contentWidth; x += viewportWidth) {
			const contentWidthChunk = Math.min(viewportWidth, contentWidth - x);
			const scrollX = Math.min(x, maxScrollX);
			chunks.push({
				scrollX,
				scrollY,
				contentX: x,
				contentY: y,
				contentWidth: contentWidthChunk,
				contentHeight: contentHeightChunk
			});
		}
	}

	return {
		scale,
		canvasWidth: Math.ceil(contentWidth * scale),
		canvasHeight: Math.ceil(contentHeight * scale),
		chunks
	};
}

export function normalizeRect(
	a: { x: number; y: number },
	b: { x: number; y: number }
): CropRect {
	return {
		x: Math.min(a.x, b.x),
		y: Math.min(a.y, b.y),
		width: Math.abs(b.x - a.x),
		height: Math.abs(b.y - a.y)
	};
}

/**
 * Clamps a crop to the image bounds. Returns null when the surviving region
 * is smaller than minSize (a stray click, not a real selection).
 */
export function clampCropRect(
	rect: CropRect,
	imageWidth: number,
	imageHeight: number,
	minSize = 8
): CropRect | null {
	const left = Math.max(0, Math.min(rect.x, imageWidth));
	const top = Math.max(0, Math.min(rect.y, imageHeight));
	const right = Math.max(0, Math.min(rect.x + rect.width, imageWidth));
	const bottom = Math.max(0, Math.min(rect.y + rect.height, imageHeight));
	const width = right - left;
	const height = bottom - top;
	if (width < minSize || height < minSize) return null;
	return { x: left, y: top, width, height };
}

export function scaleCropRect(rect: CropRect, factor: number): CropRect {
	return {
		x: rect.x * factor,
		y: rect.y * factor,
		width: rect.width * factor,
		height: rect.height * factor
	};
}

export function screenshotFilename(now: Date = new Date()): string {
	const p = (value: number, size = 2) => String(value).padStart(size, '0');
	const date = `${now.getFullYear()}${p(now.getMonth() + 1)}${p(now.getDate())}`;
	const time = `${p(now.getHours())}${p(now.getMinutes())}${p(now.getSeconds())}`;
	return `screenshot-${date}-${time}.png`;
}

export function escapeHtmlAttribute(value: string): string {
	return value
		.replaceAll('&', '&amp;')
		.replaceAll('<', '&lt;')
		.replaceAll('>', '&gt;')
		.replaceAll('"', '&quot;');
}

/** Comment body that embeds an uploaded asset the same way the editor does. */
export function commentBodyForAsset(url: string, filename: string): string {
	return `<p><img src="${escapeHtmlAttribute(url)}" alt="${escapeHtmlAttribute(filename)}"></p>`;
}
