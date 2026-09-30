import test from 'node:test';
import assert from 'node:assert/strict';
import {
	forgejoOrigin,
	safeForgejoRepositoryUrl,
	safeForgejoPullRequestUrl,
	safeForgejoBranchUrl,
	safeForgejoCommitUrl,
	forgejoRepositoryHomeUrl,
	safeForgejoActionsUrl
} from '../../src/lib/security/forgejo-url.ts';

const INSTANCE = 'https://git.terrascale.tech';
const REPO = 'TerraScale/ku';

test('forgejoOrigin extracts the origin', () => {
	assert.equal(forgejoOrigin(INSTANCE), 'https://git.terrascale.tech');
	assert.equal(forgejoOrigin(INSTANCE + '/'), 'https://git.terrascale.tech');
	assert.equal(forgejoOrigin('http://git.internal:3000'), 'http://git.internal:3000');
	assert.equal(forgejoOrigin(''), null);
	assert.equal(forgejoOrigin('not a url'), null);
});

test('safeForgejoRepositoryUrl accepts only the linked instance', () => {
	assert.equal(safeForgejoRepositoryUrl(`${INSTANCE}/${REPO}`, REPO, INSTANCE), `${INSTANCE}/${REPO}`);
	assert.equal(safeForgejoRepositoryUrl(`${INSTANCE}/${REPO}/src`, REPO, INSTANCE), `${INSTANCE}/${REPO}/src`);
	assert.equal(safeForgejoRepositoryUrl(`https://github.com/${REPO}`, REPO, INSTANCE), null);
	assert.equal(safeForgejoRepositoryUrl(`https://git.terrascale.tech.example.com/${REPO}`, REPO, INSTANCE), null);
	assert.equal(safeForgejoRepositoryUrl(`javascript:alert(1)`, REPO, INSTANCE), null);
});

test('safeForgejoPullRequestUrl validates pull paths', () => {
	assert.equal(safeForgejoPullRequestUrl(`${INSTANCE}/${REPO}/pull/12`, REPO, INSTANCE), `${INSTANCE}/${REPO}/pull/12`);
	assert.equal(
		safeForgejoPullRequestUrl(`${INSTANCE}/${REPO}/pulls/12`, REPO, INSTANCE),
		`${INSTANCE}/${REPO}/pulls/12`
	);
	assert.equal(safeForgejoPullRequestUrl(`${INSTANCE}/${REPO}/pull/12/files`, REPO, INSTANCE), null);
	assert.equal(safeForgejoPullRequestUrl(`${INSTANCE}/${REPO}/pull/zero`, REPO, INSTANCE), null);
	assert.equal(safeForgejoPullRequestUrl(`${INSTANCE}/${REPO}/issues/12`, REPO, INSTANCE), null);
	assert.equal(safeForgejoPullRequestUrl(`${INSTANCE}/${REPO}/pull/12?x=1`, REPO, INSTANCE), null);
	assert.equal(safeForgejoPullRequestUrl(`${INSTANCE}/attacker/ku/pull/12`, REPO, INSTANCE), null);
});

test('safeForgejoBranchUrl validates tree paths', () => {
	assert.equal(
		safeForgejoBranchUrl(`${INSTANCE}/${REPO}/tree/KUA-1-fix`, REPO, INSTANCE),
		`${INSTANCE}/${REPO}/tree/KUA-1-fix`
	);
	assert.equal(
		safeForgejoBranchUrl(`${INSTANCE}/${REPO}/src/branch/main`, REPO, INSTANCE),
		`${INSTANCE}/${REPO}/src/branch/main`
	);
	assert.equal(safeForgejoBranchUrl(`${INSTANCE}/${REPO}/tree/`, REPO, INSTANCE), null);
	assert.equal(safeForgejoBranchUrl(`${INSTANCE}/${REPO}/pull/1`, REPO, INSTANCE), null);
});

test('safeForgejoCommitUrl validates commit shas', () => {
	assert.equal(
		safeForgejoCommitUrl(`${INSTANCE}/${REPO}/commit/9f2a1c0`, REPO, INSTANCE),
		`${INSTANCE}/${REPO}/commit/9f2a1c0`
	);
	assert.equal(safeForgejoCommitUrl(`${INSTANCE}/${REPO}/commit/xyz`, REPO, INSTANCE), null);
	assert.equal(safeForgejoCommitUrl(`${INSTANCE}/${REPO}/commit/`, REPO, INSTANCE), null);
});

test('forgejoRepositoryHomeUrl builds a safe link', () => {
	assert.equal(forgejoRepositoryHomeUrl(REPO, INSTANCE), `${INSTANCE}/${REPO}`);
	assert.equal(forgejoRepositoryHomeUrl(REPO, ''), null);
});

test('safeForgejoActionsUrl accepts only a run on the linked repository', () => {
	assert.equal(
		safeForgejoActionsUrl(`${INSTANCE}/${REPO}/actions/42`, REPO, INSTANCE),
		`${INSTANCE}/${REPO}/actions/42`
	);
	for (const url of [
		`https://example.com/${REPO}/actions/42`,
		`${INSTANCE}/someone/else/actions/42`,
		`${INSTANCE}/${REPO}/actions/42?token=secret`,
		`${INSTANCE}/${REPO}/actions/0`,
		`${INSTANCE}/${REPO}/actions/42/jobs/2`,
		`${INSTANCE}/${REPO}/actions/%ZZ`
	]) {
		assert.equal(safeForgejoActionsUrl(url, REPO, INSTANCE), null);
	}
});

test('malformed encoded Forgejo paths are rejected without throwing', () => {
	assert.equal(safeForgejoRepositoryUrl(`${INSTANCE}/${REPO}/src/%ZZ`, REPO, INSTANCE), null);
});
