<script lang="ts">
	// The release notice renders release notes with markdown-it, so the root
	// layout loads it after the first render instead of with every page.
	import { onMount, type Component } from 'svelte';

	let Notice = $state<Component | null>(null);

	onMount(() => {
		import('./ReleaseNotice.svelte')
			.then((module) => (Notice = module.default))
			.catch((error) => console.error('Failed to load the release notice', error));
	});
</script>

{#if Notice}
	<Notice />
{/if}
