import os from 'node:os';
import { defineConfig } from '@playwright/test';

// Two suites, chosen with E2E_SUITE (unset runs both):
//   fast - the critical paths a deploy must never break: signing in,
//          issues, boards, and the Forgejo automation. It gates every
//          deploy to alpha (ci.yml, `make test-fast`).
//   slow - every other spec. It runs in slow.yml after the push and never
//          blocks a deploy (`make test-slow`).
// A new spec is slow unless it is added here; promote it only when a
// regression in it must stop a deploy, and keep this list short.
const FAST_SPECS = [
	'login.test.ts',
	'issues.test.ts',
	'issue-description-recovery.test.ts',
	'create-issue-image-breaks.test.ts',
	'create-issue-failure-keeps-draft.test.ts',
	'upload-auth-refresh.test.ts',
	'team-board.test.ts',
	'my-issues-board.test.ts',
	'forgejo-automation.test.ts',
	'workspace-load-recovery.test.ts'
];
const suite = process.env.E2E_SUITE ?? '';
if (!['', 'fast', 'slow'].includes(suite)) {
	throw new Error(`E2E_SUITE must be fast, slow, or unset; got ${suite}`);
}

export default defineConfig({
	testDir: 'tests/e2e',
	...(suite === 'fast' ? { testMatch: FAST_SPECS } : {}),
	...(suite === 'slow' ? { testIgnore: FAST_SPECS } : {}),
	forbidOnly: !!process.env.CI,
	// These tests gate every deploy to alpha: one retry keeps a transient
	// timing flake on the shared runner from blocking a roll, and Playwright
	// still reports the test as flaky.
	retries: process.env.CI ? 1 : 0,
	// Every suite mocks the API per page, so workers share no state; the cap
	// keeps a smaller runner from being oversubscribed.
	workers: process.env.CI ? Math.min(2, os.availableParallelism()) : undefined,
	reporter: process.env.CI ? [['dot'], ['html', { open: 'never' }]] : 'list',
	// CI builds the app once and hands the build to every shard
	// (E2E_PREBUILT=1), which then only serves it. A plain run always
	// rebuilds, so a stale build is never tested by accident.
	webServer: {
		command:
			process.env.E2E_PREBUILT === '1'
				? 'node scripts/serve-e2e-build.mjs 4174'
				: 'npm run build && node scripts/serve-e2e-build.mjs 4174',
		port: 4174,
		reuseExistingServer: false,
		timeout: 480_000
	},
	use: {
		baseURL: 'http://localhost:4174',
		// Preserve full diagnostics on CI retries without tracing every passing test.
		trace: process.env.CI ? 'on-first-retry' : 'retain-on-failure',
		screenshot: 'only-on-failure',
		video: process.env.CI ? 'on-first-retry' : 'retain-on-failure'
	}
});
