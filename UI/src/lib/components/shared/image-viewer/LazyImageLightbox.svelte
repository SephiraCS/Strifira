<script module lang="ts">
	// The lightbox is only needed once someone opens an image, so it is
	// fetched on first open (or when the browser is idle) rather than with
	// the root layout. Every instance shares one request.
	let loading: Promise<typeof import('./ImageLightbox.svelte')> | undefined;

	function loadLightbox() {
		loading ??= import('./ImageLightbox.svelte').catch((error) => {
			loading = undefined;
			throw error;
		});
		return loading;
	}
</script>

<script lang="ts">
	import { onMount, type Component } from 'svelte';
	import { imageViewerState } from './image-viewer.state.svelte';

	let Lightbox = $state<Component | null>(null);

	function load() {
		loadLightbox()
			.then((module) => (Lightbox = module.default))
			.catch((error) => {
				console.error('Failed to load the image viewer', error);
				imageViewerState.close();
			});
	}

	onMount(() => {
		if (typeof requestIdleCallback !== 'function') return;
		const handle = requestIdleCallback(load, { timeout: 5000 });
		return () => cancelIdleCallback(handle);
	});

	$effect(() => {
		if (imageViewerState.current && !Lightbox) load();
	});
</script>

{#if Lightbox}
	<Lightbox />
{/if}
