import type { Component } from 'svelte';

export const LEGACY_ICON_NAMES: Record<string, string> = {
	box: 'Box',
	'circle-dot': 'CircleDot',
	layers: 'Layers',
	settings: 'Settings',
	shield: 'ShieldCheck',
	'square-user': 'SquareUser',
	users: 'Users'
};

// Team icons can be any Lucide icon, stored by export name (aliases
// included). The full set is ~800 KB of JavaScript, so it loads on first
// use instead of riding along with every page that shows a team.
let library = $state<Record<string, Component> | null>(null);
let pending: Promise<void> | undefined;

export function loadLucideLibrary(): void {
	pending ??= import('@lucide/svelte')
		.then((module) => {
			library = module as unknown as Record<string, Component>;
		})
		.catch((error) => {
			console.error('Failed to load team icons', error);
			// Retried by the next caller; icons render as placeholders meanwhile.
			pending = undefined;
		});
}

export function lucideLibrary(): Record<string, Component> | null {
	return library;
}
