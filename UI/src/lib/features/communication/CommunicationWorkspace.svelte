<script lang="ts">
	import type { Snippet } from 'svelte';
	import ShareWall from '$lib/features/shares/ShareWall.svelte';
	import { m } from '$lib/paraglide/messages.js';

	let { slug, screenSharing, children }: { slug: string; screenSharing: boolean; children: Snippet } = $props();
</script>

<svelte:head><title>{m['communication.title']()} · {slug}</title></svelte:head>

<div data-testid="communication-workspace" class="flex h-full min-h-0 flex-col">
	<h1 class="shrink-0 border-b border-[var(--app-border)] px-3 py-2 text-sm font-semibold">
		{m['communication.title']()}
	</h1>
	<div
		class="min-h-0 flex-1 {screenSharing
			? 'grid grid-rows-[minmax(12rem,40%)_minmax(0,1fr)] lg:grid-cols-[minmax(0,1.15fr)_minmax(20rem,1fr)] lg:grid-rows-1'
			: 'flex'}"
	>
		{#if screenSharing}
			<section
				aria-label={m['shares.title']()}
				class="min-h-0 min-w-0 border-b border-[var(--app-border)] lg:border-r lg:border-b-0"
			>
				<ShareWall {slug} />
			</section>
		{/if}
		<section
			aria-label={m['sidebar.chat']()}
			data-testid="communication-conversation"
			class="min-h-0 min-w-0 flex-1 overflow-hidden"
		>
			{@render children()}
		</section>
	</div>
</div>
