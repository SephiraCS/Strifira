<script lang="ts">
	import { page } from '$app/state';
	import ShareWall from '$lib/features/shares/ShareWall.svelte';
	import { m } from '$lib/paraglide/messages.js';
	const slug = $derived(page.params.workspaceSlug ?? '');
</script>

<svelte:head><title>{m['shares.page_title']()} · {slug}</title></svelte:head>
{#key slug}<ShareWall {slug} />{/key}
