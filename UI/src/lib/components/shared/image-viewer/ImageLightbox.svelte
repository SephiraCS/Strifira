<script lang="ts">
	import Copy from '@lucide/svelte/icons/copy';
	import Download from '@lucide/svelte/icons/download';
	import ExternalLink from '@lucide/svelte/icons/external-link';
	import ImageOff from '@lucide/svelte/icons/image-off';
	import Link2 from '@lucide/svelte/icons/link-2';
	import LoaderCircle from '@lucide/svelte/icons/loader-circle';
	import RotateCcw from '@lucide/svelte/icons/rotate-ccw';
	import X from '@lucide/svelte/icons/x';
	import ZoomIn from '@lucide/svelte/icons/zoom-in';
	import ZoomOut from '@lucide/svelte/icons/zoom-out';
	import { m } from '$lib/paraglide/messages.js';
	import { imageViewerState } from './image-viewer.state.svelte';
	import {
		MAX_SCALE,
		MIN_SCALE,
		ZOOM_STEP,
		anchoredOffset,
		formatZoomPercent,
		imageFilename,
		zoomScale
	} from './image-viewer';
	import { copyImage, copyImageLink, downloadImage, openImageInNewTab } from './image-actions';

	let scale = $state(1);
	let offsetX = $state(0);
	let offsetY = $state(0);
	let loading = $state(false);
	let failed = $state(false);
	let panning = $state(false);
	let busy = $state(false);
	let stage = $state<HTMLDivElement | null>(null);
	let imageEl = $state<HTMLImageElement | null>(null);
	let dialog = $state<HTMLDivElement | null>(null);

	let drag: { startX: number; startY: number; baseX: number; baseY: number; moved: boolean } | null = null;

	const current = $derived(imageViewerState.current);
	const isOpen = $derived(current !== null);
	const zoomLabel = $derived(formatZoomPercent(scale));
	const canZoomIn = $derived(scale < MAX_SCALE - 0.0001);
	const canZoomOut = $derived(scale > MIN_SCALE + 0.0001);
	const canPan = $derived(scale > 1.0001);
	const filename = $derived(
		current ? imageFilename({ explicit: current.filename, src: current.src, alt: current.alt }) : ''
	);

	function resetTransform() {
		scale = 1;
		offsetX = 0;
		offsetY = 0;
	}

	function finishLoading(ok: boolean) {
		loading = false;
		failed = !ok;
	}

	// Reset the transform and loading state whenever a different image opens.
	$effect(() => {
		const src = current?.src;
		if (!src) return;
		resetTransform();
		failed = false;
		loading = true;
		if (imageEl?.complete) finishLoading(imageEl.naturalWidth > 0);
	});

	// Lock page scrolling while the viewer is open.
	$effect(() => {
		if (!isOpen) return;
		const previousOverflow = document.body.style.overflow;
		document.body.style.overflow = 'hidden';
		return () => {
			document.body.style.overflow = previousOverflow;
		};
	});

	// Move focus into the viewer so keyboard shortcuts work immediately.
	$effect(() => {
		if (isOpen && dialog) dialog.focus({ preventScroll: true });
	});

	function close() {
		imageViewerState.close();
	}

	function applyZoom(next: number, pointerX: number, pointerY: number) {
		const element = stage;
		if (!element) {
			scale = next;
			return;
		}
		const rect = element.getBoundingClientRect();
		const offset = anchoredOffset({
			offsetX,
			offsetY,
			scale,
			nextScale: next,
			pointerX,
			pointerY,
			centerX: rect.left + rect.width / 2,
			centerY: rect.top + rect.height / 2
		});
		scale = next;
		offsetX = offset.x;
		offsetY = offset.y;
	}

	function zoomByStep(delta: number) {
		const next = zoomScale(scale, delta);
		const element = stage;
		if (!element) {
			scale = next;
			return;
		}
		const rect = element.getBoundingClientRect();
		applyZoom(next, rect.left + rect.width / 2, rect.top + rect.height / 2);
	}

	function handleWheel(event: WheelEvent) {
		if (!isOpen) return;
		event.preventDefault();
		applyZoom(zoomScale(scale, event.deltaY < 0 ? ZOOM_STEP : -ZOOM_STEP), event.clientX, event.clientY);
	}

	function handleDoubleClick(event: MouseEvent) {
		if (failed) return;
		if (canPan) resetTransform();
		else applyZoom(Math.min(2, MAX_SCALE), event.clientX, event.clientY);
	}

	function handlePointerDown(event: PointerEvent) {
		if (event.target === stage) {
			close();
			return;
		}
		if (event.button !== 0 || !canPan) return;
		drag = { startX: event.clientX, startY: event.clientY, baseX: offsetX, baseY: offsetY, moved: false };
		panning = true;
		(event.currentTarget as HTMLElement).setPointerCapture?.(event.pointerId);
	}

	function handlePointerMove(event: PointerEvent) {
		if (!drag) return;
		const dx = event.clientX - drag.startX;
		const dy = event.clientY - drag.startY;
		if (Math.abs(dx) > 3 || Math.abs(dy) > 3) drag.moved = true;
		offsetX = drag.baseX + dx;
		offsetY = drag.baseY + dy;
	}

	function handlePointerUp(event: PointerEvent) {
		if (!drag) return;
		drag = null;
		panning = false;
		const target = event.currentTarget as HTMLElement;
		if (target.hasPointerCapture?.(event.pointerId)) target.releasePointerCapture(event.pointerId);
	}

	// Keys the viewer acts on are handled in the capture phase and stopped, so
	// they never reach an underlying dialog: Escape would otherwise also close
	// e.g. a create-issue dialog stacked below the viewer, discarding the draft.
	$effect(() => {
		if (!isOpen) return;
		const onKeyDown = (event: KeyboardEvent) => {
			let handled = false;
			switch (event.key) {
				case 'Escape':
					close();
					handled = true;
					break;
				case '+':
				case '=':
					zoomByStep(ZOOM_STEP);
					handled = true;
					break;
				case '-':
				case '_':
					zoomByStep(-ZOOM_STEP);
					handled = true;
					break;
				case '0':
					resetTransform();
					handled = true;
					break;
				case 'ArrowLeft':
					if (canPan) {
						offsetX += 50;
						handled = true;
					}
					break;
				case 'ArrowRight':
					if (canPan) {
						offsetX -= 50;
						handled = true;
					}
					break;
				case 'ArrowUp':
					if (canPan) {
						offsetY += 50;
						handled = true;
					}
					break;
				case 'ArrowDown':
					if (canPan) {
						offsetY -= 50;
						handled = true;
					}
					break;
			}
			if (handled) {
				event.preventDefault();
				event.stopPropagation();
			}
		};
		window.addEventListener('keydown', onKeyDown, { capture: true });
		return () => window.removeEventListener('keydown', onKeyDown, { capture: true });
	});

	async function handleDownload() {
		if (!current || busy) return;
		busy = true;
		try {
			await downloadImage({ src: current.src, alt: current.alt, filename: current.filename });
		} finally {
			busy = false;
		}
	}

	async function handleCopy() {
		if (!current || busy) return;
		busy = true;
		try {
			await copyImage({ src: current.src, alt: current.alt, filename: current.filename });
		} finally {
			busy = false;
		}
	}

	async function handleCopyLink() {
		if (!current || busy) return;
		busy = true;
		try {
			await copyImageLink(current.src);
		} finally {
			busy = false;
		}
	}

	function handleOpen() {
		if (current) openImageInNewTab(current.src);
	}
