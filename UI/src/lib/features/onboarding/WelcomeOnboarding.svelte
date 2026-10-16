<script lang="ts">
	import { goto } from '$app/navigation';
	import { createTeam } from '$lib/api/teams';
	import { sidebarState } from '$lib/features/layout/sidebar.state.svelte';
	import CreateTeamDialog from '$lib/features/teams/CreateTeamDialog.svelte';
	import { appToast } from '$lib/features/toast/toast';
	import { Button } from '$lib/components/ui/button';
	import { m } from '$lib/paraglide/messages.js';
	import Circle from '@lucide/svelte/icons/circle';
	import CircleCheck from '@lucide/svelte/icons/circle-check';

	let { slug }: { slug: string } = $props();

	let showCreateTeam = $state(false);
	const teamCreated = $derived(sidebarState.teams.length > 0);

	async function handleCreateTeam(data: { name: string; key: string; description?: string }) {
		try {
			const team = await createTeam(slug, data);
			sidebarState.addTeam(team);
			appToast.success(m['sidebar.team_created']());
		} catch (err: any) {
			appToast.apiError(err, m['sidebar.failed_create_team']());
		}
	}
</script>

<div class="flex h-full flex-col items-center justify-center px-6">
	<div class="w-full max-w-sm rounded-xl border border-[var(--app-border)] bg-[var(--color-bg-secondary)] p-5">
		<h2 class="text-sm font-semibold text-[var(--color-text-primary)]">{m['onboarding.title']()}</h2>
		<p class="mt-1 text-xs text-[var(--color-text-tertiary)]">{m['onboarding.description']()}</p>

		<ol class="mt-4 space-y-3.5">
			<li class="flex items-start gap-2.5">
				{#if teamCreated}
					<CircleCheck size={16} class="mt-0.5 shrink-0 text-[var(--color-success)]" />
					<span class="text-sm text-[var(--color-text-secondary)] line-through">{m['onboarding.step_team']()}</span>
				{:else}
					<Circle size={16} class="mt-0.5 shrink-0 text-[var(--app-accent)]" />
					<div class="flex-1">
						<p class="text-sm text-[var(--color-text-primary)]">{m['onboarding.step_team']()}</p>
						<Button size="sm" class="mt-2 w-full" onclick={() => (showCreateTeam = true)}>
							{m['onboarding.cta_team']()}
						</Button>
					</div>
				{/if}
			</li>
			<li class="flex items-start gap-2.5">
				{#if teamCreated}
					<Circle size={16} class="mt-0.5 shrink-0 text-[var(--app-accent)]" />
					<div class="flex-1">
						<p class="text-sm text-[var(--color-text-primary)]">{m['onboarding.step_issue']()}</p>
						<Button
							variant="outline"
							size="sm"
							class="mt-2 w-full"
							onclick={() => goto(`/${slug}/teams/${sidebarState.teams[0].id}`)}
						>
							{m['onboarding.cta_issue']()}
						</Button>
					</div>
				{:else}
					<Circle size={16} class="mt-0.5 shrink-0 text-[var(--color-text-tertiary)] opacity-50" />
					<p class="text-sm text-[var(--color-text-tertiary)]">{m['onboarding.step_issue']()}</p>
				{/if}
			</li>
			<li class="flex items-start gap-2.5">
				{#if teamCreated}
					<Circle size={16} class="mt-0.5 shrink-0 text-[var(--app-accent)]" />
					<div class="flex-1">
						<p class="text-sm text-[var(--color-text-primary)]">{m['onboarding.step_members']()}</p>
						<Button
							variant="outline"
							size="sm"
							class="mt-2 w-full"
							onclick={() => goto(`/${slug}/settings/members`)}
						>
							{m['onboarding.cta_members']()}
						</Button>
					</div>
				{:else}
					<Circle size={16} class="mt-0.5 shrink-0 text-[var(--color-text-tertiary)] opacity-50" />
					<p class="text-sm text-[var(--color-text-tertiary)]">{m['onboarding.step_members']()}</p>
				{/if}
			</li>
		</ol>
	</div>
</div>

<CreateTeamDialog bind:open={showCreateTeam} onsubmit={handleCreateTeam} />
