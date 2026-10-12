import type { Editor } from '@tiptap/core';
import Heading1 from '@lucide/svelte/icons/heading-1';
import Heading2 from '@lucide/svelte/icons/heading-2';
import Heading3 from '@lucide/svelte/icons/heading-3';
import List from '@lucide/svelte/icons/list';
import ListOrdered from '@lucide/svelte/icons/list-ordered';
import ListChecks from '@lucide/svelte/icons/list-checks';
import ImagePlus from '@lucide/svelte/icons/image-plus';
import Smile from '@lucide/svelte/icons/face-slightly-smiling';
import Paperclip from '@lucide/svelte/icons/paperclip';
import Code2 from '@lucide/svelte/icons/code-xml';
import GitBranch from '@lucide/svelte/icons/git-branch';
import ChevronsDownUp from '@lucide/svelte/icons/chevrons-down-up';
import Quote from '@lucide/svelte/icons/quote';
import { appToast } from '$lib/features/toast/toast';

export interface SlashMenuItem {
	id: string;
	label: string;
	// eslint-disable-next-line @typescript-eslint/no-explicit-any
	icon: any;
	shortcut?: string;
	group: string;
	keywords: string[];
	action: (editor: Editor, opts?: { uploadUrl?: string }) => void;
}

export interface SlashGroup {
	name: string;
	items: SlashMenuItem[];
}

const ITEMS: SlashMenuItem[] = [
	// --- Headings ---
	{
		id: 'h1',
		label: 'Heading 1',
		icon: Heading1,
		shortcut: '⌘⌥1',
		group: 'Headings',
		keywords: ['heading', 'h1', 'title', 'large'],
		action: (editor) => editor.chain().focus().toggleHeading({ level: 1 }).run()
	},
	{
		id: 'h2',
		label: 'Heading 2',
		icon: Heading2,
		shortcut: '⌘⌥2',
		group: 'Headings',
		keywords: ['heading', 'h2', 'subtitle', 'medium'],
		action: (editor) => editor.chain().focus().toggleHeading({ level: 2 }).run()
	},
	{
		id: 'h3',
		label: 'Heading 3',
		icon: Heading3,
		shortcut: '⌘⌥3',
		group: 'Headings',
		keywords: ['heading', 'h3', 'small'],
		action: (editor) => editor.chain().focus().toggleHeading({ level: 3 }).run()
	},

	// --- Lists ---
	{
		id: 'bullet-list',
		label: 'Bulleted list',
		icon: List,
		shortcut: '⌘⇧8',
		group: 'Lists',
		keywords: ['bullet', 'list', 'unordered', 'ul'],
		action: (editor) => editor.chain().focus().toggleBulletList().run()
	},
	{
		id: 'numbered-list',
		label: 'Numbered list',
		icon: ListOrdered,
		shortcut: '⌘⇧9',
		group: 'Lists',
		keywords: ['numbered', 'list', 'ordered', 'ol'],
		action: (editor) => editor.chain().focus().toggleOrderedList().run()
	},
	{
		id: 'checklist',
		label: 'Checklist',
		icon: ListChecks,
		shortcut: '⌘⇧7',
		group: 'Lists',
		keywords: ['checklist', 'task', 'todo', 'check'],
		action: (editor) => editor.chain().focus().toggleTaskList().run()
	},

	// --- Media ---
	{
		id: 'insert-media',
		label: 'Insert media...',
		icon: ImagePlus,
		group: 'Media',
		keywords: ['image', 'media', 'photo', 'picture', 'upload'],
		action: (_editor, opts) => {
			if (!opts?.uploadUrl) {
				appToast.info('No upload URL configured');
				return;
			}
			// Trigger file picker — the RichEditor will handle this via a callback
			const input = document.createElement('input');
			input.type = 'file';
			input.accept = 'image/*';
			input.onchange = () => {
				const file = input.files?.[0];
				if (file) {
					// Dispatch custom event that RichEditor listens for
					window.dispatchEvent(
						new CustomEvent('slash:upload-image', { detail: { file, editor: _editor } })
					);
				}
			};
			input.click();
		}
	},
	{
		id: 'insert-gif',
		label: 'Insert gif...',
		icon: Smile,
		group: 'Media',
		keywords: ['gif', 'giphy', 'animation'],
		action: () => appToast.info('GIF insertion coming soon')
	},
	{
		id: 'attach-files',
		label: 'Attach files...',
		icon: Paperclip,
		shortcut: '⌘⇧U',
		group: 'Media',
		keywords: ['attach', 'file', 'upload', 'document'],
		action: (_editor, opts) => {
			if (!opts?.uploadUrl) {
				appToast.info('No upload URL configured');
				return;
			}
			const input = document.createElement('input');
			input.type = 'file';
			input.multiple = true;
			input.onchange = () => {
				const files = Array.from(input.files ?? []);
				if (files.length > 0) {
					window.dispatchEvent(
						new CustomEvent('slash:upload-files', { detail: { files, editor: _editor } })
					);
				}
			};
			input.click();
		}
	},

	// --- Advanced ---
	{
		id: 'code-block',
		label: 'Code block',
		icon: Code2,
		shortcut: '⌘⇧\\',
		group: 'Advanced',
		keywords: ['code', 'block', 'snippet', 'pre'],
		action: (editor) => editor.chain().focus().toggleCodeBlock().run()
	},
	{
		id: 'diagram',
		label: 'Diagram',
		icon: GitBranch,
		group: 'Advanced',
		keywords: ['diagram', 'mermaid', 'chart', 'flow'],
		action: () => appToast.info('Diagrams coming soon')
	},
	{
		id: 'collapsible',
		label: 'Collapsible section',
		icon: ChevronsDownUp,
		group: 'Advanced',
		keywords: ['collapsible', 'toggle', 'details', 'accordion', 'expand'],
		action: () => appToast.info('Collapsible sections coming soon')
	},
	{
		id: 'blockquote',
		label: 'Blockquote',
		icon: Quote,
		shortcut: '⌥⇧.',
		group: 'Advanced',
		keywords: ['quote', 'blockquote', 'callout'],
		action: (editor) => editor.chain().focus().toggleBlockquote().run()
	}
];

