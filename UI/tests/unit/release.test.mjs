import assert from 'node:assert/strict';
import test from 'node:test';
import {
	currentReleaseUrl,
	currentVersion,
	isTrustedReleaseNoteUrl,
	parseReleaseManifest,
	releasesManifestUrl,
	releasesPageUrl
} from '../../src/lib/release.ts';

const tag = 'v99.0.0';
const repoUrl = 'https://github.com/SephiraCS/Strifira';

function entry(overrides = {}) {
	return {
		tag_name: tag,
		html_url: `${repoUrl}/releases/tag/${tag}`,
		body: null,
		published_at: '2026-09-21T00:00:00Z',
		prerelease: false,
		...overrides
	};
}

test('release tracking points at the repository and same-origin manifest', () => {
	assert.equal(releasesManifestUrl, '/releases.json');
	assert.equal(releasesPageUrl, `${repoUrl}/releases`);
	assert.equal(currentReleaseUrl, `${repoUrl}/releases/tag/v${currentVersion}`);
});

test('keeps repository release URLs and falls back for foreign or mismatched ones', () => {
	const [release] = parseReleaseManifest([
		entry(),
		entry({ html_url: 'https://attacker.example/releases/latest', upgrade_url: 'https://attacker.example/install' }),
		entry({ html_url: `${repoUrl}/releases/tag/v98.0.0` }),
		entry({ html_url: 'https://github.com/attacker/strifira/releases/tag/v99.0.0' })
	]);

	assert.equal(release.html_url, `${repoUrl}/releases/tag/${tag}`);
	const fallback = parseReleaseManifest([entry({ html_url: null })])[0];
	assert.equal(fallback.html_url, `${repoUrl}/releases/tag/${tag}`);

	const hostile = parseReleaseManifest([entry({ html_url: 'https://attacker.example/releases/latest' })])[0];
	assert.equal(hostile.html_url, `${repoUrl}/releases/tag/${tag}`);
	assert.equal(hostile.upgrade_url, null);

	const mismatchedTag = parseReleaseManifest([entry({ html_url: `${repoUrl}/releases/tag/v98.0.0` })])[0];
	assert.equal(mismatchedTag.html_url, `${repoUrl}/releases/tag/${tag}`);

	const otherRepository = parseReleaseManifest([entry({ html_url: 'https://github.com/attacker/strifira/releases/tag/v99.0.0' })])[0];
	assert.equal(otherRepository.html_url, `${repoUrl}/releases/tag/${tag}`);
});

test('manifest entries from the decommissioned TerraScale host fall back', () => {
	const legacy = parseReleaseManifest([
		entry({
			html_url: 'https://git.terrascale.tech/TerraScale/strifira/releases/tag/v99.0.0',
			upgrade_url: 'https://git.terrascale.tech/Tools/strifira/releases/tag/v99.0.0'
		})
	])[0];
	assert.equal(legacy.html_url, `${repoUrl}/releases/tag/${tag}`);
	assert.equal(legacy.upgrade_url, null);
});

test('trusted release-note URLs stay inside the repository', () => {
	assert.equal(isTrustedReleaseNoteUrl(`${repoUrl}/pull/46`), true);
	assert.equal(isTrustedReleaseNoteUrl(`${repoUrl}/compare/v0.1.16...v0.2.0`), true);
	assert.equal(isTrustedReleaseNoteUrl('https://git.terrascale.tech/Tools/strifira/pulls/46'), false);
	assert.equal(isTrustedReleaseNoteUrl('https://git.terrascale.tech/TerraScale/strifira/pulls/46'), false);
	assert.equal(isTrustedReleaseNoteUrl('https://github.com/attacker/strifira/pulls/46'), false);
	assert.equal(isTrustedReleaseNoteUrl('https://github.com.evil.example/SephiraCS/Strifira/pulls/46'), false);
	assert.equal(isTrustedReleaseNoteUrl(`http://github.com/SephiraCS/Strifira/pull/46`), false);
});
