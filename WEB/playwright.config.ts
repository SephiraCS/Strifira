import os from 'node:os';
import { defineConfig } from '@playwright/test';

export default defineConfig({
	testDir: 'tests/e2e',
	timeout: 120_000,
	// Keep the shared CI runner from being oversubscribed.
	workers: process.env.CI ? Math.min(4, os.availableParallelism()) : undefined,
	webServer: {
		command: 'npm run preview -- --host 127.0.0.1 --port 4175',
		port: 4175,
		reuseExistingServer: false,
		timeout: 120_000
	},
	use: {
		baseURL: 'http://127.0.0.1:4175',
		viewport: { width: 320, height: 800 }
	}
});
