<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { page } from '$app/state';
	import { listIssues, triageAccept, triageDecline } from '$lib/api/issues';
	import type { Issue } from '$lib/types/issue';
	import type { PaginatedResponse } from '$lib/types/common';
	import IssueStatusIcon from '$lib/features/issues/IssueStatusIcon.svelte';
	import IssuePriorityIcon from '$lib/features/issues/IssuePriorityIcon.svelte';
	import EmptyState from '$lib/components/shared/EmptyState.svelte';
	import { Badge } from '$lib/components/ui/badge';
	import { Button } from '$lib/components/ui/button';
	import { Kbd } from '$lib/components/ui/kbd';
	import { appToast } from '$lib/features/toast/toast';
	import { formatRelativeTime } from '$lib/utils/format';
	import { m } from '$lib/paraglide/messages.js';
	import { getLocale, setLocale } from '$lib/paraglide/runtime.js';
	import CheckCircle2 from '@lucide/svelte/icons/circle-check';
	import XCircle from '@lucide/svelte/icons/circle-x';
	import SquareUser from '@lucide/svelte/icons/square-user';
	import ShieldCheck from '@lucide/svelte/icons/shield-check';
	import ChevronRight from '@lucide/svelte/icons/chevron-right';
	import { sidebarState } from '$lib/features/layout/sidebar.state.svelte';
	import SidebarToggle from '$lib/components/layout/SidebarToggle.svelte';

	const slug = $derived(page.params.workspaceSlug ?? '');
	const teamId = $derived(page.params.teamId ?? '');

	let issues = $state<Issue[]>([]);
	let loading = $state(true);
	let selectedIndex = $state(0);

	let selectedIssue = $derived(issues.length > 0 && selectedIndex >= 0 ? issues[selectedIndex] : null);

	onMount(async () => {
		await loadIssues();
	});

	async function loadIssues() {
		loading = true;
		try {
			const res: PaginatedResponse<Issue> = await listIssues(slug, {
				team: teamId,
				triaged: 'false',
				per_page: '100'
			});
			issues = res.data;
			selectedIndex = 0;
		} finally {
			loading = false;
		}
	}

	async function handleAccept(identifier: string) {
		try {
			await triageAccept(slug, identifier);
			issues = issues.filter((i) => i.identifier !== identifier);
			selectedIndex = Math.min(selectedIndex, issues.length - 1);
			appToast.success(m['triage.accepted']());
		} catch (err: any) {
			appToast.apiError(err, m['triage.failed_accept']());
		}
	}

	async function handleDecline(identifier: string) {
		try {
			await triageDecline(slug, identifier);
			issues = issues.filter((i) => i.identifier !== identifier);
			selectedIndex = Math.min(selectedIndex, issues.length - 1);
			appToast.success(m['triage.declined']());
		} catch (err: any) {
			appToast.apiError(err, m['triage.failed_decline']());
		}
	}

	function handleKeydown(e: KeyboardEvent) {
		const target = e.target as HTMLElement;
		if (target.tagName === 'INPUT' || target.tagName === 'TEXTAREA') return;

		switch (e.key) {
			case 'j':
				e.preventDefault();
				selectedIndex = Math.min(selectedIndex + 1, issues.length - 1);
				break;
			case 'k':
				e.preventDefault();
				selectedIndex = Math.max(selectedIndex - 1, 0);
				break;
			case '1':
				e.preventDefault();
				if (selectedIssue) handleAccept(selectedIssue.identifier);
				break;
			case '3':
				e.preventDefault();
				if (selectedIssue) handleDecline(selectedIssue.identifier);
				break;
		}
	}

	onMount(() => {
		document.addEventListener('keydown', handleKeydown);
		return () => document.removeEventListener('keydown', handleKeydown);
	});
</script>

