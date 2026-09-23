// Pure helpers shared by the image viewer UI, the delegated click action and
// the rich text editor. Keep this module free of DOM/Svelte imports so it can
// be exercised by the node:test unit suite.

export const MIN_SCALE = 0.25;
export const MAX_SCALE = 8;
export const ZOOM_STEP = 0.25;

const DEFAULT_FILENAME = 'image';
const DEFAULT_EXTENSION = 'png';

const CONTENT_TYPE_EXTENSIONS: Record<string, string> = {
	'image/png': 'png',
	'image/jpeg': 'jpg',
	'image/gif': 'gif',
	'image/webp': 'webp',
	'image/svg+xml': 'svg',
	'image/avif': 'avif',
	'image/bmp': 'bmp'
};

/** Restricts a zoom factor to the supported range and rejects NaN/Infinity. */
export function clampScale(value: number): number {
	if (!Number.isFinite(value)) return 1;
	return Math.min(MAX_SCALE, Math.max(MIN_SCALE, value));
}

/** Applies a relative zoom step, clamped to the supported range. */
export function zoomScale(current: number, delta: number): number {
	return clampScale((Number.isFinite(current) ? current : 1) + delta);
}

/** Human readable zoom percentage, e.g. `150%`. */
export function formatZoomPercent(scale: number): string {
	return `${Math.round(clampScale(scale) * 100)}%`;
}

/**
 * Keeps the image point under the pointer fixed while the zoom factor changes.
 * `pointer*` and `center*` are viewport coordinates; `offset*` is the current
 * translation of the centered image.
 */
export function anchoredOffset(input: {
	offsetX: number;
	offsetY: number;
	scale: number;
	nextScale: number;
	pointerX: number;
	pointerY: number;
	centerX: number;
	centerY: number;
}): { x: number; y: number } {
	const { offsetX, offsetY, scale, nextScale, pointerX, pointerY, centerX, centerY } = input;
	if (!Number.isFinite(scale) || !Number.isFinite(nextScale) || scale <= 0 || nextScale <= 0) {
		return { x: offsetX, y: offsetY };
	}
	const dx = pointerX - centerX;
	const dy = pointerY - centerY;
	return {
		x: dx - ((dx - offsetX) / scale) * nextScale,
		y: dy - ((dy - offsetY) / scale) * nextScale
	};
}

function safeDecode(value: string): string {
	try {
		return decodeURIComponent(value);
	} catch {
		return value;
	}
}

/** Strips control characters and path separators from a candidate filename. */
export function sanitizeFilename(value?: string | null): string {
	if (!value) return '';
	return value
		.replace(/[\u0000-\u001f\u007f]/g, '')
		.replace(/[\\/]+/g, '-')
		.trim()
		.replace(/^\.+/, '')
		.slice(0, 200)
		.trim();
}

/** Maps an `image/*` content type to a file extension. */
export function extensionForContentType(contentType?: string | null): string | null {
	if (!contentType) return null;
	const normalized = contentType.split(';')[0]?.trim().toLowerCase() ?? '';
	if (!normalized) return null;
	const mapped = CONTENT_TYPE_EXTENSIONS[normalized];
	if (mapped) return mapped;
	if (!normalized.startsWith('image/')) return null;
	const subtype = normalized.slice('image/'.length);
	if (!/^[a-z0-9.+-]+$/.test(subtype)) return null;
	return subtype === 'jpeg' ? 'jpg' : subtype;
}

/** Extracts `filename` (including RFC 5987 `filename*`) from a Content-Disposition header. */
export function filenameFromDisposition(disposition?: string | null): string | null {
	if (!disposition) return null;

	const encoded = /filename\*\s*=\s*([^;]+)/i.exec(disposition);
	if (encoded) {
		let value = encoded[1].trim().replace(/^["']|["']$/g, '');
		const separator = value.indexOf("''");
		if (separator !== -1) value = value.slice(separator + 2);
		const decoded = sanitizeFilename(safeDecode(value));
		if (decoded) return decoded;
	}

	const plain = /filename\s*=\s*(?:"([^"]*)"|([^;]+))/i.exec(disposition);
	if (plain) {
		const value = sanitizeFilename((plain[1] ?? plain[2] ?? '').trim());
		if (value) return value;
	}

	return null;
}

function hasExtension(name: string): boolean {
	return /\.[a-z0-9]{1,8}$/i.test(name);
}

/** Derives a filename from the last path segment when it carries an extension. */
export function filenameFromUrl(src?: string | null): string | null {
	if (!src) return null;
	let pathname = src;
	try {
		pathname = new URL(src, 'http://localhost').pathname;
	} catch {
		// Fall back to the raw string when it is not a parseable URL.
	}
	const segment = pathname.split('/').filter(Boolean).pop();
	if (!segment) return null;
	const decoded = sanitizeFilename(safeDecode(segment));
	if (!decoded || !hasExtension(decoded)) return null;
	return decoded;
}

function withExtension(name: string, contentType?: string | null, src?: string | null): string {
	if (hasExtension(name)) return name;
	const fromType = extensionForContentType(contentType);
	if (fromType) return `${name}.${fromType}`;
	const fromUrl = filenameFromUrl(src);
	if (fromUrl) {
		const extension = fromUrl.slice(fromUrl.lastIndexOf('.') + 1);
		return `${name}.${extension}`;
	}
	return `${name}.${DEFAULT_EXTENSION}`;
}

/**
 * Best-effort download filename. Preference order: caller-provided name,
 * `Content-Disposition`, the image `alt` text (only when it looks like a file
 * name), the URL path, then a generic `image.<ext>`.
 */
export function imageFilename(options: {
	explicit?: string | null;
	src?: string | null;
	alt?: string | null;
	contentType?: string | null;
	disposition?: string | null;
}): string {
	const explicit = sanitizeFilename(options.explicit);
	if (explicit) return withExtension(explicit, options.contentType, options.src);

	const disposition = filenameFromDisposition(options.disposition);
	if (disposition) return withExtension(disposition, options.contentType, options.src);

	const fromUrl = filenameFromUrl(options.src);
	if (fromUrl) return fromUrl;

	const alt = sanitizeFilename(options.alt);
	if (alt && hasExtension(alt)) return alt;

	const extension = extensionForContentType(options.contentType) ?? DEFAULT_EXTENSION;
	return `${DEFAULT_FILENAME}.${extension}`;
}
