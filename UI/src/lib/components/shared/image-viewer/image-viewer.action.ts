import { m } from '$lib/paraglide/messages.js';
import { openImagePreview } from './image-actions';

export interface ImageViewerOptions {
	/** When false, images inside the node are left untouched. */
	enabled?: boolean;
}

const ENHANCED_FLAG = 'imageViewerEnhanced';

/**
 * Delegated enhancement for images rendered through `{@html}`. It makes every
 * previewable image focusable and opens the full-screen viewer on click or
 * Enter/Space. Images wrapped in a link keep their link behaviour.
 */
function normalizeOptions(options: ImageViewerOptions = {}): Required<ImageViewerOptions> {
	return { enabled: options.enabled !== false };
}

export function imageViewer(node: HTMLElement, options: ImageViewerOptions = {}) {
	let currentOptions = normalizeOptions(options);
	let observer: MutationObserver | null = null;

	function imageFrom(target: EventTarget | null): HTMLImageElement | null {
		if (!(target instanceof Element)) return null;
		const image = target.closest('img');
		if (!image || !node.contains(image)) return null;
		return image;
	}

	function isPreviewable(image: HTMLImageElement): boolean {
		if (!image.getAttribute('src')) return false;
		if (image.dataset.noImageViewer !== undefined) return false;
		if (image.closest('a[href]')) return false;
		return true;
	}

	function enhance(image: HTMLImageElement) {
		if (!currentOptions.enabled || image.dataset[ENHANCED_FLAG] === 'true') return;
		image.dataset[ENHANCED_FLAG] = 'true';
		image.classList.add('image-viewer-trigger');
		image.setAttribute('role', 'button');
		image.tabIndex = 0;
		const alt = image.alt?.trim();
		image.setAttribute('aria-label', alt ? m['imageViewer.view_named']({ name: alt }) : m['imageViewer.view']());
	}

	function enhanceAll() {
		node.querySelectorAll<HTMLImageElement>('img').forEach(enhance);
	}

	function resetImage(image: HTMLImageElement) {
		delete image.dataset[ENHANCED_FLAG];
		image.classList.remove('image-viewer-trigger');
		image.removeAttribute('role');
		image.removeAttribute('aria-label');
		image.removeAttribute('tabindex');
	}

	function clearEnhancements() {
		node.querySelectorAll<HTMLImageElement>('img').forEach(resetImage);
	}

	function open(image: HTMLImageElement) {
		const src = image.getAttribute('src') || image.currentSrc || image.src;
		if (!src) return;
		openImagePreview({ src, alt: image.alt || null });
	}

	function handleClick(event: MouseEvent) {
		if (event.defaultPrevented || event.button !== 0) return;
		if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
		const image = imageFrom(event.target);
		if (!image || !isPreviewable(image)) return;
		event.preventDefault();
		event.stopPropagation();
		open(image);
	}

	function handleKeyDown(event: KeyboardEvent) {
		if (event.key !== 'Enter' && event.key !== ' ') return;
		const image = imageFrom(event.target);
		if (!image || event.target !== image || !isPreviewable(image)) return;
		event.preventDefault();
		open(image);
	}

	function bind() {
		observer = new MutationObserver(enhanceAll);
		observer.observe(node, { childList: true, subtree: true });
		enhanceAll();
	}

	function unbind() {
		observer?.disconnect();
		observer = null;
	}

	node.addEventListener('click', handleClick);
	node.addEventListener('keydown', handleKeyDown);
	if (currentOptions.enabled) bind();

	return {
		update(newOptions: ImageViewerOptions = {}) {
			const wasEnabled = currentOptions.enabled;
			currentOptions = normalizeOptions(newOptions);
			if (currentOptions.enabled) {
				if (!wasEnabled) bind();
				else enhanceAll();
			} else {
				unbind();
				clearEnhancements();
			}
		},
		destroy() {
			unbind();
			node.removeEventListener('click', handleClick);
			node.removeEventListener('keydown', handleKeyDown);
			clearEnhancements();
		}
	};
}
