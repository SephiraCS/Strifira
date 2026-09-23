import { appToast } from '$lib/features/toast/toast';
import { m } from '$lib/paraglide/messages.js';
import { imageFilename } from './image-viewer';
import { imageViewerState, type ImageViewerItem } from './image-viewer.state.svelte';

export type ImageSource = {
	src: string;
	alt?: string | null;
	filename?: string | null;
};

type FetchedImage = {
	blob: Blob;
	contentType: string | null;
	disposition: string | null;
	filename: string;
};

/** Resolves a possibly relative asset path into an absolute URL. */
export function absoluteUrl(src: string): string {
	if (/^(https?:|data:|blob:)/i.test(src)) return src;
	if (typeof window === 'undefined') return src;
	try {
		return new URL(src, window.location.origin).href;
	} catch {
		return src;
	}
}

/** Adds `download=1` so the API replies with an attachment disposition. */
export function downloadHref(src: string): string {
	if (/^https?:/i.test(src) || !src.startsWith('/api/')) return src;
	return src.includes('?') ? `${src}&download=1` : `${src}?download=1`;
}

/** Opens the full-screen preview for a rendered image. */
export function openImagePreview(item: ImageViewerItem) {
	imageViewerState.open(item);
}

/**
 * Fetches the image bytes so download/copy work for both protected workspace
 * assets and token-signed public assets, regardless of CORS quirks.
 */
export async function fetchImage(item: ImageSource): Promise<FetchedImage> {
	const response = await fetch(item.src, { credentials: 'include' });
	if (!response.ok) throw new Error(`HTTP ${response.status}`);

	const blob = await response.blob();
	if (blob.size === 0) throw new Error('Empty image response');

	const headerType = response.headers.get('content-type');
	const contentType = headerType?.startsWith('image/') ? headerType : blob.type || headerType;
	const disposition = response.headers.get('content-disposition');
	const filename = imageFilename({
		explicit: item.filename,
		src: item.src,
		alt: item.alt,
		contentType,
		disposition
	});

	return { blob, contentType, disposition, filename };
}

function triggerDownload(href: string, filename: string) {
	const anchor = document.createElement('a');
	anchor.href = href;
	anchor.download = filename;
	anchor.rel = 'noopener';
	anchor.style.display = 'none';
	document.body.appendChild(anchor);
	anchor.click();
	anchor.remove();
}

/** Downloads the image, falling back to a direct asset link when fetch fails. */
export async function downloadImage(item: ImageSource): Promise<void> {
	let objectUrl: string | null = null;
	try {
		const { blob, filename } = await fetchImage(item);
		objectUrl = URL.createObjectURL(blob);
		triggerDownload(objectUrl, filename);
	} catch {
		try {
			const filename = imageFilename({ explicit: item.filename, src: item.src, alt: item.alt });
			triggerDownload(downloadHref(item.src), filename);
		} catch {
			appToast.error(m['imageViewer.download_failed']());
		}
	} finally {
		if (objectUrl) {
			const url = objectUrl;
			setTimeout(() => URL.revokeObjectURL(url), 10_000);
		}
	}
}

async function loadDecodable(blob: Blob): Promise<ImageBitmap | HTMLImageElement> {
	if (typeof createImageBitmap === 'function') {
		try {
			return await createImageBitmap(blob);
		} catch {
			// Some browsers cannot decode SVG/AVIF through createImageBitmap;
			// fall back to an <img> element below.
		}
	}

	const url = URL.createObjectURL(blob);
	try {
		return await new Promise<HTMLImageElement>((resolve, reject) => {
			const image = new Image();
			image.onload = () => resolve(image);
			image.onerror = () => reject(new Error('Image decode failed'));
			image.src = url;
		});
	} finally {
		setTimeout(() => URL.revokeObjectURL(url), 10_000);
	}
}

async function blobToPng(blob: Blob): Promise<Blob> {
	if (blob.type === 'image/png') return blob;

	const source = await loadDecodable(blob);
	const width = 'naturalWidth' in source ? source.naturalWidth : source.width;
	const height = 'naturalHeight' in source ? source.naturalHeight : source.height;
	if (!width || !height) throw new Error('Image has no dimensions');

	const canvas = document.createElement('canvas');
	canvas.width = width;
	canvas.height = height;
	const context = canvas.getContext('2d');
	if (!context) throw new Error('Canvas is unavailable');
	context.drawImage(source as CanvasImageSource, 0, 0);
	if ('close' in source && typeof source.close === 'function') source.close();

	const png = await new Promise<Blob | null>((resolve) => canvas.toBlob(resolve, 'image/png'));
	if (!png) throw new Error('PNG encoding failed');
	return png;
}

/** Copies the image bitmap to the clipboard, falling back to its link. */
export async function copyImage(item: ImageSource): Promise<void> {
	try {
		const { blob } = await fetchImage(item);
		const png = await blobToPng(blob);
		if (typeof ClipboardItem === 'undefined' || !navigator.clipboard?.write) {
			throw new Error('Clipboard image writing is unsupported');
		}
		await navigator.clipboard.write([new ClipboardItem({ 'image/png': png })]);
		appToast.success(m['imageViewer.copied']());
	} catch {
		await copyImageLink(item.src);
	}
}

/** Copies the absolute image URL to the clipboard. */
export async function copyImageLink(src: string): Promise<void> {
	try {
		if (!navigator.clipboard?.writeText) throw new Error('Clipboard is unavailable');
		await navigator.clipboard.writeText(absoluteUrl(src));
		appToast.info(m['imageViewer.link_copied']());
	} catch {
		appToast.error(m['imageViewer.copy_failed']());
	}
}

/** Opens the raw image in a new browser tab. */
export function openImageInNewTab(src: string) {
	window.open(absoluteUrl(src), '_blank', 'noopener,noreferrer');
}
