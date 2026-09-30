<script lang="ts">
	import type { Issue } from '$lib/types/issue';
	import type { TeamStatus } from '$lib/types/team-status';
	import type { WorkspaceMember } from '$lib/types/workspace';
	import type { Label } from '$lib/types/label';
	import IssueStatusIcon from './IssueStatusIcon.svelte';
	import IssuePriorityIcon from './IssuePriorityIcon.svelte';
	import SubIssueCounterTag from './SubIssueCounterTag.svelte';
	import IssueContextMenu from './IssueContextMenu.svelte';
	import { AssigneeSelector } from './selectors';
	import Avatar from '$lib/components/shared/Avatar.svelte';
	import { issuesState } from './issues.state.svelte';
	import { appToast } from '$lib/features/toast/toast';
	import { COLUMN_SORT_KEYS, type ColumnSortKey } from './column-sort';
	import { dndzone, dragHandle, TRIGGERS, type DndEvent, type DndEventInfo } from 'svelte-dnd-action';
	import * as DropdownMenu from '$lib/components/ui/dropdown-menu';
	import Archive from '@lucide/svelte/icons/archive';
	import ArrowDownUp from '@lucide/svelte/icons/arrow-down-up';
	import Check from '@lucide/svelte/icons/check';
	import Ellipsis from '@lucide/svelte/icons/ellipsis';
	import Plus from '@lucide/svelte/icons/plus';
	import UserCircle from '@lucide/svelte/icons/circle-user';
	import { m } from '$lib/paraglide/messages.js';

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
		onquickadd,
		sortKey = null,
		onsortchange,
		onarchiveall,
		ondragstate
	}: {
		statusId: string;
		teamStatus: TeamStatus;
		issues: Issue[];
		slug?: string;
		members?: WorkspaceMember[];
		labels?: Label[];
		onissueclick: (issue: Issue) => void;
		onconsider?: (statusId: string, items: Issue[]) => void;
		onfinalize?: (statusId: string, items: Issue[], info: DndEventInfo) => void;
		onquickadd?: (statusId: string) => void;
		sortKey?: ColumnSortKey | null;
		onsortchange?: (statusId: string, key: ColumnSortKey | null) => void;
		onarchiveall?: (statusId: string) => void;
		ondragstate?: (statusId: string, dragging: boolean) => void;
	} = $props();

	// A sorted column is read-only ordering (like Linear): manual drag & drop
	// would immediately fight the comparator.
	const sortingActive = $derived(sortKey !== null);
	const canArchiveAll = $derived(teamStatus.category === 'completed' && issues.length > 0);

	// svelte-dnd-action owns a column's order while a drag involves it: the
	// source from the first move until the drop, any other column while the
	// dragged card is over it. The board keeps background refreshes out of
	// those columns meanwhile.
	let dragSource = false;

	function handleConsider(e: CustomEvent<DndEvent<Issue>>) {
		const { trigger } = e.detail.info;
		if (trigger === TRIGGERS.DRAG_STARTED) {
			dragSource = true;
			ondragstate?.(statusId, true);
		} else if (trigger === TRIGGERS.DRAGGED_ENTERED) {
			ondragstate?.(statusId, true);
		} else if (trigger === TRIGGERS.DRAGGED_LEFT && !dragSource) {
			ondragstate?.(statusId, false);
		}
		onconsider?.(statusId, e.detail.items);
	}

	function handleFinalize(e: CustomEvent<DndEvent<Issue>>) {
		dragSource = false;
		ondragstate?.(statusId, false);
		onfinalize?.(statusId, e.detail.items, e.detail.info);
	}

	function sortOptionLabel(key: ColumnSortKey | null): string {
		if (key === null) return m['board.sort.manual']();
		switch (key) {
			case 'priority':
				return m['common.context_menu.priority']();
			case 'due_date':
				return m['common.context_menu.due_date']();
			case 'updated':
				return m['board.sort.updated']();
			case 'created':
				return m['board.sort.created']();
			case 'title':
				return m['board.sort.title']();
		}
	}

	function assigneeIds(issue: Issue): string[] {
		const ids = (issue.assignees ?? []).map((assignee) => assignee.id);
		if (ids.length === 0 && issue.assignee) return [issue.assignee.id];
		return ids;
	}

	function displayAssignees(issue: Issue) {
		if (issue.assignees && issue.assignees.length > 0) return issue.assignees;
		return issue.assignee ? [issue.assignee] : [];
	}

	function assigneeName(assignee: NonNullable<Issue['assignee']>) {
		return assignee.name ?? assignee.email ?? 'Unassigned';
	}

	function assigneeTitle(issue: Issue) {
		const assignees = displayAssignees(issue);
		return assignees.length > 0 ? assignees.map(assigneeName).join(', ') : m['issue.assignee']();
	}

	async function toggleAssignee(issue: Issue, userId: string) {
		const currentIds = assigneeIds(issue);
		const newIds = currentIds.includes(userId)
			? currentIds.filter((id) => id !== userId)
			: [...currentIds, userId];
		try {
			await issuesState.update(slug, issue.identifier, { assignee_ids: newIds });
		} catch {
			appToast.error(m['issue.toast.failed_assignees']());
		}
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
		{#if onsortchange}
			<DropdownMenu.Root>
				<DropdownMenu.Trigger
					class="rounded p-0.5 text-[var(--color-text-tertiary)] transition-colors hover:bg-[var(--color-bg-tertiary)] hover:text-[var(--color-text-primary)] {sortingActive
						? 'text-[var(--color-text-primary)]'
						: ''}"
					title={m['board.sort.button']()}
					aria-label={m['board.sort.button']()}
				>
						<ArrowDownUp size={14} />
					</DropdownMenu.Trigger>
				<DropdownMenu.Content align="start" class="w-44">
					{#each [null, ...COLUMN_SORT_KEYS] as key, i (key ?? 'manual')}
						{#if i === 1}
							<DropdownMenu.Separator />
						{/if}
						<DropdownMenu.Item onclick={() => onsortchange?.(statusId, key)}>
							<span class="flex w-full items-center gap-2">
								<span class="flex h-4 w-4 items-center justify-center">
									{#if sortKey === key}<Check size={12} />{/if}
								</span>
								{sortOptionLabel(key)}
							</span>
						</DropdownMenu.Item>
					{/each}
				</DropdownMenu.Content>
			</DropdownMenu.Root>
		{/if}
		{#if canArchiveAll}
			<DropdownMenu.Root>
				<DropdownMenu.Trigger
					class="rounded p-0.5 text-[var(--color-text-tertiary)] transition-colors hover:bg-[var(--color-bg-tertiary)] hover:text-[var(--color-text-primary)]"
					title={m['issues.archive_all.column_action']()}
					aria-label={m['issues.archive_all.column_action']()}
				>
					<Ellipsis size={14} />
				</DropdownMenu.Trigger>
				<DropdownMenu.Content align="start" class="w-44">
					<DropdownMenu.Item onclick={() => onarchiveall?.(statusId)}>
						<span class="flex items-center gap-2">
							<Archive size={14} class="shrink-0 text-[var(--color-text-tertiary)]" />
							{m['issues.archive_all.column_action']()}
						</span>
					</DropdownMenu.Item>
				</DropdownMenu.Content>
			</DropdownMenu.Root>
		{/if}
	</div>

	<div
		class="flex-1 space-y-1.5 overflow-y-auto px-1 pb-4 min-h-[60px]"
		use:dndzone={{ items: issues, type: 'issue', dropTargetStyle: {}, dragDisabled: sortingActive, dropFromOthersDisabled: sortingActive }}
		onconsider={handleConsider}
		onfinalize={handleFinalize}
	>
		{#each issues as issue (issue.id)}
			{@const assignees = displayAssignees(issue)}
			<IssueContextMenu {issue} {slug} {members} {labels}>
				<button
					class="relative w-full rounded-md border border-[var(--app-border)] bg-[var(--color-bg-secondary)] px-3 pt-3 pb-7 text-left transition-colors hover:border-[var(--app-border-hover)]"
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

					<!-- Assignee (bottom-right) -->
					<!-- svelte-ignore a11y_click_events_have_key_events -->
					<!-- svelte-ignore a11y_no_static_element_interactions -->
					<span
						class="absolute bottom-1.5 right-1.5"
						onclick={(e) => e.stopPropagation()}
						onkeydown={(e) => e.stopPropagation()}
						role="presentation"
					>
						<AssigneeSelector
							{members}
							value={assigneeIds(issue)}
							width="w-52"
							align="end"
							onchange={(userId) => toggleAssignee(issue, userId)}
						>
								{#snippet trigger()}
									<span
										class="flex h-5 min-w-5 cursor-pointer items-center justify-center rounded-full text-[var(--color-text-tertiary)] transition-all hover:text-[var(--color-text-primary)]"
										title={assigneeTitle(issue)}
									>
										{#if assignees.length > 0}
											<span class="flex -space-x-2 rounded-full hover:ring-2 hover:ring-[var(--app-accent)]">
												{#each assignees as assignee}
													<span class="rounded-full ring-1 ring-[var(--color-bg)]" title={assigneeName(assignee)}>
														<Avatar name={assigneeName(assignee)} avatarUrl={assignee.avatar_url} size="xs" />
													</span>
												{/each}
											</span>
										{:else}
											<UserCircle size={16} />
										{/if}
									</span>
								{/snippet}
						</AssigneeSelector>
					</span>
				</button>
			</IssueContextMenu>
		{/each}
	</div>
</div>