</script>

{#if isOpen && current}
	<!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
	<div
		class="image-viewer"
		role="dialog"
		aria-modal="true"
		aria-label={m['imageViewer.title']()}
		bind:this={dialog}
		tabindex="-1"
	>
		<!-- svelte-ignore a11y_no_static_element_interactions -->
		<div
			class="image-viewer__stage"
			class:can-pan={canPan}
			class:is-panning={panning}
			bind:this={stage}
			onwheel={handleWheel}
			onpointerdown={handlePointerDown}
			onpointermove={handlePointerMove}
			onpointerup={handlePointerUp}
			onpointercancel={handlePointerUp}
		>
			<!-- svelte-ignore a11y_no_static_element_interactions -->
			<img
				class="image-viewer__image"
				class:is-hidden={failed}
				src={current.src}
				alt={current.alt ?? ''}
				draggable="false"
				style="transform: translate3d({offsetX}px, {offsetY}px, 0) scale({scale});"
				bind:this={imageEl}
				onload={() => finishLoading(true)}
				onerror={() => finishLoading(false)}
				ondblclick={handleDoubleClick}
			/>
		</div>

		<div class="image-viewer__toolbar" role="toolbar" aria-label={m['imageViewer.actions']()}>
			<button
				type="button"
				class="image-viewer__button"
				onclick={() => zoomByStep(-ZOOM_STEP)}
				disabled={!canZoomOut || failed}
				title={m['imageViewer.zoom_out']()}
				aria-label={m['imageViewer.zoom_out']()}
			>
				<ZoomOut size={16} />
			</button>
			<button
				type="button"
				class="image-viewer__zoom"
				onclick={resetTransform}
				disabled={failed}
				title={m['imageViewer.reset_zoom']()}
				aria-label={m['imageViewer.zoom_level']({ percent: zoomLabel })}
			>
				{zoomLabel}
			</button>
			<button
				type="button"
				class="image-viewer__button"
				onclick={() => zoomByStep(ZOOM_STEP)}
				disabled={!canZoomIn || failed}
				title={m['imageViewer.zoom_in']()}
				aria-label={m['imageViewer.zoom_in']()}
			>
				<ZoomIn size={16} />
			</button>
			<button
				type="button"
				class="image-viewer__button"
				onclick={resetTransform}
				disabled={failed}
				title={m['imageViewer.reset_zoom']()}
				aria-label={m['imageViewer.reset_zoom']()}
			>
				<RotateCcw size={16} />
			</button>

			<span class="image-viewer__divider" aria-hidden="true"></span>

			<button
				type="button"
				class="image-viewer__button"
				onclick={handleDownload}
				disabled={busy || failed}
				title={m['imageViewer.download']()}
				aria-label={m['imageViewer.download']()}
			>
				<Download size={16} />
			</button>
			<button
				type="button"
				class="image-viewer__button"
				onclick={handleCopy}
				disabled={busy || failed}
				title={m['imageViewer.copy']()}
				aria-label={m['imageViewer.copy']()}
			>
				<Copy size={16} />
			</button>
			<button
				type="button"
				class="image-viewer__button"
				onclick={handleCopyLink}
				disabled={busy || failed}
				title={m['imageViewer.copy_link']()}
				aria-label={m['imageViewer.copy_link']()}
			>
				<Link2 size={16} />
			</button>
			<button
				type="button"
				class="image-viewer__button"
				onclick={handleOpen}
				disabled={failed}
				title={m['imageViewer.open_new_tab']()}
				aria-label={m['imageViewer.open_new_tab']()}
			>
				<ExternalLink size={16} />
			</button>

			<span class="image-viewer__divider" aria-hidden="true"></span>

			<button
				type="button"
				class="image-viewer__button"
				onclick={close}
				title={m['imageViewer.close']()}
				aria-label={m['imageViewer.close']()}
			>
				<X size={16} />
			</button>
		</div>

		{#if loading && !failed}
			<div class="image-viewer__status" role="status">
				<span class="image-viewer__spin" aria-hidden="true"><LoaderCircle size={18} /></span>
				<span>{m['imageViewer.loading']()}</span>
			</div>
		{:else if failed}
			<div class="image-viewer__status image-viewer__status--error" role="alert">
				<ImageOff size={18} />
				<span>{m['imageViewer.load_failed']()}</span>
			</div>
		{/if}

		{#if filename && !failed}
			<div class="image-viewer__caption" title={filename}>
				<span class="image-viewer__caption-name">{filename}</span>
				<span class="image-viewer__caption-zoom">{zoomLabel}</span>
			</div>
		{/if}
	</div>
{/if}

<style>
	.image-viewer {
		position: fixed;
		inset: 0;
		z-index: 200;
		background: color-mix(in srgb, #000 92%, transparent);
		outline: none;
		/* Re-enable interaction when an ancestor modal sets pointer-events: none. */
		pointer-events: auto;
		animation: imageViewerFade 120ms ease-out;
	}

	.image-viewer__stage {
		position: absolute;
		inset: 0;
		display: flex;
		align-items: center;
		justify-content: center;
		overflow: hidden;
		touch-action: none;
	}

	.image-viewer__stage.can-pan {
		cursor: grab;
	}

	.image-viewer__stage.can-pan.is-panning {
		cursor: grabbing;
	}

	.image-viewer__image {
		max-width: calc(100vw - 2rem);
		max-height: calc(100vh - 7rem);
		transform-origin: center center;
		user-select: none;
		-webkit-user-drag: none;
		border-radius: 4px;
		box-shadow: 0 24px 70px rgba(0, 0, 0, 0.55);
		will-change: transform;
	}

	.image-viewer__image.is-hidden {
		visibility: hidden;
	}

	.image-viewer__toolbar {
		position: absolute;
		top: 0.75rem;
		left: 50%;
		transform: translateX(-50%);
		z-index: 2;
		display: flex;
		align-items: center;
		gap: 0.125rem;
		max-width: calc(100vw - 1rem);
		padding: 0.25rem;
		border: 1px solid var(--app-border);
		border-radius: 999px;
		background: color-mix(in srgb, var(--color-bg-secondary) 92%, transparent);
		backdrop-filter: blur(10px);
		box-shadow: 0 10px 30px rgba(0, 0, 0, 0.4);
	}

	.image-viewer__button,
	.image-viewer__zoom {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		height: 30px;
		min-width: 30px;
		padding: 0 0.375rem;
		border: none;
		border-radius: 999px;
		background: transparent;
		color: var(--color-text-secondary);
		font-size: 0.75rem;
		font-variant-numeric: tabular-nums;
		cursor: pointer;
		transition:
			background-color 120ms ease,
			color 120ms ease;
	}

	.image-viewer__button:hover:not(:disabled),
	.image-viewer__zoom:hover:not(:disabled) {
		background: var(--color-bg-hover);
		color: var(--color-text-primary);
	}

	.image-viewer__button:disabled,
	.image-viewer__zoom:disabled {
		opacity: 0.4;
		cursor: default;
	}

	.image-viewer__button:focus-visible,
	.image-viewer__zoom:focus-visible {
		outline: 2px solid var(--app-accent);
		outline-offset: 1px;
	}

	.image-viewer__zoom {
		min-width: 52px;
		font-weight: 600;
	}

	.image-viewer__divider {
		width: 1px;
		height: 18px;
		margin: 0 0.25rem;
		background: var(--app-border);
	}

	.image-viewer__status {
		position: absolute;
		top: 50%;
		left: 50%;
		transform: translate(-50%, -50%);
		z-index: 2;
		display: inline-flex;
		align-items: center;
		gap: 0.5rem;
		padding: 0.5rem 0.875rem;
		border: 1px solid var(--app-border);
		border-radius: 999px;
		background: var(--color-bg-secondary);
		color: var(--color-text-secondary);
		font-size: 0.8125rem;
	}

	.image-viewer__status--error {
		color: #f87171;
	}

	.image-viewer__caption {
		position: absolute;
		bottom: 1rem;
		left: 50%;
		transform: translateX(-50%);
		z-index: 2;
		display: flex;
		align-items: center;
		gap: 0.5rem;
		max-width: calc(100vw - 2rem);
		padding: 0.25rem 0.75rem;
		border-radius: 999px;
		background: color-mix(in srgb, #000 55%, transparent);
		color: rgba(255, 255, 255, 0.85);
		font-size: 0.75rem;
	}

	.image-viewer__caption-name {
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.image-viewer__caption-zoom {
		color: rgba(255, 255, 255, 0.55);
		font-variant-numeric: tabular-nums;
	}

	.image-viewer__spin {
		display: inline-flex;
		animation: imageViewerSpin 1s linear infinite;
	}

	@keyframes imageViewerSpin {
		from {
			transform: rotate(0deg);
		}
		to {
			transform: rotate(360deg);
		}
	}

	@keyframes imageViewerFade {
		from {
			opacity: 0;
		}
		to {
			opacity: 1;
		}
	}
</style>
