import type { Component } from 'svelte';

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
		.catch(() => {
			// Retried by the next caller; icons render as placeholders meanwhile.
			pending = undefined;
		});
}

export function lucideLibrary(): Record<string, Component> | null {
	return library;
}
