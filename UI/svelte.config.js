import adapter from '@sveltejs/adapter-static';
import { vitePreprocess } from '@sveltejs/vite-plugin-svelte';

/** @type {import('@sveltejs/kit').Config} */
const config = {
	preprocess: vitePreprocess(),
	kit: {
		adapter: adapter({
			pages: 'build',
			assets: 'build',
			fallback: 'index.html',
			precompress: false,
			strict: true
		}),
		// Every push to main deploys: poll for a new build so the next
		// navigation after a deploy loads it instead of the stale bundle
		// (whose lazily loaded chunks no longer exist on the server).
		version: {
			pollInterval: 60_000
		}
	}
};

export default config;
