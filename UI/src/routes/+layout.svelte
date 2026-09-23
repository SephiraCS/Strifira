<script lang="ts">
	import '../app.css';
	import { Toaster } from 'svelte-sonner';
	import { onMount } from 'svelte';
	import ReleaseNotice from '$lib/components/shared/ReleaseNotice.svelte';
	import { ImageLightbox } from '$lib/components/shared/image-viewer';
	import { preferencesState } from '$lib/features/preferences/preferences.state.svelte';
	import { m } from '$lib/paraglide/messages.js';
	import { getLocale, setLocale } from '$lib/paraglide/runtime.js';

	let { children } = $props();

	onMount(() => {
		preferencesState.init();
	});
</script>

<Toaster position="bottom-right" theme={preferencesState.resolvedMode} />
<ReleaseNotice />
<ImageLightbox />
{@render children()}
