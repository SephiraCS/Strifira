<script lang="ts">
	import type { Team } from '$lib/types/team';
	import { getStableTeamColor } from '$lib/features/layout/sidebar.state.svelte';
	import { loadLucideLibrary, lucideLibrary } from './lucide-library.svelte';

	const LEGACY_ICON_NAMES: Record<string, string> = {
		box: 'Box',
		'circle-dot': 'CircleDot',
		layers: 'Layers',
		settings: 'Settings',
		shield: 'ShieldCheck',
		'square-user': 'SquareUser',
		users: 'Users'
	};

	function resolveIconName(icon?: string | null): string {
		if (!icon || icon.startsWith('emoji:')) return 'SquareUser';
		return LEGACY_ICON_NAMES[icon] ?? icon;
	}

	let {
		team,
		size = 16,
		class: className = 'shrink-0'
	}: {
		team: Team;
		size?: number;
		class?: string;
	} = $props();

	loadLucideLibrary();

	const library = $derived(lucideLibrary());
	const Icon = $derived(library ? (library[resolveIconName(team.icon)] ?? library.SquareUser) : null);
	const emoji = $derived(team.icon?.startsWith('emoji:') ? team.icon.slice(6) : null);
	const color = $derived(getStableTeamColor(team));
</script>

{#if emoji}
	<span class={className} style="font-size: {size}px; line-height: 1" aria-hidden="true">{emoji}</span>
{:else if Icon}
	<Icon {size} class={className} style="color: {color}" />
{:else}
	<span class={className} style="display: inline-block; width: {size}px; height: {size}px" aria-hidden="true"></span>
{/if}
