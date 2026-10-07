import assert from 'node:assert/strict';
import test from 'node:test';
import { createKeyboardHandler, createShortcutEngine } from '../../src/lib/utils/keyboard.ts';

for (const [name, createHandler] of [
	['single-key', (handler) => createKeyboardHandler([{ key: 'Escape', handler }])],
	[
		'sequence',
		(handler) => createShortcutEngine([{ key: 'Escape', handler, label: 'Back', category: 'Navigation' }]).handler
	]
]) {
	test(`${name} shortcuts respect consumed Escape and editable controls`, () => {
		let calls = 0;
		const handler = createHandler(() => calls++);
		const event = (target = { tagName: 'BUTTON' }, consumed = false) => ({
			key: 'Escape',
			target,
			defaultPrevented: consumed,
			preventDefault() {
				this.defaultPrevented = true;
			}
		});
		handler(event(undefined, true));
		for (const target of [{ tagName: 'INPUT' }, { tagName: 'TEXTAREA' }, { tagName: 'DIV', isContentEditable: true }]) {
			handler(event(target));
		}
		assert.equal(calls, 0);
		const escape = event();
		handler(escape);
		assert.equal(calls, 1);
		assert.equal(escape.defaultPrevented, true);
	});
}
