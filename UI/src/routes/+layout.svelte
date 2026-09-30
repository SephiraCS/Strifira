<script lang="ts">
	import '../app.css';
	import { Toaster } from 'svelte-sonner';
	import { onMount } from 'svelte';
	import { beforeNavigate } from '$app/navigation';
	import { updated } from '$app/state';
	import ReleaseNotice from '$lib/components/shared/ReleaseNotice.svelte';
	import { ImageLightbox } from '$lib/components/shared/image-viewer';
	import { preferencesState } from '$lib/features/preferences/preferences.state.svelte';
	import { m } from '$lib/paraglide/messages.js';
	import { getLocale, setLocale } from '$lib/paraglide/runtime.js';

	let { children } = $props();

	beforeNavigate(({ to, cancel, willUnload }) => {
		if (updated.current && to && !willUnload) {
			cancel();
			window.location.assign(to.url);
		}
	});

	onMount(() => {
		preferencesState.init();
	});
</script>

<Toaster position="bottom-right" theme={preferencesState.resolvedMode} />
<ReleaseNotice />
<ImageLightbox />
{@render children()}
