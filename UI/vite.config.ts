import tailwindcss from '@tailwindcss/vite';
import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig, loadEnv } from 'vite';
import { paraglideVitePlugin } from '@inlang/paraglide-js';

export default defineConfig(({ mode }) => {
	const env = loadEnv(mode, '..', 'KU_DEV_');
	const apiUrl = env.KU_DEV_API_URL || 'http://localhost:8080';

	return {
		envDir: '..',
		plugins: [
			tailwindcss(),
			paraglideVitePlugin({
				project: './project.inlang',
				outdir: './src/lib/paraglide',
				strategy: ['localStorage', 'cookie', 'baseLocale'],
				// One module per locale instead of ~1,700 per-message modules:
				// fewer modules to bundle, and every page ships nearly all
				// messages anyway.
				outputStructure: 'locale-modules'
			}),
			sveltekit()
		],
		server: {
			port: Number(env.KU_DEV_UI_PORT || '5173'),
			proxy: {
				'/api': {
					target: apiUrl,
					changeOrigin: true,
					ws: true
				},
				'/uploads': {
					target: apiUrl,
					changeOrigin: true
				}
			}
		}
	};
});
