import tailwindcss from '@tailwindcss/vite';
import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig, loadEnv, type ConfigEnv } from 'vite';
import { paraglideVitePlugin } from '@inlang/paraglide-js';

export function createViteConfig({ mode }: ConfigEnv, envDir = '..') {
	const env = loadEnv(mode, envDir, 'STRIFIRA_DEV_');
	const apiUrl = env.STRIFIRA_DEV_API_URL || 'http://localhost:8080';

	return {
		envDir,
		plugins: [
			tailwindcss(),
			paraglideVitePlugin({
				project: './project.inlang',
				outdir: './src/lib/paraglide',
				strategy: ['localStorage', 'cookie', 'preferredLanguage', 'baseLocale'],
				// One module per locale instead of ~1,700 per-message modules:
				// fewer modules to bundle, and every page ships nearly all
				// messages anyway.
				outputStructure: 'locale-modules'
			}),
			sveltekit()
		],
		server: {
			port: Number(env.STRIFIRA_DEV_UI_PORT || '5173'),
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
}

export default defineConfig((configEnv) => createViteConfig(configEnv));
