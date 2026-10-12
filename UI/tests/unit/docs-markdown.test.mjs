import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
	markdownTitle,
	editorHtmlToMarkdown,
	markdownFilename,
	downloadTextFile
} from '../../src/lib/features/docs/markdown.ts';

test('markdownTitle prefers the first level-1 heading', () => {
	assert.equal(markdownTitle('notes.md', '# My Notes\n\nBody'), 'My Notes');
});

test('markdownTitle falls back to a cleaned filename', () => {
	assert.equal(markdownTitle('my-cool-notes.md', 'just text'), 'my cool notes');
	assert.equal(markdownTitle('untitled.markdown', ''), 'untitled');
});

test('markdown module still exports the DOM-backed helpers', () => {
	// editorHtmlToMarkdown/downloadTextFile need a browser DOM, so only their
	// presence is asserted here; behavior is covered by e2e tests.
	assert.equal(typeof editorHtmlToMarkdown, 'function');
	assert.equal(typeof downloadTextFile, 'function');
});

test('markdownFilename slugifies titles', () => {
	assert.equal(markdownFilename('My Page!'), 'my-page.md');
	assert.equal(markdownFilename('产品路线图'), '产品路线图.md');
	assert.equal(markdownFilename(''), 'untitled.md');
});
