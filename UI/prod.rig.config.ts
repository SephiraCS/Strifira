import { defineConfig } from '@playwright/test';

// Throwaway runner config for the production wall rig
// (tests/prod/wall.spec.ts) — not committed; delete after the run.
export default defineConfig({
	testDir: './tests/prod',
	retries: 0,
	workers: 1,
	reporter: 'list',
	timeout: 330_000,
	use: { baseURL: 'https://ku.mariogk.com' }
});
