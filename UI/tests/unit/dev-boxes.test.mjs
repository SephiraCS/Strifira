import assert from 'node:assert/strict';
import { registerHooks } from 'node:module';
import test from 'node:test';

const alias = registerHooks({
	resolve(specifier, context, nextResolve) {
		if (specifier === '$lib/paraglide/messages.js') {
			return nextResolve(new URL('../../src/lib/paraglide/messages.js', import.meta.url).href, context);
		}
		return nextResolve(specifier, context);
	}
});
const { parseTestCommand } = await import('../../src/lib/features/dev-boxes/dev-boxes.ts');
alias.deregister();

test('test command arguments preserve their independent values at the exact UTF-8 byte limit', () => {
	for (const argument of ['a'.repeat(4096), 'é'.repeat(2048), '🚀'.repeat(1024)]) {
		assert.deepEqual(parseTestCommand(JSON.stringify(['runner', argument, ' value with spaces '])), [
			'runner',
			argument,
			' value with spaces '
		]);
		assert.throws(() => parseTestCommand(JSON.stringify(['runner', argument + 'a'])));
	}
	assert.throws(() => parseTestCommand(JSON.stringify(['runner', 'é'.repeat(2049)])));
});

test('test commands reject invalid JSON, empty/non-string/NUL arguments and excess argument counts', () => {
	for (const command of [null, {}, [], [''], [' \t'], [1], ['run\0this'], Array.from({ length: 33 }, () => 'arg')]) {
		assert.throws(() => parseTestCommand(JSON.stringify(command)));
	}
	assert.throws(() => parseTestCommand('go test ./...'));
	const command = Array.from({ length: 32 }, (_, index) => `arg ${index}`);
	assert.deepEqual(parseTestCommand(JSON.stringify(command)), command);
});
