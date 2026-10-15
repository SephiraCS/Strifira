<script lang="ts">
	import { onMount } from 'svelte';
	import '@scalar/api-reference/style.css';
	import { createApiReference } from '@scalar/api-reference';

	// The Scalar reference is a self-contained app mounted imperatively into
	// this CSR-only route. It renders the OpenAPI document the Go backend
	// serves same-origin at /api/openapi.json; the backend's parity test
	// keeps that document in lockstep with the registered routes.
	let container: HTMLDivElement | undefined;

	onMount(() => {
		if (!container) return;
		const instance = createApiReference(container, {
			url: '/api/openapi.json',
			darkMode: true,
			hideClientButton: true
		});
		return () => instance.destroy();
	});
</script>

<svelte:head>
	<title>API Reference – Strifira</title>
</svelte:head>

<div bind:this={container} class="h-screen w-screen overflow-hidden" data-testid="api-docs-page"></div>
