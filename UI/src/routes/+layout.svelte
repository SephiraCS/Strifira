<script lang="ts">
	import '../app.css';
	import { Toaster } from 'svelte-sonner';
	import { onMount } from 'svelte';
	import { beforeNavigate } from '$app/navigation';
	import { updated } from '$app/state';
	import LazyReleaseNotice from '$lib/components/shared/LazyReleaseNotice.svelte';
	import LazyImageLightbox from '$lib/components/shared/image-viewer/LazyImageLightbox.svelte';
	import { preferencesState } from '$lib/features/preferences/preferences.state.svelte';
	import { voiceState } from '$lib/features/chat/voice/voice.state.svelte';
	import { m } from '$lib/paraglide/messages.js';
	import { getLocale, setLocale } from '$lib/paraglide/runtime.js';

	let { children } = $props();

	beforeNavigate(({ to, cancel, willUnload }) => {
		// A deploy must not turn navigation within a call's workspace into a
		// page reload: that destroys its microphone, peers and floating panel.
		// Once the call ends, the next navigation can load the new build.
		const keepsCall = voiceState.session && to?.params?.workspaceSlug === voiceState.session.slug;
		if (updated.current && to && !willUnload && !keepsCall) {
			cancel();
			window.location.assign(to.url);
		}
	});

	onMount(() => {
		preferencesState.init();
	});
</script>

<Toaster position="bottom-right" theme={preferencesState.resolvedMode} />
<LazyReleaseNotice />
<LazyImageLightbox />
{@render children()}
