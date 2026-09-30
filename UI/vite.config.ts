import tailwindcss from '@tailwindcss/vite';
import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vite';
import { paraglideVitePlugin } from '@inlang/paraglide-js';

export default defineConfig({
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
		proxy: {
			'/api': {
				target: 'http://localhost:8080',
				changeOrigin: true,
				ws: true
			},
			'/uploads': {
				target: 'http://localhost:8080',
				changeOrigin: true
			}
		}
	}
});