// Build groups preserving insertion order
const GROUP_ORDER = ['Headings', 'Lists', 'Media', 'Advanced'];

export const SLASH_ITEMS = ITEMS;

export const SLASH_GROUPS: SlashGroup[] = GROUP_ORDER.map((name) => ({
	name,
	items: ITEMS.filter((item) => item.group === name)
}));

/**
 * Merge host-supplied slash items (docs add wiki links and tables) into the
 * shared groups; unknown group names are appended after the built-ins.
 */
function groupsWithExtras(extra: SlashMenuItem[]): SlashGroup[] {
	if (extra.length === 0) return SLASH_GROUPS;
	const groups = SLASH_GROUPS.map((group) => ({
		...group,
		items: [...group.items, ...extra.filter((item) => item.group === group.name)]
	}));
	const extraGroups = [...new Set(extra.map((item) => item.group))]
		.filter((name) => !GROUP_ORDER.includes(name))
		.map((name) => ({ name, items: extra.filter((item) => item.group === name) }));
	return [...groups, ...extraGroups];
}

/** Filter items by query and return grouped results */
export function filterSlashItems(query: string, extra: SlashMenuItem[] = []): SlashGroup[] {
	const groups = groupsWithExtras(extra);
	if (!query) return groups;
	const q = query.toLowerCase();
	return groups
		.map((group) => ({
			...group,
			items: group.items.filter(
				(item) => item.label.toLowerCase().includes(q) || item.keywords.some((kw) => kw.includes(q))
			)
		}))
		.filter((group) => group.items.length > 0);
}

/** Get flat filtered items list (for keyboard navigation indexing) */
export function flatFilteredItems(query: string, extra: SlashMenuItem[] = []): SlashMenuItem[] {
	return filterSlashItems(query, extra).flatMap((g) => g.items);
}
