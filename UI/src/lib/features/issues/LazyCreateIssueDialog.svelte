<script module lang="ts">
	// The dialog embeds the rich-text editor (tiptap, ProseMirror,
	// highlight.js: ~600 KB of JavaScript), so pages load it after they
	// render instead of with them. Every instance shares one request.
	let loading: Promise<typeof import('./CreateIssueDialog.svelte')> | undefined;

	function loadDialog() {
		loading ??= import('./CreateIssueDialog.svelte').catch((error) => {
			loading = undefined;
			throw error;
		});
		return loading;
	}
</script>

<script lang="ts">
	import { onMount, type ComponentProps } from 'svelte';
	import type CreateIssueDialog from './CreateIssueDialog.svelte';
	import { appToast } from '$lib/features/toast/toast';
	import { m } from '$lib/paraglide/messages.js';

	let { open = $bindable(false), ...props }: ComponentProps<typeof CreateIssueDialog> = $props();

	let Dialog = $state<typeof CreateIssueDialog | null>(null);

	function load() {
		loadDialog()
			.then((module) => (Dialog = module.default))
			.catch(() => {
				if (!open) return;
				open = false;
				appToast.error(m['sidebar.failed_create_issue']());
			});
	}

	onMount(load);

	$effect(() => {
		if (open && !Dialog) load();
	});
</script>

{#if Dialog}
	<Dialog bind:open {...props} />
{/if}
