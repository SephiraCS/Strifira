import { defineConfig } from '@playwright/test';

// Runner for the production-facing specs under tests/prod/ (deployed
// strifira.mariogk.com). No webServer: these exercise the live build.
//   npx playwright test --config playwright.prod.config.ts [spec name]
export default defineConfig({
	testDir: 'tests/prod',
	retries: 0,
	workers: 1,
	reporter: 'list',
	timeout: 330_000,
	use: { baseURL: 'https://strifira.mariogk.com' }
});
