<script lang="ts">
	import ComboboxPopover from '$lib/components/shared/ComboboxPopover.svelte';
	import Avatar from '$lib/components/shared/Avatar.svelte';
	import * as Command from '$lib/components/ui/command/index.js';
	import { Checkbox } from '$lib/components/ui/checkbox';
	import type { WorkspaceMember } from '$lib/types/workspace';
	import type { Snippet } from 'svelte';
	import { m } from '$lib/paraglide/messages.js';
	import { getLocale, setLocale } from '$lib/paraglide/runtime.js';

	let {
		open = $bindable(false),
		members,
		value = [],
		onchange,
		trigger,
		width = 'w-48',
		align = 'start' as 'start' | 'center' | 'end',
		shortcutKey,
	}: {
		open?: boolean;
		members: WorkspaceMember[];
		value: string[];
		onchange: (memberUserId: string) => void;
		trigger: Snippet;
		width?: string;
		align?: 'start' | 'center' | 'end';
		shortcutKey?: string;
	} = $props();
</script>

<ComboboxPopover bind:open placeholder={m['sharedComponents.selectors.search_members']()} emptyMessage={m['sharedComponents.selectors.no_members']()} {width} {align} {shortcutKey} {trigger}>
	{#each members as member (member.user_id)}
		{@const isAssigned = value.includes(member.user_id)}
		<Command.Item
			value={member.name || member.email}
			keywords={[member.email]}
			onSelect={() => onchange(member.user_id)}
			class="flex items-center gap-2"
		>
			<Checkbox checked={isAssigned} />
			<Avatar name={member.name || member.email} avatarUrl={member.avatar_url} size="xs" />
			{member.name || member.email}
		</Command.Item>
	{/each}
</ComboboxPopover>
