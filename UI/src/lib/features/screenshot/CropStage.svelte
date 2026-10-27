<script lang="ts">
	import { Button } from '$lib/components/ui/button';
	import { m } from '$lib/paraglide/messages.js';
	import { clampCropRect, normalizeRect, scaleCropRect, type CropRect } from './geometry';

	let {
		src,
		naturalWidth,
		naturalHeight,
		processing = false,
		onapply,
		oncancel
	}: {
		src: string;
		naturalWidth: number;
		naturalHeight: number;
		processing?: boolean;
		onapply: (rect: CropRect) => void;
		oncancel: () => void;
	} = $props();

	let stageEl = $state<HTMLElement | null>(null);
	let dragFrom = $state<{ x: number; y: number } | null>(null);
	let selection = $state<CropRect | null>(null);

	function localPoint(event: PointerEvent): { x: number; y: number } {
		const rect = stageEl?.getBoundingClientRect();
		if (!rect) return { x: 0, y: 0 };
		return {
			x: Math.max(0, Math.min(event.clientX - rect.left, rect.width)),
			y: Math.max(0, Math.min(event.clientY - rect.top, rect.height))
		};
	}

	function onPointerDown(event: PointerEvent) {
		if (processing) return;
		event.preventDefault();
		stageEl?.setPointerCapture(event.pointerId);
		dragFrom = localPoint(event);
		selection = null;
	}

	function onPointerMove(event: PointerEvent) {
		if (!dragFrom || processing) return;
		selection = normalizeRect(dragFrom, localPoint(event));
	}

	function onPointerUp() {
		if (!dragFrom) return;
		dragFrom = null;
		if (!selection || selection.width < 8 || selection.height < 8) selection = null;
	}

	const naturalSelection = $derived.by(() => {
		const rect = stageEl?.getBoundingClientRect();
		if (!selection || !rect || rect.width === 0) return null;
		return scaleCropRect(selection, naturalWidth / rect.width);
	});

	function apply() {
		if (!naturalSelection || processing) return;
		const clamped = clampCropRect(naturalSelection, naturalWidth, naturalHeight, 1);
		if (clamped) onapply(clamped);
	}

	function reset() {
		selection = null;
		dragFrom = null;
	}

	function handleKeydown(event: KeyboardEvent) {
		if (processing) return;
		if (event.key === 'Escape') {
			if (selection) reset();
			else oncancel();
		}
	}
</script>

<svelte:window onkeydown={handleKeydown} />

<div
	class="fixed inset-0 z-[200] flex flex-col bg-black/90"
	role="dialog"
	aria-modal="true"
	aria-label={m['screenshot.crop.title']()}
>
	<header class="flex shrink-0 items-center justify-between gap-3 px-4 py-3">
		<div class="min-w-0">
			<h2 class="text-sm font-medium text-white">{m['screenshot.crop.title']()}</h2>
			<p class="truncate text-xs text-white/60">{m['screenshot.crop.hint']()}</p>
		</div>
		<Button variant="ghost" size="sm" onclick={oncancel} disabled={processing} class="text-white hover:bg-white/10">
			{m['screenshot.preview.cancel']()}
		</Button>
	</header>

	<div class="flex min-h-0 flex-1 items-center justify-center px-4">
		<!-- svelte-ignore a11y_no_static_element_interactions -->
		<div
			bind:this={stageEl}
			class="relative inline-block leading-none select-none touch-none"
			class:cursor-crosshair={!processing}
			onpointerdown={onPointerDown}
			onpointermove={onPointerMove}
			onpointerup={onPointerUp}
			onpointercancel={onPointerUp}
		>
			<img
				{src}
				alt=""
				draggable="false"
				class="max-h-[70vh] max-w-[92vw] rounded-sm object-contain"
			/>
			{#if selection}
				<div
					class="pointer-events-none absolute border border-white"
					style="left: {selection.x}px; top: {selection.y}px; width: {selection.width}px; height: {selection.height}px; box-shadow: 0 0 0 100000px rgba(0, 0, 0, 0.55);"
				></div>
			{/if}
			{#if processing}
				<div class="absolute inset-0 flex items-center justify-center bg-black/50">
					<div
						class="h-8 w-8 animate-spin rounded-full border-2 border-white/30 border-t-white"
						aria-hidden="true"
					></div>
				</div>
			{/if}
		</div>
	</div>

	<footer class="flex shrink-0 items-center justify-between gap-2 px-4 py-3">
		<Button variant="outline" size="sm" onclick={reset} disabled={processing || !selection} class="border-white/30 bg-transparent text-white hover:bg-white/10">
			{m['screenshot.crop.reset']()}
		</Button>
		<Button size="sm" onclick={apply} disabled={processing || !naturalSelection}>
			{m['screenshot.crop.apply']()}
		</Button>
	</footer>
</div>
