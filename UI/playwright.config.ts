import os from 'node:os';
import { defineConfig } from '@playwright/test';

export default defineConfig({
	testDir: 'tests/e2e',
	forbidOnly: !!process.env.CI,
	// These tests gate every deploy to alpha: one retry keeps a transient
	// timing flake on the shared runner from blocking a roll, and Playwright
	// still reports the test as flaky.
	retries: process.env.CI ? 1 : 0,
	// Every suite mocks the API per page, so workers share no state; the cap
	// keeps a smaller runner from being oversubscribed.
	workers: process.env.CI ? Math.min(4, os.availableParallelism()) : undefined,
	reporter: process.env.CI ? [['dot'], ['html', { open: 'never' }]] : 'list',
	webServer: {
		command: 'npm run build && npm run preview -- --port 4174',
		port: 4174,
		reuseExistingServer: false,
		timeout: 480_000
	},
	use: {
		baseURL: 'http://localhost:4174',
		trace: 'retain-on-failure',
		screenshot: 'only-on-failure',
		video: 'retain-on-failure'
	}
});
