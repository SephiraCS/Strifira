import test from 'node:test';
import assert from 'node:assert/strict';
import { validateAcceptanceCriteria } from '../../src/lib/features/dev-boxes/acceptance-criteria.ts';

test('criteria are trimmed and blank lines do not consume the requirement limit', () => {
	assert.deepEqual(validateAcceptanceCriteria('  Tests pass \r\n\n\tChanges are reviewable\t\n'), {
		criteria: ['Tests pass', 'Changes are reviewable'],
		error: null
	});
	assert.deepEqual(validateAcceptanceCriteria(' \n\t\r\n'), { criteria: [], error: null });
});

test('exactly 50 nonempty criteria are allowed and 51 are rejected', () => {
	const fifty = Array.from({ length: 50 }, (_, index) => `Requirement ${index + 1}`);
	assert.deepEqual(validateAcceptanceCriteria(fifty.join('\n\n')), { criteria: fifty, error: null });
	assert.deepEqual(validateAcceptanceCriteria([...fifty, 'One too many'].join('\n')), {
		criteria: null,
		error: 'count'
	});
});

test('criteria limits use UTF-8 bytes after trimming, including multibyte text', () => {
	for (const accepted of ['a'.repeat(2000), 'é'.repeat(1000), '🚀'.repeat(500)]) {
		assert.deepEqual(validateAcceptanceCriteria(` \t${accepted} \r\n`), { criteria: [accepted], error: null });
		assert.deepEqual(validateAcceptanceCriteria(`Short\n${accepted}a`), {
			criteria: null,
			error: 'length',
			line: 2
		});
	}
});

test('NUL characters are rejected with the affected requirement number', () => {
	assert.deepEqual(validateAcceptanceCriteria('Valid\n\nMust not contain\0NUL'), {
		criteria: null,
		error: 'nul',
		line: 2
	});
});
