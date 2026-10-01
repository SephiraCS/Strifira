import { defineConfig } from '@playwright/test';

// This suite creates accounts and workspaces. Only its disposable local
// launcher may opt in; never silently point it at a deployed Ku instance.
const baseURL = process.env.KU_LIVE_BASE_URL;
if (process.env.KU_LIVE_ISOLATED !== '1' || !baseURL || new URL(baseURL).hostname !== '127.0.0.1') {
	throw new Error('Run node ../scripts/check-screen-share-live.mjs for an isolated live test.');
}

export default defineConfig({
	testDir: 'tests/integration',
	testMatch: 'screen-shares-live.spec.ts',
	forbidOnly: true,
	workers: 1,
	retries: 0,
	timeout: 180_000,
	expect: { timeout: 30_000 },
	reporter: [['list'], ['json', { outputFile: 'test-results/integration/results.json' }]],
	outputDir: process.env.KU_LIVE_OUTPUT_DIR ?? 'test-results/integration',
	use: {
		baseURL,
		browserName: 'chromium',
		locale: 'en-US',
		trace: 'retain-on-failure',
		screenshot: 'only-on-failure',
		launchOptions: {
			args: [
				'--disable-background-timer-throttling',
				'--disable-backgrounding-occluded-windows',
				'--disable-renderer-backgrounding'
			]
		}
	}
});
