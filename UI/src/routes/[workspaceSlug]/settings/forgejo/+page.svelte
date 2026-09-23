<script lang="ts">
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import {
		getForgejoStatus,
		connectForgejo,
		disconnectForgejo,
		listForgejoRepos,
		linkForgejoRepos,
		unlinkForgejoRepo,
		listAutoTransitions,
		updateAutoTransitions,
		listUserMappings,
		updateUserMappings
	} from '$lib/api/forgejo';
	import { forgejoRepositoryHomeUrl } from '$lib/security/forgejo-url';
	import type { ForgejoStatus, ForgejoAvailableRepo, ForgejoAutoTransition, ForgejoUserMapping } from '$lib/types/forgejo';
	import { listTeams } from '$lib/api/teams';
	import { listTeamStatuses } from '$lib/api/team-statuses';
	import { listMembers } from '$lib/api/members';
	import type { WorkspaceMember } from '$lib/types/workspace';
	import { Button } from '$lib/components/ui/button';
	import { Badge } from '$lib/components/ui/badge';
	import { Switch } from '$lib/components/ui/switch';
	import { Input } from '$lib/components/ui/input';
	import { Checkbox } from '$lib/components/ui/checkbox';
	import { appToast } from '$lib/features/toast/toast';
	import { ExternalLink, Plus, Loader2, Search, GitBranch, Trash2 } from 'lucide-svelte';
	import { m } from '$lib/paraglide/messages.js';

	const DEFAULT_INSTANCE_URL = 'https://git.terrascale.tech';

	const slug = $derived(page.params.workspaceSlug ?? '');
	let status = $state<ForgejoStatus | null>(null);
	let loading = $state(true);
	let availableRepos = $state<ForgejoAvailableRepo[]>([]);
	let loadingRepos = $state(false);
	let showRepoSelector = $state(false);
	let selectedRepoIds = $state<Set<number>>(new Set());
	let transitions = $state<ForgejoAutoTransition[]>([]);
	let repoSearch = $state('');
	let savingRepos = $state(false);

	let instanceUrl = $state(DEFAULT_INSTANCE_URL);
	let token = $state('');
	let connecting = $state(false);
	let workflowStates = $state<{ value: string; label: string }[]>([]);
	let members = $state<WorkspaceMember[]>([]);
	let userMappings = $state<ForgejoUserMapping[]>([]);
	let mappingLogin = $state('');
	let mappingUserID = $state('');
	let savingMappings = $state(false);

	const filteredRepos = $derived(
		repoSearch
			? availableRepos.filter((r) => r.full_name.toLowerCase().includes(repoSearch.toLowerCase()))
			: availableRepos
	);

	const allFilteredSelected = $derived(
		filteredRepos.length > 0 && filteredRepos.every((r) => selectedRepoIds.has(r.forgejo_repo_id))
	);

	const someFilteredSelected = $derived(
		!allFilteredSelected && filteredRepos.some((r) => selectedRepoIds.has(r.forgejo_repo_id))
	);

	function toggleSelectAll() {
		const next = new Set(selectedRepoIds);
		if (allFilteredSelected) {
			for (const r of filteredRepos) next.delete(r.forgejo_repo_id);
		} else {
			for (const r of filteredRepos) next.add(r.forgejo_repo_id);
		}
		selectedRepoIds = next;
	}

	onMount(async () => {
		loadWorkflowStates();
		listMembers(slug).then((result) => { members = result; }).catch(() => { members = []; });
		try {
			status = await getForgejoStatus(slug);
			if (status.connected) {
				transitions = await listAutoTransitions(slug);
				userMappings = await listUserMappings(slug);
			}
		} catch (err: any) {
			console.error('Forgejo status error:', err);
		} finally {
			loading = false;
		}
	});

	// Automation targets are workspace-level but resolve against each team's
	// own workflow at fire time — offer the union of the states actually
	// configured on the workspace's teams, deduped by slug. Teams that lack
	// the selected state simply stay unchanged.
	async function loadWorkflowStates() {
		try {
			const teams = await listTeams(slug);
			const perTeam = await Promise.all(
				teams.map((team) => listTeamStatuses(slug, team.id).catch(() => []))
			);
			const bySlug = new Map<string, { names: Set<string>; position: number }>();
			perTeam.flat().forEach((state) => {
				const entry = bySlug.get(state.slug) ?? { names: new Set<string>(), position: state.position };
				entry.names.add(state.name);
				entry.position = Math.min(entry.position, state.position);
				bySlug.set(state.slug, entry);
			});
			workflowStates = [...bySlug.entries()]
				.map(([slug, entry]) => ({ value: slug, label: [...entry.names].sort().join(' / ') }))
				.sort((a, b) => a.label.localeCompare(b.label));
		} catch {
			workflowStates = [];
		}
	}

	async function handleConnect() {
		connecting = true;
		try {
			await connectForgejo(slug, instanceUrl, token);
			token = '';
			status = await getForgejoStatus(slug);
			transitions = await listAutoTransitions(slug);
			userMappings = await listUserMappings(slug);
			appToast.success(m['settings.forgejo.connected_toast']());
		} catch (err: any) {
			appToast.apiError(err, m['settings.forgejo.failed_connect']());
		} finally {
			connecting = false;
		}
	}

	async function handleDisconnect() {
		try {
			await disconnectForgejo(slug);
			status = await getForgejoStatus(slug);
			transitions = [];
			userMappings = [];
			appToast.success(m['settings.forgejo.disconnected']());
		} catch {
			appToast.error(m['settings.forgejo.failed_disconnect']());
		}
	}

	async function loadAvailableRepos() {
		loadingRepos = true;
		showRepoSelector = true;
		repoSearch = '';
		try {
			availableRepos = await listForgejoRepos(slug);
			selectedRepoIds = new Set(availableRepos.filter((r) => r.linked).map((r) => r.forgejo_repo_id));
		} catch {
			appToast.error(m['settings.forgejo.failed_load_repos']());
		} finally {
			loadingRepos = false;
		}
	}

	function toggleRepo(repoId: number) {
		const next = new Set(selectedRepoIds);
		if (next.has(repoId)) {
			next.delete(repoId);
		} else {
			next.add(repoId);
		}
		selectedRepoIds = next;
	}

	async function saveRepoSelection() {
		savingRepos = true;
		try {
			const newIds = [...selectedRepoIds].filter(
				(id) => !availableRepos.find((r) => r.forgejo_repo_id === id && r.linked)
			);
			if (newIds.length > 0) {
				try {
					await linkForgejoRepos(slug, newIds);
					appToast.success(m['settings.forgejo.repos_linked']());
				} catch {
					appToast.error(m['settings.forgejo.failed_link_repos']());
				}
			}
			for (const repo of availableRepos.filter((r) => r.linked)) {
				if (!selectedRepoIds.has(repo.forgejo_repo_id)) {
					const linked = status?.repos.find((r) => r.forgejo_repo_id === repo.forgejo_repo_id);
					if (linked) {
						try {
							await unlinkForgejoRepo(slug, linked.id);
						} catch {
							/* ignore */
						}
					}
				}
			}
			status = await getForgejoStatus(slug);
			showRepoSelector = false;
			repoSearch = '';
		} finally {
			savingRepos = false;
		}
	}

	const TRANSITION_EVENTS: { event: ForgejoAutoTransition['event']; label: () => string; description: () => string }[] = $derived([
		{ event: 'branch_created', label: () => m['settings.forgejo.branch_created'](), description: () => m['settings.forgejo.branch_created_desc']() },
		{ event: 'pr_opened', label: () => m['settings.forgejo.pr_opened'](), description: () => m['settings.forgejo.pr_opened_desc']() },
		{ event: 'pr_checks_green', label: () => m['settings.forgejo.pr_checks_green'](), description: () => m['settings.forgejo.pr_checks_green_desc']() },
		{ event: 'pr_merged', label: () => m['settings.forgejo.pr_merged'](), description: () => m['settings.forgejo.pr_merged_desc']() }
	]);

	// The configured states, plus the rule's current slug when it no longer
	// exists on any team (so a stale selection stays visible and changeable).
	function pickerOptions(currentSlug: string): { value: string; label: string }[] {
		const options = [{ value: '', label: m['settings.forgejo.no_transition']() }, ...workflowStates];
		if (currentSlug && !workflowStates.some((o) => o.value === currentSlug)) {
			options.push({ value: currentSlug, label: currentSlug });
		}
		return options;
	}

	function ruleFor(event: ForgejoAutoTransition['event']): ForgejoAutoTransition | undefined {
		return transitions.find((t) => t.event === event);
	}

	async function patchRule(event: ForgejoAutoTransition['event'], changes: Partial<ForgejoAutoTransition>) {
		const previous = ruleFor(event);
		if (!previous) return;
		const updated = { ...previous, ...changes };
		transitions = transitions.map((t) => (t.event === event ? updated : t));
		try {
			await updateAutoTransitions(slug, [updated]);
		} catch {
			transitions = transitions.map((t) => (t.event === event ? previous : t));
			appToast.error(m['settings.forgejo.failed_update']());
		}
	}

	async function saveMappings(next: ForgejoUserMapping[]): Promise<boolean> {
		savingMappings = true;
		try {
			await updateUserMappings(slug, next);
			userMappings = await listUserMappings(slug);
			return true;
		} catch (err: any) {
			appToast.apiError(err, m['settings.forgejo.failed_update']());
			return false;
		} finally {
			savingMappings = false;
		}
	}

	async function addMapping() {
		const forgejoLogin = mappingLogin.trim().toLowerCase();
		if (!forgejoLogin || !mappingUserID) return;
		const next = [...userMappings.filter((mapping) => mapping.forgejo_login !== forgejoLogin),
			{ forgejo_login: forgejoLogin, user_id: mappingUserID }];
		if (await saveMappings(next)) {
			mappingLogin = '';
			mappingUserID = '';
		}
	}
