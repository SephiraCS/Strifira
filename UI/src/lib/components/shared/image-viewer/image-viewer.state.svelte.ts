export interface ImageViewerItem {
	/** Image source URL (protected workspace asset or signed public asset). */
	src: string;
	/** Alternative text, used for accessibility and as a filename hint. */
	alt?: string | null;
	/** Explicit download filename override. */
	filename?: string | null;
}

class ImageViewerState {
	current = $state<ImageViewerItem | null>(null);

	get isOpen(): boolean {
		return this.current !== null;
	}

	open(item: ImageViewerItem) {
		if (!item.src) return;
		this.current = { ...item };
	}

	close() {
		this.current = null;
	}
}

export const imageViewerState = new ImageViewerState();
