import test from 'node:test';
import assert from 'node:assert/strict';
import {
	githubOrigin,
	githubApiBaseUrl,
	safeGitHubRepositoryUrl,
	safeGitHubPullRequestUrl,
	safeGitHubBranchUrl,
	safeGitHubCommitUrl,
	githubRepositoryHomeUrl,
	safeGitHubActionsUrl
} from '../../src/lib/security/github-url.ts';

const INSTANCE = 'https://github.example.com';
const REPO = 'TerraScale/ku';

test('githubOrigin extracts the origin', () => {
	assert.equal(githubOrigin(INSTANCE), 'https://github.example.com');
	assert.equal(githubOrigin(INSTANCE + '/'), 'https://github.example.com');
	assert.equal(githubOrigin('http://github.internal:3000'), 'http://github.internal:3000');
	assert.equal(githubOrigin(''), null);
	assert.equal(githubOrigin('not a url'), null);
});

test('githubApiBaseUrl mirrors the backend client derivation', () => {
	assert.equal(githubApiBaseUrl('https://github.com'), 'https://api.github.com');
	assert.equal(githubApiBaseUrl('https://github.com/'), 'https://api.github.com');
	assert.equal(githubApiBaseUrl('https://GITHUB.COM'), 'https://api.github.com');
	assert.equal(githubApiBaseUrl(INSTANCE), 'https://github.example.com/api/v3');
	assert.equal(githubApiBaseUrl(INSTANCE + '/org/repo'), 'https://github.example.com/api/v3');
	assert.equal(githubApiBaseUrl('http://github.internal:3000'), 'http://github.internal:3000/api/v3');
	assert.equal(githubApiBaseUrl(''), null);
	assert.equal(githubApiBaseUrl('not a url'), null);
});

test('safeGitHubRepositoryUrl accepts only the linked instance', () => {
	assert.equal(safeGitHubRepositoryUrl(`${INSTANCE}/${REPO}`, REPO, INSTANCE), `${INSTANCE}/${REPO}`);
	assert.equal(safeGitHubRepositoryUrl(`${INSTANCE}/${REPO}/src`, REPO, INSTANCE), `${INSTANCE}/${REPO}/src`);
	assert.equal(safeGitHubRepositoryUrl(`https://github.com/${REPO}`, REPO, INSTANCE), null);
	assert.equal(safeGitHubRepositoryUrl(`https://github.example.com.evil.net/${REPO}`, REPO, INSTANCE), null);
	assert.equal(safeGitHubRepositoryUrl(`javascript:alert(1)`, REPO, INSTANCE), null);
});

test('safeGitHubPullRequestUrl validates pull paths', () => {
	assert.equal(safeGitHubPullRequestUrl(`${INSTANCE}/${REPO}/pull/12`, REPO, INSTANCE), `${INSTANCE}/${REPO}/pull/12`);
	assert.equal(
		safeGitHubPullRequestUrl(`${INSTANCE}/${REPO}/pulls/12`, REPO, INSTANCE),
		`${INSTANCE}/${REPO}/pulls/12`
	);
	assert.equal(safeGitHubPullRequestUrl(`${INSTANCE}/${REPO}/pull/12/files`, REPO, INSTANCE), null);
	assert.equal(safeGitHubPullRequestUrl(`${INSTANCE}/${REPO}/pull/zero`, REPO, INSTANCE), null);
	assert.equal(safeGitHubPullRequestUrl(`${INSTANCE}/${REPO}/issues/12`, REPO, INSTANCE), null);
	assert.equal(safeGitHubPullRequestUrl(`${INSTANCE}/${REPO}/pull/12?x=1`, REPO, INSTANCE), null);
	assert.equal(safeGitHubPullRequestUrl(`${INSTANCE}/attacker/ku/pull/12`, REPO, INSTANCE), null);
});

test('safeGitHubBranchUrl validates tree paths', () => {
	assert.equal(
		safeGitHubBranchUrl(`${INSTANCE}/${REPO}/tree/KUA-1-fix`, REPO, INSTANCE),
		`${INSTANCE}/${REPO}/tree/KUA-1-fix`
	);
	assert.equal(
		safeGitHubBranchUrl(`${INSTANCE}/${REPO}/src/branch/main`, REPO, INSTANCE),
		`${INSTANCE}/${REPO}/src/branch/main`
	);
	assert.equal(safeGitHubBranchUrl(`${INSTANCE}/${REPO}/tree/`, REPO, INSTANCE), null);
	assert.equal(safeGitHubBranchUrl(`${INSTANCE}/${REPO}/pull/1`, REPO, INSTANCE), null);
});

test('safeGitHubCommitUrl validates commit shas', () => {
	assert.equal(
		safeGitHubCommitUrl(`${INSTANCE}/${REPO}/commit/9f2a1c0`, REPO, INSTANCE),
		`${INSTANCE}/${REPO}/commit/9f2a1c0`
	);
	assert.equal(safeGitHubCommitUrl(`${INSTANCE}/${REPO}/commit/xyz`, REPO, INSTANCE), null);
	assert.equal(safeGitHubCommitUrl(`${INSTANCE}/${REPO}/commit/`, REPO, INSTANCE), null);
});

test('githubRepositoryHomeUrl builds a safe link', () => {
	assert.equal(githubRepositoryHomeUrl(REPO, INSTANCE), `${INSTANCE}/${REPO}`);
	assert.equal(githubRepositoryHomeUrl(REPO, ''), null);
});

test('safeGitHubActionsUrl accepts the run URL shape', () => {
	const nativeUrl = `${INSTANCE}/${REPO}/actions/runs/302`;
	assert.equal(safeGitHubActionsUrl(nativeUrl, REPO, INSTANCE), nativeUrl);
});

test('safeGitHubActionsUrl rejects obsolete, malformed, and untrusted run URLs', () => {
	for (const url of [
		`https://example.com/${REPO}/actions/runs/42`,
		`https://github.example.com.evil.net/${REPO}/actions/runs/42`,
		`https://user:secret@github.example.com/${REPO}/actions/runs/42`,
		`${INSTANCE}/someone/else/actions/runs/42`,
		`${INSTANCE}/${REPO}/actions/runs/42?token=secret`,
		`${INSTANCE}/${REPO}/actions/runs/42#fragment`,
		`${INSTANCE}/${REPO}/actions/42`,
		`${INSTANCE}/${REPO}/actions/0`,
		`${INSTANCE}/${REPO}/actions/runs/0`,
		`${INSTANCE}/${REPO}/actions/runs/-1`,
		`${INSTANCE}/${REPO}/actions/runs/01`,
		`${INSTANCE}/${REPO}/actions/runs/42.5`,
		`${INSTANCE}/${REPO}/actions/runs/not-a-number`,
		`${INSTANCE}/${REPO}/actions/runs/`,
		`${INSTANCE}/${REPO}/actions/runs/42/jobs/2`,
		`${INSTANCE}/${REPO}/actions/runs/42/`,
		`${INSTANCE}/${REPO}/actions/runs/%ZZ`,
		`${INSTANCE}/${REPO}/actions/runs/42%2Fjobs`,
		`javascript:alert(1)`
	]) {
		assert.equal(safeGitHubActionsUrl(url, REPO, INSTANCE), null, url);
	}
});

test('malformed encoded GitHub paths are rejected without throwing', () => {
	assert.equal(safeGitHubRepositoryUrl(`${INSTANCE}/${REPO}/src/%ZZ`, REPO, INSTANCE), null);
});