</script>

<div class="mx-auto max-w-2xl space-y-8 p-6">
	<div>
		<h2 class="text-lg font-semibold text-[var(--color-text-primary)]">{m['settings.forgejo.title']()}</h2>
		<p class="mt-1 text-sm text-[var(--color-text-tertiary)]">
			{m['settings.forgejo.desc']()}
		</p>
	</div>

	{#if loading}
		<div class="flex items-center justify-center py-12">
			<Loader2 size={20} class="animate-spin text-[var(--color-text-tertiary)]" />
		</div>
	{:else if !status?.connected}
		<!-- State 1: not connected — token form -->
		<div class="rounded-lg border border-[var(--app-border)] p-6">
			<div class="flex items-center gap-3">
				<div class="flex h-10 w-10 items-center justify-center rounded-lg bg-[var(--color-bg-tertiary)]">
					<GitBranch size={20} class="text-[var(--color-text-secondary)]" />
				</div>
				<div class="flex-1">
					<h3 class="text-sm font-medium text-[var(--color-text-primary)]">{m['settings.forgejo.setup']()}</h3>
					<p class="text-xs text-[var(--color-text-tertiary)]">{m['settings.forgejo.setup_desc']()}</p>
				</div>
			</div>

			<div class="mt-4 space-y-3">
				<div>
					<label for="forgejo-instance" class="mb-1 block text-xs font-medium text-[var(--color-text-secondary)]">
						{m['settings.forgejo.instance_url']()}
					</label>
					<Input id="forgejo-instance" type="url" bind:value={instanceUrl} placeholder={DEFAULT_INSTANCE_URL} class="h-9 text-sm" />
					<p class="mt-1 text-xs text-[var(--color-text-tertiary)]">{m['settings.forgejo.instance_url_desc']()}</p>
				</div>
				<div>
					<label for="forgejo-token" class="mb-1 block text-xs font-medium text-[var(--color-text-secondary)]">
						{m['settings.forgejo.token']()}
					</label>
					<Input
						id="forgejo-token"
						type="password"
						autocomplete="off"
						bind:value={token}
						placeholder={m['settings.forgejo.token_placeholder']()}
						class="h-9 text-sm font-mono"
					/>
					<p class="mt-1 text-xs text-[var(--color-text-tertiary)]">{m['settings.forgejo.token_desc']()}</p>
				</div>
				<div class="flex justify-end">
					<Button size="sm" onclick={handleConnect} disabled={connecting || token.trim().length < 20 || !instanceUrl.trim()}>
						{#if connecting}
							<Loader2 size={14} class="animate-spin" />
						{/if}
						{m['settings.forgejo.connect']()}
					</Button>
				</div>
			</div>
		</div>

		<div class="rounded-md bg-[var(--color-bg-secondary)] px-4 py-3">
			<p class="text-xs text-[var(--color-text-tertiary)]">
				{m['settings.forgejo.setup_note']()}
			</p>
		</div>
	{:else}
		<!-- State 2: connected -->
		<div class="space-y-6">
			<!-- Connection info -->
			<div class="rounded-lg border border-[var(--app-border)] p-4">
				<div class="flex items-center justify-between">
					<div class="flex items-center gap-3">
						<div class="flex h-8 w-8 items-center justify-center rounded-lg bg-[var(--color-bg-tertiary)]">
							<GitBranch size={16} class="text-[var(--color-text-secondary)]" />
						</div>
						<div>
							<div class="flex items-center gap-2">
								<span class="text-sm font-medium text-[var(--color-text-primary)]">{status.account?.login}</span>
								{#if status.global_config}
									<Badge variant="outline" class="text-[10px]">{m['settings.forgejo.global']()}</Badge>
								{/if}
							</div>
							<p class="text-xs text-[var(--color-text-tertiary)]">{status.instance_url}</p>
						</div>
					</div>
					{#if !status.global_config}
						<Button variant="destructive" size="sm" onclick={handleDisconnect}>
							{m['settings.forgejo.disconnect']()}
						</Button>
					{/if}
				</div>
			</div>

			<!-- Linked repos -->
			<div>
				<div class="flex items-center justify-between">
					<h3 class="text-sm font-medium text-[var(--color-text-primary)]">{m['settings.forgejo.linked_repos']()}</h3>
					{#if !showRepoSelector}
						<button
							onclick={loadAvailableRepos}
							class="flex items-center gap-1.5 rounded-md px-2.5 py-1 text-xs font-medium text-[var(--color-text-secondary)] hover:bg-[var(--color-bg-hover)] hover:text-[var(--color-text-primary)] transition-colors"
						>
							<Plus size={13} />
							{m['settings.forgejo.manage']()}
						</button>
					{/if}
				</div>

				{#if showRepoSelector}
					<div class="mt-3 rounded-lg border border-[var(--app-border)] p-3">
						{#if loadingRepos}
							<div class="flex items-center justify-center py-8">
								<Loader2 size={18} class="animate-spin text-[var(--color-text-tertiary)]" />
							</div>
						{:else}
							<div class="relative">
								<Search size={14} class="absolute left-2.5 top-1/2 -translate-y-1/2 text-[var(--color-text-tertiary)]" />
								<Input
									type="text"
									placeholder={m['settings.forgejo.search_repos']()}
									bind:value={repoSearch}
									class="pl-8 h-8 text-sm"
								/>
							</div>

							<button
								onclick={toggleSelectAll}
								class="mt-2 flex w-full items-center gap-2.5 rounded-md px-2 py-1.5 text-sm text-[var(--color-text-secondary)] hover:bg-[var(--color-bg-hover)]"
							>
								<Checkbox checked={allFilteredSelected} indeterminate={someFilteredSelected} class="pointer-events-none" />
								<span class="text-xs">{repoSearch ? m['settings.forgejo.select_all_filtered']({ n: filteredRepos.length }) : m['settings.forgejo.select_all']()} ({filteredRepos.length})</span>
							</button>

							<div class="mt-1 max-h-64 space-y-0.5 overflow-y-auto">
								{#each filteredRepos as repo}
									<button
										onclick={() => toggleRepo(repo.forgejo_repo_id)}
										class="flex w-full items-center gap-2.5 rounded-md px-2 py-1.5 text-sm hover:bg-[var(--color-bg-hover)] {selectedRepoIds.has(repo.forgejo_repo_id) ? 'bg-[var(--color-bg-hover)]/50' : ''}"
									>
										<Checkbox checked={selectedRepoIds.has(repo.forgejo_repo_id)} class="pointer-events-none" />
										<GitBranch size={14} class="shrink-0 text-[var(--color-text-tertiary)]" />
										<span class="truncate text-[var(--color-text-primary)]">{repo.full_name}</span>
										{#if repo.private}
											<Badge variant="outline" class="ml-auto shrink-0 text-[9px]">{m['settings.forgejo.private']()}</Badge>
										{/if}
									</button>
								{:else}
									<p class="py-4 text-center text-xs text-[var(--color-text-tertiary)]">
										{repoSearch ? m['settings.forgejo.no_repos_match']() : m['settings.forgejo.no_repos_available']()}
									</p>
								{/each}
							</div>

							<div class="mt-3 flex items-center justify-between border-t border-[var(--app-border)] pt-3">
								<span class="text-xs text-[var(--color-text-tertiary)]">
									{m['settings.forgejo.n_selected']({ n: selectedRepoIds.size })}
								</span>
								<div class="flex gap-2">
									<Button variant="outline" size="sm" onclick={() => { showRepoSelector = false; repoSearch = ''; }}>{m['settings.cancel']()}</Button>
									<Button size="sm" onclick={saveRepoSelection} disabled={savingRepos}>
										{#if savingRepos}
											<Loader2 size={14} class="animate-spin" />
										{/if}
										{m['settings.forgejo.save']()}
									</Button>
								</div>
							</div>
						{/if}
					</div>
				{:else if status.repos.length === 0}
					<p class="mt-3 text-sm text-[var(--color-text-tertiary)]">{m['settings.forgejo.no_repos_linked']()}</p>
				{:else}
					<div class="mt-3 space-y-1">
						{#each status.repos as repo}
							{@const repositoryUrl = forgejoRepositoryHomeUrl(repo.full_name, repo.instance_url || status.instance_url || '')}
							<div class="flex items-center justify-between rounded-md border border-[var(--app-border)] px-3 py-2">
								<div class="flex items-center gap-2">
									<GitBranch size={14} class="text-[var(--color-text-tertiary)]" />
									<span class="text-sm text-[var(--color-text-primary)]">{repo.full_name}</span>
									<span class="text-xs text-[var(--color-text-tertiary)]">{repo.default_branch}</span>
								</div>
								{#if repositoryUrl}
									<a href={repositoryUrl} target="_blank" rel="noopener noreferrer" class="text-[var(--color-text-tertiary)] hover:text-[var(--color-text-secondary)]">
										<ExternalLink size={14} />
									</a>
								{/if}
							</div>
						{/each}
					</div>
				{/if}
			</div>

			<div>
				<h3 class="text-sm font-medium text-[var(--color-text-primary)]">{m['settings.forgejo.user_mappings']()}</h3>
				<p class="mt-1 text-xs text-[var(--color-text-tertiary)]">{m['settings.forgejo.user_mappings_desc']()}</p>
				<div class="mt-3 space-y-2">
					{#each userMappings as mapping (mapping.forgejo_login)}
						<div class="flex items-center justify-between gap-3 rounded-md border border-[var(--app-border)] px-3 py-2">
							<span class="min-w-0 truncate text-sm text-[var(--color-text-primary)]">{mapping.forgejo_login} → {members.find((member) => member.user_id === mapping.user_id)?.name ?? m['settings.forgejo.unavailable_assignee']()}</span>
							<button
							aria-label={m['settings.forgejo.remove_mapping']({ login: mapping.forgejo_login })}
								disabled={savingMappings}
								onclick={() => saveMappings(userMappings.filter((item) => item.forgejo_login !== mapping.forgejo_login))}
								class="shrink-0 text-[var(--color-text-tertiary)] hover:text-[var(--color-text-primary)] disabled:opacity-50"
							><Trash2 size={15} /></button>
						</div>
					{/each}
				</div>
				<div class="mt-3 flex flex-wrap items-center gap-2">
					<Input
						aria-label={m['settings.forgejo.forgejo_login']()}
						placeholder={m['settings.forgejo.forgejo_login']()}
						bind:value={mappingLogin}
						class="h-8 min-w-32 flex-1 text-sm"
						maxlength={100}
					/>
					<select
						aria-label={m['settings.forgejo.ku_member']()}
						class="h-8 min-w-36 flex-1 rounded-md border border-[var(--app-border)] bg-[var(--color-bg-secondary)] px-2 text-xs text-[var(--color-text-primary)]"
						bind:value={mappingUserID}
					>
						<option value="">{m['settings.forgejo.ku_member']()}</option>
						{#each members as member (member.user_id)}
							<option value={member.user_id}>{member.name || member.email}</option>
						{/each}
					</select>
					<Button size="sm" onclick={addMapping} disabled={savingMappings || !mappingLogin.trim() || !mappingUserID}>
						<Plus size={14} /> {m['settings.forgejo.add_mapping']()}
					</Button>
				</div>
			</div>

			<!-- Per-event status and assignee rules -->
			<div>
				<h3 class="text-sm font-medium text-[var(--color-text-primary)]">{m['settings.forgejo.automations']()}</h3>
				<p class="mt-1 text-xs text-[var(--color-text-tertiary)]">{m['settings.forgejo.automations_desc']()}</p>
				<div class="mt-3 space-y-2">
					{#each TRANSITION_EVENTS as meta}
						{@const rule = ruleFor(meta.event)}
						{#if rule}
							<div class="flex flex-wrap items-center justify-between gap-3 rounded-md border border-[var(--app-border)] px-3 py-2.5">
								<div class="min-w-0">
									<span class="text-sm text-[var(--color-text-primary)]">{meta.label()}</span>
									<p class="text-xs text-[var(--color-text-tertiary)]">{meta.description()}</p>
								</div>
								<div class="flex flex-wrap items-center gap-2">
									<select
										aria-label={m['settings.forgejo.target_status']()}
										class="h-8 rounded-md border border-[var(--app-border)] bg-[var(--color-bg-secondary)] px-2 text-xs text-[var(--color-text-primary)]"
										value={rule.target_status}
										disabled={!rule.is_active}
										onchange={(e) => {
											const value = (e.currentTarget as HTMLSelectElement).value;
											patchRule(meta.event, { target_status: value, target_status_id: null });
										}}
									>
										{#each pickerOptions(rule.target_status) as option}
											<option value={option.value} selected={rule.target_status === option.value}>{option.label}</option>
										{/each}
									</select>
									<select
										aria-label={m['settings.forgejo.target_assignee']()}
										class="h-8 max-w-40 rounded-md border border-[var(--app-border)] bg-[var(--color-bg-secondary)] px-2 text-xs text-[var(--color-text-primary)]"
										value={rule.assign_forgejo_user ? '__forgejo_user__' : (rule.target_assignee_id ?? '')}
										disabled={!rule.is_active}
										onchange={(e) => {
											const value = (e.currentTarget as HTMLSelectElement).value;
											patchRule(meta.event, {
												target_assignee_id: value && value !== '__forgejo_user__' ? value : null,
												assign_forgejo_user: value === '__forgejo_user__'
											});
										}}
									>
										<option value="">{m['settings.forgejo.keep_assignee']()}</option>
										<option value="__forgejo_user__">{m['settings.forgejo.match_user']()}</option>
										{#each members as member (member.user_id)}
											<option value={member.user_id}>{member.name || member.email}</option>
										{/each}
										{#if rule.target_assignee_id && !members.some((member) => member.user_id === rule.target_assignee_id)}
											<option value={rule.target_assignee_id}>{m['settings.forgejo.unavailable_assignee']()}</option>
										{/if}
									</select>
									<Switch
										checked={rule.is_active}
										onCheckedChange={(v) => patchRule(meta.event, { is_active: v })}
									/>
								</div>
							</div>
						{/if}
					{/each}
				</div>
				<p class="mt-2 text-xs text-[var(--color-text-tertiary)]">{m['settings.forgejo.automations_note']()}</p>
				<p class="mt-1 text-xs text-[var(--color-text-tertiary)]">{m['settings.forgejo.match_user_note']()}</p>
			</div>
		</div>
	{/if}
</div>
