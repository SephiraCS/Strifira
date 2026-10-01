import test from 'node:test';
import assert from 'node:assert/strict';
import { actionDuration } from '../../src/lib/features/forgejo/actions.ts';

test('formats completed run durations across minute and date boundaries', () => {
	assert.equal(actionDuration('2026-09-30T23:59:50Z', '2026-10-01T00:00:20Z'), '30s');
	assert.equal(actionDuration('2026-09-30T12:00:00Z', '2026-09-30T12:01:00Z'), '1m 0s');
	assert.equal(actionDuration('2026-09-30T12:00:00Z', '2026-09-30T12:02:05Z'), '2m 5s');
	assert.equal(actionDuration('2026-09-30T12:00:00Z', '2026-09-30T12:00:59.500Z'), '1m 0s');
});

test('omits durations for incomplete, invalid, or reversed timestamps', () => {
	assert.equal(actionDuration(null, '2026-09-30T12:01:00Z'), '');
	assert.equal(actionDuration('2026-09-30T12:00:00Z', null), '');
	assert.equal(actionDuration('', '2026-09-30T12:01:00Z'), '');
	assert.equal(actionDuration('invalid', '2026-09-30T12:01:00Z'), '');
	assert.equal(actionDuration('2026-09-30T12:00:00Z', 'invalid'), '');
	assert.equal(actionDuration('2026-09-30T12:01:00Z', '2026-09-30T12:00:00Z'), '');
});
