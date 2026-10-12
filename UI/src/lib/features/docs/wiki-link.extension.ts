import { Node, Extension, mergeAttributes } from '@tiptap/core';
import { Plugin, PluginKey } from 'prosemirror-state';

export interface WikiLinkState {
	active: boolean;
	query: string;
	range: { from: number; to: number } | null;
	x: number;
	y: number;
}

export const wikiLinkPluginKey = new PluginKey('wikiLinkAutocomplete');

/**
 * Inline [[wiki link]] node. Links pages by title; the server resolves
 * titles to document ids when the editor saves, powering backlinks and the
 * graph view.
 */
export const WikiLink = Node.create({
	name: 'wikiLink',
	group: 'inline',
	inline: true,
	atom: true,

	addAttributes() {
		return {
			title: {
				default: '',
				parseHTML: (element) => element.getAttribute('data-wiki-title') ?? element.textContent ?? ''
			},
			docId: {
				default: null,
				parseHTML: (element) => element.getAttribute('data-doc-id')
			}
		};
	},

	parseHTML() {
		return [{ tag: 'a[data-type="wiki"]' }];
	},

	renderHTML({ node, HTMLAttributes }) {
		return [
			'a',
			mergeAttributes(HTMLAttributes, {
				'data-type': 'wiki',
				'data-wiki-title': node.attrs.title,
				...(node.attrs.docId ? { 'data-doc-id': node.attrs.docId } : {}),
				class: 'wiki-link',
				href: '#'
			}),
			node.attrs.title
		];
	}
});

/**
 * Detects "[[query" at the caret and drives an autocomplete popup for page
 * titles, mirroring the slash-command plugin's callback style.
 */
export function createWikiLinkAutocomplete(callbacks: {
	onStateChange: (state: WikiLinkState) => void;
	onNavigate: (direction: 'up' | 'down') => void;
	onSelect: () => void;
}) {
	return Extension.create({
		name: 'wikiLinkAutocomplete',

		addProseMirrorPlugins() {
			let active = false;
			let bracketFrom = 0;

			function deactivate() {
				if (!active) return;
				active = false;
				callbacks.onStateChange({ active: false, query: '', range: null, x: 0, y: 0 });
			}

			function emitState(view: any) {
				const to = view.state.selection.from;
				const query = view.state.doc.textBetween(bracketFrom + 2, to, '\0');
				try {
					const coords = view.coordsAtPos(bracketFrom);
					callbacks.onStateChange({
						active: true,
						query,
						range: { from: bracketFrom, to },
						x: coords.left,
						y: coords.bottom + 4
					});
				} catch {
					deactivate();
				}
			}

			// Detect "[[" right before the caret (typed as two separate chars).
			function checkActivation(view: any) {
				const pos = view.state.selection.from;
				if (pos < 2) return;
				const before = view.state.doc.textBetween(pos - 2, pos, '\0', '\0');
				if (before === '[[') {
					active = true;
					bracketFrom = pos - 2;
					emitState(view);
				}
			}

			return [
				new Plugin({
					key: wikiLinkPluginKey,
					props: {
						handleTextInput(view, from, _to, text) {
							if (active) {
								if (text === ']') {
									// "]]" typed: commit like Enter, but only when a
									// query exists; otherwise let the bracket through.
									const to = view.state.selection.from;
									const query = view.state.doc.textBetween(bracketFrom + 2, to, '\0');
									if (query) {
										callbacks.onSelect();
										return true;
									}
								}
								setTimeout(() => {
									if (active) emitState(view);
								}, 0);
								return false;
							}
							if (text === '[') {
								setTimeout(() => checkActivation(view), 0);
							}
							return false;
						},
						handleKeyDown(view: any, event: KeyboardEvent) {
							if (!active) return false;
							if (event.metaKey || event.ctrlKey) return false;
							if (event.key === 'ArrowDown') {
								callbacks.onNavigate('down');
								return true;
							}
							if (event.key === 'ArrowUp') {
								callbacks.onNavigate('up');
								return true;
							}
							if (event.key === 'Enter') {
								event.preventDefault();
								callbacks.onSelect();
								return true;
							}
							if (event.key === 'Escape') {
								deactivate();
								return true;
							}
							if (event.key === 'Backspace') {
								const cursorPos = view.state.selection.from;
								if (cursorPos <= bracketFrom + 2) {
									deactivate();
									return false;
								}
								setTimeout(() => {
									if (active) emitState(view);
								}, 0);
								return false;
							}
							return false;
						},
						handleClick() {
							if (active) deactivate();
							return false;
						}
					},
					view() {
						return {
							update(view: any) {
								if (!active) return;
								const cursorPos = view.state.selection.from;
								if (cursorPos < bracketFrom || cursorPos > bracketFrom + 80) {
									deactivate();
									return;
								}
								try {
									const chars = view.state.doc.textBetween(bracketFrom, bracketFrom + 2);
									if (chars !== '[[') deactivate();
								} catch {
									deactivate();
								}
							},
							destroy() {
								deactivate();
							}
						};
					}
				})
			];
		}
	});
}

/** Collect every wiki-link title in the current document, deduplicated. */
export function collectWikiTitles(editor: { state: { doc: any } }): string[] {
	const titles: string[] = [];
	const seen = new Set<string>();
	editor.state.doc.descendants((node: any) => {
		if (node.type.name === 'wikiLink' && node.attrs.title && !seen.has(node.attrs.title)) {
			seen.add(node.attrs.title);
			titles.push(node.attrs.title);
		}
	});
	return titles;
}

/** Collect wiki-link titles from stored editor HTML (template copies). */
export function wikiTitlesFromHtml(html: string): string[] {
	const titles: string[] = [];
	const seen = new Set<string>();
	const container = document.createElement('div');
	container.innerHTML = html;
	for (const anchor of Array.from(container.querySelectorAll('a[data-type="wiki"]'))) {
		const title = anchor.getAttribute('data-wiki-title') ?? anchor.textContent?.trim() ?? '';
		if (title && !seen.has(title)) {
			seen.add(title);
			titles.push(title);
		}
	}
	return titles;
}
