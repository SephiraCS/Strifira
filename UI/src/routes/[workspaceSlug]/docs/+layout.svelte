<script lang="ts">
	import { page } from '$app/state';
	import DocsSidebar from '$lib/features/docs/DocsSidebar.svelte';

	let { children } = $props();

	const slug = $derived(page.params.workspaceSlug ?? '');
</script>

<div class="flex h-full min-h-0">
	<div class="hidden md:block">
		<DocsSidebar {slug} />
	</div>
	<div class="min-w-0 flex-1 overflow-y-auto">
		{@render children()}
	</div>
</div>
