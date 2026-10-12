import TurndownService from 'turndown';

/**
 * Markdown helpers for docs import/export. Wiki links ([[Page]]) survive the
 * editor-HTML → markdown direction; markdown → editor-HTML conversion for
 * imports happens server-side when a doc is created from content_md.
 */

/** Derive a page title from an imported markdown file. */
export function markdownTitle(filename: string, source: string): string {
	const heading = source
		.split('\n')
		.map((line) => line.match(/^#\s+(.+)$/))
		.find(Boolean);
	if (heading?.[1]) return heading[1].trim().slice(0, 200);
	const base = filename.replace(/\.(md|markdown)$/i, '').replace(/[-_]+/g, ' ').trim();
	return (base || 'Untitled').slice(0, 200);
}

let turndown: TurndownService | null = null;

function getTurndown(): TurndownService {
	if (turndown) return turndown;
	const service = new TurndownService({
		headingStyle: 'atx',
		hr: '---',
		bulletListMarker: '-',
		codeBlockStyle: 'fenced'
	});
	// Task list items render with a checkbox input inside the li.
	service.addRule('taskItem', {
		filter: (node) => {
			if (node.nodeName !== 'LI') return false;
			return Array.from(node.children).some((child) => child.nodeName === 'INPUT');
		},
		replacement: (content, node) => {
			const input = Array.from(node.children).find((child) => child.nodeName === 'INPUT') as HTMLInputElement | undefined;
			const checked = input?.getAttribute('data-checked') === 'true' || input?.hasAttribute('checked');
			const body = content.replace(/^\s+/, '').replace(/\n\s+/g, '\n  ').trim();
			return `\n- [${checked ? 'x' : ' '}] ${body}\n`;
		}
	});
	service.addRule('wikiLink', {
		filter: (node) => node.nodeName === 'A' && node.getAttribute('data-type') === 'wiki',
		replacement: (_content, node) => `[[${node.getAttribute('data-wiki-title') ?? node.textContent}]]`
	});
	service.addRule('attachment', {
		filter: (node) => node.nodeName === 'A' && node.hasAttribute('data-filename'),
		replacement: (_content, node) => `[${node.getAttribute('data-filename') ?? 'file'}](${node.getAttribute('href') ?? ''})`
	});
	// Strikethrough uses ~~del~~ in markdown.
	service.addRule('strikethrough', {
		filter: ['del', 's'],
		replacement: (content) => `~~${content}~~`
	});
	service.addRule('underline', {
		filter: ['u'],
		replacement: (content) => content
	});
	turndown = service;
	return service;
}

/** Convert sanitized editor HTML back to markdown (wiki links included). */
export function editorHtmlToMarkdown(html: string): string {
	const container = document.createElement('div');
	container.innerHTML = html;
	// Attachment/file links keep their query string; images keep src/alt.
	return getTurndown().turndown(container).trim();
}

/** Build a filename for markdown export. */
export function markdownFilename(title: string): string {
	const slug = (title || 'untitled')
		.toLowerCase()
		.replace(/[^\p{L}\p{N}]+/gu, '-')
		.replace(/^-+|-+$/g, '')
		.slice(0, 80);
	return `${slug || 'untitled'}.md`;
}

/** Trigger a client-side file download. */
export function downloadTextFile(filename: string, content: string, contentType = 'text/markdown') {
	const blob = new Blob([content], { type: contentType });
	const url = URL.createObjectURL(blob);
	const anchor = document.createElement('a');
	anchor.href = url;
	anchor.download = filename;
	document.body.append(anchor);
	anchor.click();
	anchor.remove();
	URL.revokeObjectURL(url);
}
