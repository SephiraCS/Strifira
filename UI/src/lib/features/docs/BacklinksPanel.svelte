<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { listBacklinks } from '$lib/api/docs';
	import type { DocBacklink } from '$lib/types/docs';
	import { m } from '$lib/paraglide/messages.js';
	import Link2 from '@lucide/svelte/icons/link-2';

	let {
		slug,
		docId,
		outgoingTitles = []
	}: { slug: string; docId: string; outgoingTitles?: string[] } = $props();

	let backlinks = $state<DocBacklink[]>([]);

	$effect(() => {
		void loadBacklinks();
	});

	async function loadBacklinks() {
		try {
			backlinks = await listBacklinks(slug, docId);
		} catch {
			backlinks = [];
		}
	}
</script>

{#if backlinks.length > 0 || outgoingTitles.length > 0}
	<section
		class="mt-10 border-t border-[var(--app-border)] pt-4"
		aria-label={m['docs.backlinks.title']()}
	>
		<h3 class="mb-2 flex items-center gap-1.5 text-xs font-semibold uppercase tracking-wide text-[var(--color-text-tertiary)]">
			<Link2 size={13} />
			{m['docs.backlinks.title']()}
		</h3>
		{#if outgoingTitles.length > 0}
			<p class="mb-1 text-xs text-[var(--color-text-tertiary)]">{m['docs.backlinks.links_out']()}</p>
			<div class="mb-3 flex flex-wrap gap-1.5">
				{#each outgoingTitles as title (title)}
					<span class="rounded-md border border-[var(--app-border)] bg-[var(--color-bg-secondary)] px-2 py-0.5 text-xs text-[var(--color-text-secondary)]">
						{title}
					</span>
				{/each}
			</div>
		{/if}
		{#if backlinks.length > 0}
			<p class="mb-1 text-xs text-[var(--color-text-tertiary)]">{m['docs.backlinks.mentions']()}</p>
			<div class="flex flex-wrap gap-1.5">
				{#each backlinks as backlink (backlink.id)}
					<button
						type="button"
						class="rounded-md border border-[var(--app-border)] bg-[var(--color-bg-secondary)] px-2 py-0.5 text-xs text-[var(--color-text-secondary)] hover:bg-[var(--color-bg-hover)] hover:text-[var(--color-text-primary)]"
						onclick={() => goto(`/${slug}/docs/${backlink.id}`)}
					>
						{backlink.icon ? backlink.icon + ' ' : ''}{backlink.title || m['docs.untitled']()}
					</button>
				{/each}
			</div>
		{/if}
	</section>
{/if}
