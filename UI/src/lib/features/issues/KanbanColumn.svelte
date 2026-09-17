<script lang="ts">
	import type { Issue } from '$lib/types/issue';
	import type { TeamStatus } from '$lib/types/team-status';
	import type { WorkspaceMember } from '$lib/types/workspace';
	import type { Label } from '$lib/types/label';
	import IssueStatusIcon from './IssueStatusIcon.svelte';
	import IssuePriorityIcon from './IssuePriorityIcon.svelte';
	import SubIssueCounterTag from './SubIssueCounterTag.svelte';
	import IssueContextMenu from './IssueContextMenu.svelte';
	import { dndzone, dragHandle } from 'svelte-dnd-action';
	import { Plus } from 'lucide-svelte';

	let {
		statusId,
		teamStatus,
		issues,
		slug = '',
		members = [],
		labels = [],
		onissueclick,
		onconsider,
		onfinalize,
		onquickadd
	}: {
		statusId: string;
		teamStatus: TeamStatus;
		issues: Issue[];
		slug?: string;
		members?: WorkspaceMember[];
		labels?: Label[];
		onissueclick: (issue: Issue) => void;
		onconsider?: (statusId: string, items: Issue[]) => void;
		onfinalize?: (statusId: string, items: Issue[]) => void;
		onquickadd?: (statusId: string) => void;
	} = $props();

	function handleConsider(e: CustomEvent<{ items: Issue[] }>) {
		onconsider?.(statusId, e.detail.items);
	}

	function handleFinalize(e: CustomEvent<{ items: Issue[] }>) {
		onfinalize?.(statusId, e.detail.items);
	}
</script>

<div class="flex w-72 shrink-0 flex-col" aria-label={teamStatus.name}>
	<div class="flex items-center gap-2 px-2 py-2">
		<div
			class="flex min-w-0 flex-1 cursor-grab items-center gap-2 select-none active:cursor-grabbing"
			use:dragHandle
			aria-label="Reorder {teamStatus.name} column"
		>
			<IssueStatusIcon category={teamStatus.category} color={teamStatus.color} />
			<span class="text-sm font-medium text-[var(--color-text-primary)]"
				>{teamStatus.name}</span
			>
			<span class="text-xs text-[var(--color-text-tertiary)]">{issues.length}</span>
		</div>
		{#if onquickadd}
			<button
				onclick={() => onquickadd?.(statusId)}
				class="rounded p-0.5 text-[var(--color-text-tertiary)] hover:text-[var(--color-text-primary)] hover:bg-[var(--color-bg-tertiary)] transition-colors"
				title="New issue in {teamStatus.name}"
			>
				<Plus size={14} />
			</button>
		{/if}
	</div>

	<div
		class="flex-1 space-y-1.5 overflow-y-auto px-1 pb-4 min-h-[60px]"
		use:dndzone={{ items: issues, type: 'issue', dropTargetStyle: {} }}
		onconsider={handleConsider}
		onfinalize={handleFinalize}
	>
		{#each issues as issue (issue.id)}
			<IssueContextMenu {issue} {slug} {members} {labels}>
				<button
					class="w-full rounded-md border border-[var(--app-border)] bg-[var(--color-bg-secondary)] p-3 text-left transition-colors hover:border-[var(--app-border-hover)]"
					onclick={() => onissueclick(issue)}
				>
					<div class="flex items-center gap-2">
						<span class="text-xs text-[var(--color-text-tertiary)]">{issue.identifier}</span>
						<IssuePriorityIcon priority={issue.priority} size={14} />
					</div>
					<div class="mt-1 flex items-start gap-2">
						<p class="line-clamp-2 min-w-0 flex-1 text-sm text-[var(--color-text-primary)]">{issue.title}</p>
						<SubIssueCounterTag issue={issue} {slug} {members} onclickissue={onissueclick} compact />
					</div>
				</button>
			</IssueContextMenu>
		{/each}
	</div>
</div>