<div class="flex h-full">
	<!-- Issue list -->
	<div class="flex w-80 shrink-0 flex-col border-r border-[var(--app-border)]">
		<div class="flex h-[49px] items-center gap-3 border-b border-[var(--app-border)] px-4">
			<SidebarToggle />
			<nav class="flex items-center gap-1.5 text-sm">
				{#if sidebarState.getTeam(teamId)}
					<a href="/{slug}/teams/{teamId}" class="flex items-center gap-1.5 text-[var(--color-text-tertiary)] hover:text-[var(--color-text-secondary)]">
						<SquareUser size={14} class="shrink-0" style="color: {sidebarState.getTeamColor(teamId)}" />
						{sidebarState.getTeam(teamId)?.name}
					</a>
					<ChevronRight size={12} class="shrink-0 text-[var(--color-text-tertiary)]" />
				{/if}
				<span class="flex items-center gap-1.5 font-medium text-[var(--color-text-primary)]">
					<ShieldCheck size={14} class="shrink-0" />
					{m['sidebar.triage']()}
				</span>
			</nav>
			<Badge variant="outline" class="text-[10px]">{issues.length}</Badge>
		</div>

		<div class="flex-1 overflow-y-auto">
			{#if !loading && issues.length === 0}
				<EmptyState
					title={m['triage.all_triaged']()}
					description={m['triage.none_waiting']()}
				/>
			{:else}
				{#each issues as issue, i}
					<button
						onclick={() => (selectedIndex = i)}
						class="flex w-full flex-col gap-1 border-b border-[var(--app-border)] px-4 py-3 text-left {selectedIndex === i ? 'bg-[var(--color-bg-hover)]' : 'hover:bg-[var(--color-bg-hover)]'}"
					>
						<div class="flex items-center gap-2">
							<IssuePriorityIcon priority={issue.priority} size={13} />
							<span class="text-xs text-[var(--color-text-tertiary)]">{issue.identifier}</span>
							<span class="ml-auto text-xs text-[var(--color-text-tertiary)]">{formatRelativeTime(issue.created_at, getLocale())}</span>
						</div>
						<p class="line-clamp-2 text-sm text-[var(--color-text-primary)]">{issue.title}</p>
					</button>
				{/each}
			{/if}
		</div>
	</div>

	<!-- Detail panel -->
	<div class="flex-1">
		{#if selectedIssue}
			<div class="flex h-[49px] items-center justify-between border-b border-[var(--app-border)] px-6">
				<div class="flex items-center gap-2">
					<span class="text-sm text-[var(--color-text-tertiary)]">{selectedIssue.identifier}</span>
					<IssueStatusIcon status={selectedIssue.status} />
				</div>
				<div class="flex items-center gap-2">
					<Button size="sm" onclick={() => handleAccept(selectedIssue!.identifier)} class="gap-1.5">
						<CheckCircle2 size={14} />
						{m['triage.accept']()}
						<Kbd class="ml-1">1</Kbd>
					</Button>
					<Button variant="outline" size="sm" onclick={() => handleDecline(selectedIssue!.identifier)} class="gap-1.5">
						<XCircle size={14} />
						{m['triage.decline']()}
						<Kbd class="ml-1">3</Kbd>
					</Button>
				</div>
			</div>

			<div class="p-6">
				<h2 class="text-lg font-semibold text-[var(--color-text-primary)]">{selectedIssue.title}</h2>
				{#if selectedIssue.description}
					<p class="mt-3 whitespace-pre-wrap text-sm text-[var(--color-text-secondary)]">
						{selectedIssue.description}
					</p>
				{/if}

				<div class="mt-6 grid grid-cols-2 gap-3 text-sm">
					<div class="flex items-center gap-2">
						<span class="w-20 text-[var(--color-text-tertiary)]">{m['issue.status']()}</span>
						<div class="flex items-center gap-1.5 text-[var(--color-text-secondary)]">
							<IssueStatusIcon status={selectedIssue.status} />
							{selectedIssue.status.replace('_', ' ')}
						</div>
					</div>
					<div class="flex items-center gap-2">
						<span class="w-20 text-[var(--color-text-tertiary)]">{m['issue.priority']()}</span>
						<div class="flex items-center gap-1.5 text-[var(--color-text-secondary)]">
							<IssuePriorityIcon priority={selectedIssue.priority} />
							{selectedIssue.priority}
						</div>
					</div>
				</div>

				<!-- Keyboard hint -->
				<div class="mt-8 flex items-center gap-4 rounded-md border border-[var(--app-border)] bg-[var(--color-bg-secondary)] px-4 py-3">
					<span class="text-xs text-[var(--color-text-tertiary)]">{m['triage.keyboard_shortcuts']()}</span>
					<div class="flex items-center gap-1 text-xs text-[var(--color-text-secondary)]">
						<Kbd>1</Kbd> {m['triage.accept']()}
					</div>
					<div class="flex items-center gap-1 text-xs text-[var(--color-text-secondary)]">
						<Kbd>3</Kbd> {m['triage.decline']()}
					</div>
					<div class="flex items-center gap-1 text-xs text-[var(--color-text-secondary)]">
						<Kbd>J</Kbd><Kbd>K</Kbd> {m['triage.navigate']()}
					</div>
				</div>
			</div>
		{:else if !loading && issues.length === 0}
			<div class="flex h-full items-center justify-center">
				<div class="text-center">
					<CheckCircle2 size={40} class="mx-auto text-[var(--color-success)]" />
					<p class="mt-3 text-sm font-medium text-[var(--color-text-primary)]">{m['triage.all_caught_up']()}</p>
					<p class="mt-1 text-xs text-[var(--color-text-tertiary)]">{m['triage.none_waiting']()}</p>
				</div>
			</div>
		{/if}
	</div>
</div>
