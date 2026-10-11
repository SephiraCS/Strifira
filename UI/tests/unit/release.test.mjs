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

function entry(overrides = {}) {
	return {
		tag_name: tag,
		html_url: `https://git.terrascale.tech/Tools/ku/releases/tag/${tag}`,
		body: null,
		published_at: '2026-09-21T00:00:00Z',
		prerelease: false,
		...overrides
	};
}

test('release tracking points at the fork repository and same-origin manifest', () => {
	assert.equal(releasesManifestUrl, '/releases.json');
	assert.equal(releasesPageUrl, 'https://git.terrascale.tech/Tools/ku/releases');
	assert.equal(currentReleaseUrl, `https://git.terrascale.tech/Tools/ku/releases/tag/v${currentVersion}`);
});

test('keeps fork release URLs and falls back for foreign or mismatched ones', () => {
	const [release] = parseReleaseManifest([
		entry(),
		entry({ html_url: 'https://attacker.example/releases/latest', upgrade_url: 'https://attacker.example/install' }),
		entry({ html_url: `https://git.terrascale.tech/Tools/ku/releases/tag/v98.0.0` }),
		entry({ html_url: 'https://git.terrascale.tech/attacker/ku/releases/tag/v99.0.0' })
	]);

	assert.equal(release.html_url, `https://git.terrascale.tech/Tools/ku/releases/tag/${tag}`);
	const fallback = parseReleaseManifest([entry({ html_url: null })])[0];
	assert.equal(fallback.html_url, `https://git.terrascale.tech/Tools/ku/releases/tag/${tag}`);

	const hostile = parseReleaseManifest([entry({ html_url: 'https://attacker.example/releases/latest' })])[0];
	assert.equal(hostile.html_url, `https://git.terrascale.tech/Tools/ku/releases/tag/${tag}`);
	assert.equal(hostile.upgrade_url, null);

	const mismatchedTag = parseReleaseManifest([entry({ html_url: 'https://git.terrascale.tech/Tools/ku/releases/tag/v98.0.0' })])[0];
	assert.equal(mismatchedTag.html_url, `https://git.terrascale.tech/Tools/ku/releases/tag/${tag}`);

	const otherRepository = parseReleaseManifest([entry({ html_url: 'https://git.terrascale.tech/attacker/ku/releases/tag/v99.0.0' })])[0];
	assert.equal(otherRepository.html_url, `https://git.terrascale.tech/Tools/ku/releases/tag/${tag}`);
});

test('manifest entries published before the Tools move keep their legacy owner URLs', () => {
	const legacy = parseReleaseManifest([
		entry({
			html_url: `https://git.terrascale.tech/TerraScale/ku/releases/tag/${tag}`,
			upgrade_url: 'https://git.terrascale.tech/TerraScale/ku/releases/tag/v99.0.0'
		})
	])[0];
	assert.equal(legacy.html_url, `https://git.terrascale.tech/TerraScale/ku/releases/tag/${tag}`);
	assert.equal(legacy.upgrade_url, 'https://git.terrascale.tech/TerraScale/ku/releases/tag/v99.0.0');
});

test('trusted release-note URLs stay inside the fork repository', () => {
	assert.equal(isTrustedReleaseNoteUrl('https://git.terrascale.tech/Tools/ku/pulls/46'), true);
	assert.equal(isTrustedReleaseNoteUrl('https://git.terrascale.tech/Tools/ku/compare/v0.1.16...v0.2.0'), true);
	assert.equal(isTrustedReleaseNoteUrl('https://git.terrascale.tech/TerraScale/ku/pulls/46'), true);
	assert.equal(isTrustedReleaseNoteUrl('https://github.com/carbogninalberto/kuayle/pull/46'), false);
	assert.equal(isTrustedReleaseNoteUrl('https://git.terrascale.tech/attacker/ku/pulls/46'), false);
	assert.equal(isTrustedReleaseNoteUrl('https://git.terrascale.tech.evil.example/Tools/ku/pulls/46'), false);
	assert.equal(isTrustedReleaseNoteUrl('http://git.terrascale.tech/Tools/ku/pulls/46'), false);
});
