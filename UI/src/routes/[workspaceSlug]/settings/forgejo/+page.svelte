<script lang="ts">
	import { onMount, untrack } from 'svelte';
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
	import type {
		ForgejoStatus,
		ForgejoAvailableRepo,
		ForgejoAutoTransition,
		ForgejoUserMapping
	} from '$lib/types/forgejo';
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
	import ExternalLink from '@lucide/svelte/icons/external-link';
	import Plus from '@lucide/svelte/icons/plus';
	import LoaderCircle from '@lucide/svelte/icons/loader-circle';
	import Search from '@lucide/svelte/icons/search';
	import GitBranch from '@lucide/svelte/icons/git-branch';
	import Trash2 from '@lucide/svelte/icons/trash-2';
	import { m } from '$lib/paraglide/messages.js';
	import { sidebarState } from '$lib/features/layout/sidebar.state.svelte';
	import ErrorState from '$lib/components/shared/ErrorState.svelte';

	const DEFAULT_INSTANCE_URL = 'https://git.terrascale.tech';

	const slug = $derived(page.params.workspaceSlug ?? '');
	let status = $state<ForgejoStatus | null>(null);
	let mounted = $state(false);
	let generation = 0;
	let loadError = $state('');
	let reposError = $state('');
	let automationsError = $state('');
	let mappingsError = $state('');
	let loadingMappings = $state(false);
	let workflowLoadFailed = $state(false);
	let savingEvents = $state<Set<string>>(new Set());
	let ruleErrors = $state<Record<string, string>>({});
	const canManage = $derived(sidebarState.workspaceRole === 'owner');
	let loading = $state(true);
	let availableRepos = $state<ForgejoAvailableRepo[]>([]);
	let loadingRepos = $state(false);
	let showRepoSelector = $state(false);
	let selectedRepoIds = $state<Set<number>>(new Set());
	let transitions = $state<ForgejoAutoTransition[]>([]);
	let repoSearch = $state('');
	let savingRepos = $state(false);
	let repoDiscoveryGeneration = 0;
	let repoDiscoveryContext = $state<string | null>(null);

	let instanceUrl = $state(DEFAULT_INSTANCE_URL);
	let token = $state('');
	let connecting = $state(false);
	let workflowStates = $state<{ value: string; label: string; teams: string[]; missingTeams: string[] }[]>([]);
	let members = $state<WorkspaceMember[]>([]);
	let userMappings = $state<ForgejoUserMapping[]>([]);
	let mappingLogin = $state('');
	let mappingUserID = $state('');
	let savingMappings = $state(false);
	const repositoryContext = $derived(status?.connected ? JSON.stringify([slug, status.instance_url ?? '']) : null);
	const reposReady = $derived(repoDiscoveryContext !== null && repoDiscoveryContext === repositoryContext);
	const canEditRepos = $derived(canManage && reposReady && !loading && !loadingRepos && !savingRepos && !connecting);

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
		if (!canEditRepos) return;
		const next = new Set(selectedRepoIds);
		if (allFilteredSelected) {
			for (const r of filteredRepos) next.delete(r.forgejo_repo_id);
		} else {
			for (const r of filteredRepos) next.add(r.forgejo_repo_id);
		}
		selectedRepoIds = next;
	}

	onMount(() => {
		mounted = true;
		return () => {
			mounted = false;
			generation++;
		};
	});

	$effect(() => {
		const workspaceSlug = slug;
		if (mounted && workspaceSlug) untrack(() => void load(workspaceSlug));
	});

	function errorMessage(error: unknown, fallback: string): string {
		const value = error as { error?: { message?: unknown }; message?: unknown } | null;
		if (typeof value?.error?.message === 'string' && value.error.message) return value.error.message;
		if (typeof value?.message === 'string' && value.message) return value.message;
		return fallback;
	}

	function isCurrent(workspaceSlug: string, requestGeneration: number): boolean {
		return mounted && slug === workspaceSlug && generation === requestGeneration;
	}

	function resetRepositoryDiscovery() {
		repoDiscoveryGeneration++;
		repoDiscoveryContext = null;
		availableRepos = [];
		selectedRepoIds = new Set();
		repoSearch = '';
		loadingRepos = false;
	}

	async function load(workspaceSlug: string) {
		const requestGeneration = ++generation;
		loading = true;
		loadError = '';
		status = null;
		resetRepositoryDiscovery();
		transitions = [];
		userMappings = [];
		members = [];
		workflowStates = [];
		workflowLoadFailed = false;
		connecting = false;
		savingMappings = false;
		loadingMappings = false;
		savingRepos = false;
		automationsError = '';
		mappingsError = '';
		instanceUrl = DEFAULT_INSTANCE_URL;
		savingEvents = new Set();
		ruleErrors = {};
		reposError = '';
		showRepoSelector = false;
		token = '';
		try {
			const connection = await getForgejoStatus(workspaceSlug);
			if (!isCurrent(workspaceSlug, requestGeneration)) return;
			status = connection;
			if (connection.instance_url) instanceUrl = connection.instance_url;
			if (connection.connected) await loadConfiguration(workspaceSlug, requestGeneration);
		} catch (error) {
			if (isCurrent(workspaceSlug, requestGeneration))
				loadError = errorMessage(error, m['settings.forgejo.failed_status']());
		} finally {
			if (isCurrent(workspaceSlug, requestGeneration)) loading = false;
		}
	}

	async function loadConfiguration(workspaceSlug: string, requestGeneration: number) {
		automationsError = '';
		const [rules] = await Promise.allSettled([
			listAutoTransitions(workspaceSlug),
			loadMappings(workspaceSlug, requestGeneration),
			loadWorkflowStates(workspaceSlug, requestGeneration)
		]);
		if (!isCurrent(workspaceSlug, requestGeneration)) return;
		if (rules.status === 'fulfilled') transitions = rules.value;
		else automationsError = errorMessage(rules.reason, m['settings.forgejo.failed_load_automations']());
	}

	async function loadMappings(workspaceSlug: string, requestGeneration: number) {
		if (!isCurrent(workspaceSlug, requestGeneration) || loadingMappings || savingMappings) return;
		loadingMappings = true;
		const [mappings, workspaceMembers] = await Promise.allSettled([
			listUserMappings(workspaceSlug),
			listMembers(workspaceSlug)
		]);
		if (!isCurrent(workspaceSlug, requestGeneration)) return;
		mappingsError = '';
		if (mappings.status === 'fulfilled') userMappings = mappings.value;
		else mappingsError = errorMessage(mappings.reason, m['settings.forgejo.failed_load_mappings']());
		if (workspaceMembers.status === 'fulfilled') members = workspaceMembers.value;
		else mappingsError ||= errorMessage(workspaceMembers.reason, m['settings.forgejo.failed_load_mappings']());
		loadingMappings = false;
	}

	// A workspace rule uses a slug resolved against each issue's own team.
	async function loadWorkflowStates(workspaceSlug: string, requestGeneration: number) {
		try {
			const teams = await listTeams(workspaceSlug);
			const perTeam = await Promise.all(teams.map((team) => listTeamStatuses(workspaceSlug, team.id)));
			const bySlug = new Map<string, { names: Set<string>; teamIds: Set<string>; position: number }>();
			perTeam.forEach((states, index) =>
				states.forEach((state) => {
					const entry = bySlug.get(state.slug) ?? {
						names: new Set<string>(),
						teamIds: new Set<string>(),
						position: state.position
					};
					entry.names.add(state.name);
					entry.teamIds.add(teams[index].id);
					entry.position = Math.min(entry.position, state.position);
					bySlug.set(state.slug, entry);
				})
			);
			if (!bySlug.has('pr')) {
				bySlug.set('pr', { names: new Set(['PR']), teamIds: new Set(), position: Number.MAX_SAFE_INTEGER });
			}
			if (!isCurrent(workspaceSlug, requestGeneration)) return;
			workflowStates = [...bySlug.entries()]
				.sort((a, b) => a[1].position - b[1].position)
				.map(([value, entry]) => ({
					value,
					label: [...entry.names].sort().join(' / '),
					teams: teams.filter((team) => value === 'pr' || entry.teamIds.has(team.id)).map((team) => team.name),
					missingTeams:
						value === 'pr' ? [] : teams.filter((team) => !entry.teamIds.has(team.id)).map((team) => team.name)
				}));
			workflowLoadFailed = false;
		} catch {
			if (isCurrent(workspaceSlug, requestGeneration)) workflowLoadFailed = true;
		}
	}

	async function handleConnect() {
		if (!canManage || connecting) return;
		const workspaceSlug = slug;
		const requestGeneration = generation;
		connecting = true;
		try {
			await connectForgejo(workspaceSlug, instanceUrl.trim(), token.trim());
			if (!isCurrent(workspaceSlug, requestGeneration)) return;
			resetRepositoryDiscovery();
			showRepoSelector = false;
			token = '';
			const connection = await getForgejoStatus(workspaceSlug);
			if (!isCurrent(workspaceSlug, requestGeneration)) return;
			status = connection;
			await loadConfiguration(workspaceSlug, requestGeneration);
			appToast.success(m['settings.forgejo.connected_toast']());
		} catch (error) {
			if (isCurrent(workspaceSlug, requestGeneration)) appToast.apiError(error, m['settings.forgejo.failed_connect']());
		} finally {
			if (isCurrent(workspaceSlug, requestGeneration)) connecting = false;
		}
	}

	async function handleDisconnect() {
		if (!canManage) return;
		const workspaceSlug = slug;
		const requestGeneration = generation;
		try {
			await disconnectForgejo(workspaceSlug);
			if (!isCurrent(workspaceSlug, requestGeneration)) return;
			resetRepositoryDiscovery();
			showRepoSelector = false;
			const connection = await getForgejoStatus(workspaceSlug);
			if (!isCurrent(workspaceSlug, requestGeneration)) return;
			status = connection;
			transitions = [];
			userMappings = [];
			appToast.success(m['settings.forgejo.disconnected']());
		} catch (error) {
			if (isCurrent(workspaceSlug, requestGeneration))
				appToast.apiError(error, m['settings.forgejo.failed_disconnect']());
		}
	}

	async function loadAvailableRepos() {
		if (!canManage || !status?.connected || loading || loadingRepos || savingRepos || connecting) return;
		const workspaceSlug = slug;
		const requestGeneration = generation;
		const requestContext = repositoryContext;
		resetRepositoryDiscovery();
		const discoveryGeneration = repoDiscoveryGeneration;
		const isCurrentDiscovery = () =>
			isCurrent(workspaceSlug, requestGeneration) &&
			discoveryGeneration === repoDiscoveryGeneration &&
			requestContext === repositoryContext;
		loadingRepos = true;
		showRepoSelector = true;
		reposError = '';
		try {
			const repositories = await listForgejoRepos(workspaceSlug);
			if (!isCurrentDiscovery()) return;
			availableRepos = repositories;
			selectedRepoIds = new Set(repositories.filter((repo) => repo.linked).map((repo) => repo.forgejo_repo_id));
			repoDiscoveryContext = requestContext;
		} catch (error) {
			if (isCurrentDiscovery()) reposError = errorMessage(error, m['settings.forgejo.failed_load_repos']());
		} finally {
			if (isCurrent(workspaceSlug, requestGeneration) && discoveryGeneration === repoDiscoveryGeneration)
				loadingRepos = false;
		}
	}

	function toggleRepo(repoId: number) {
		if (!canEditRepos || !availableRepos.some((repo) => repo.forgejo_repo_id === repoId)) return;
		const next = new Set(selectedRepoIds);
		if (next.has(repoId)) next.delete(repoId);
		else next.add(repoId);
		selectedRepoIds = next;
	}

	async function saveRepoSelection() {
		if (!canEditRepos) return;
		const workspaceSlug = slug;
		const requestGeneration = generation;
		const requestContext = repositoryContext;
		const desiredIds = new Set(selectedRepoIds);
		savingRepos = true;
		reposError = '';
		const failedNames: string[] = [];
		const removals = availableRepos
			.filter((repo) => repo.linked && !desiredIds.has(repo.forgejo_repo_id))
			.flatMap((repo) => {
				const linked = status?.repos.find((item) => item.forgejo_repo_id === repo.forgejo_repo_id);
				return linked ? [{ id: linked.id, name: repo.full_name }] : [];
			});
		const additions = availableRepos.filter((repo) => desiredIds.has(repo.forgejo_repo_id) && !repo.linked);
		if (additions.length) {
			try {
				await linkForgejoRepos(
					workspaceSlug,
					additions.map((repo) => repo.forgejo_repo_id)
				);
			} catch {
				failedNames.push(...additions.map((repo) => repo.full_name));
			}
		}
		for (const repo of removals) {
			if (!isCurrent(workspaceSlug, requestGeneration)) return;
			try {
				await unlinkForgejoRepo(workspaceSlug, repo.id);
			} catch {
				failedNames.push(repo.name);
			}
		}
		if (!isCurrent(workspaceSlug, requestGeneration)) return;
		if (failedNames.length) reposError = `${m['settings.forgejo.failed_save_repos']()} ${failedNames.join(', ')}`;
		try {
			const connection = await getForgejoStatus(workspaceSlug);
			if (!isCurrent(workspaceSlug, requestGeneration)) return;
			status = connection;
			if (requestContext !== repositoryContext) {
				resetRepositoryDiscovery();
				reposError ||= m['settings.forgejo.failed_load_repos']();
				return;
			}
			availableRepos = availableRepos.map((repo) => ({
				...repo,
				linked: connection.repos.some((linked) => linked.forgejo_repo_id === repo.forgejo_repo_id)
			}));
			if (!failedNames.length) {
				showRepoSelector = false;
				repoSearch = '';
				appToast.success(m['settings.forgejo.repos_saved']());
			}
		} catch (error) {
			if (isCurrent(workspaceSlug, requestGeneration)) {
				repoDiscoveryContext = null;
				reposError ||= errorMessage(error, m['settings.forgejo.failed_status']());
			}
		} finally {
			if (isCurrent(workspaceSlug, requestGeneration)) savingRepos = false;
		}
	}

	const TRANSITION_EVENTS: { event: ForgejoAutoTransition['event']; label: () => string; description: () => string }[] =
		$derived([
			{
				event: 'branch_created',
				label: () => m['settings.forgejo.branch_created'](),
				description: () => m['settings.forgejo.branch_created_desc']()
			},
			{
				event: 'pr_opened',
				label: () => m['settings.forgejo.pr_opened'](),
				description: () => m['settings.forgejo.pr_opened_desc']()
			},
			{
				event: 'pr_checks_green',
				label: () => m['settings.forgejo.pr_checks_green'](),
				description: () => m['settings.forgejo.pr_checks_green_desc']()
			},
			{
				event: 'pr_merged',
				label: () => m['settings.forgejo.pr_merged'](),
				description: () => m['settings.forgejo.pr_merged_desc']()
			}
		]);

	// The configured states, plus the rule's current slug when it no longer
	// exists on any team (so a stale selection stays visible and changeable).
	function pickerOptions(currentSlug: string): { value: string; label: string }[] {
		const options: { value: string; label: string }[] = [
			{ value: '', label: m['settings.forgejo.no_transition']() },
			...workflowStates
		];
		if (currentSlug && !workflowStates.some((o) => o.value === currentSlug)) {
			options.push({ value: currentSlug, label: currentSlug });
		}
		return options;
	}

	function ruleFor(event: ForgejoAutoTransition['event']): ForgejoAutoTransition | undefined {
		return transitions.find((t) => t.event === event);
	}

	function canEnable(rule: ForgejoAutoTransition): boolean {
		return !!(rule.target_status || rule.target_status_id || rule.target_assignee_id || rule.assign_forgejo_user);
	}

	async function patchRule(event: ForgejoAutoTransition['event'], changes: Partial<ForgejoAutoTransition>) {
		if (!canManage || savingEvents.has(event)) return;
		const previous = ruleFor(event);
		if (!previous) return;
		const workspaceSlug = slug;
		const requestGeneration = generation;
		const updated = { ...previous, ...changes };
		if (updated.is_active && !canEnable(updated)) {
			ruleErrors = { ...ruleErrors, [event]: m['settings.forgejo.choose_target']() };
			return;
		}
		savingEvents = new Set([...savingEvents, event]);
		ruleErrors = { ...ruleErrors, [event]: '' };
		transitions = transitions.map((rule) => (rule.event === event ? updated : rule));
		try {
			await updateAutoTransitions(workspaceSlug, [updated]);
		} catch (error) {
			if (!isCurrent(workspaceSlug, requestGeneration)) return;
			transitions = transitions.map((rule) => (rule.event === event ? previous : rule));
			ruleErrors = { ...ruleErrors, [event]: errorMessage(error, m['settings.forgejo.failed_update']()) };
		} finally {
			if (isCurrent(workspaceSlug, requestGeneration))
				savingEvents = new Set([...savingEvents].filter((value) => value !== event));
		}
	}

	async function saveMappings(next: ForgejoUserMapping[]): Promise<boolean> {
		if (!canManage || savingMappings || loadingMappings || mappingsError) return false;
		const workspaceSlug = slug;
		const requestGeneration = generation;
		savingMappings = true;
		try {
			await updateUserMappings(workspaceSlug, next);
			const mappings = await listUserMappings(workspaceSlug);
			if (!isCurrent(workspaceSlug, requestGeneration)) return false;
			userMappings = mappings;
			return true;
		} catch (err: any) {
			if (isCurrent(workspaceSlug, requestGeneration)) appToast.apiError(err, m['settings.forgejo.failed_update']());
			return false;
		} finally {
			if (isCurrent(workspaceSlug, requestGeneration)) savingMappings = false;
		}
	}

	async function addMapping() {
		const forgejoLogin = mappingLogin.trim().toLowerCase();
		if (!forgejoLogin || !mappingUserID) return;
		const next = [
			...userMappings.filter((mapping) => mapping.forgejo_login !== forgejoLogin),
			{ forgejo_login: forgejoLogin, user_id: mappingUserID }
		];
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
			<LoaderCircle size={20} class="animate-spin text-[var(--color-text-tertiary)]" />
		</div>
	{:else if loadError}
		<ErrorState message={loadError} onretry={() => load(slug)} />
	{:else if !status?.connected && status?.global_config}
		<ErrorState message={m['settings.forgejo.global_unavailable']()} onretry={() => load(slug)} />
	{:else if !status?.connected}
		{#if !canManage}<p class="text-sm text-[var(--color-text-tertiary)]">{m['settings.forgejo.owner_only']()}</p>{/if}
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
					<Input
						id="forgejo-instance"
						type="url"
						bind:value={instanceUrl}
						placeholder={DEFAULT_INSTANCE_URL}
						disabled={!canManage || connecting}
						class="h-9 text-sm"
					/>
					<p class="mt-1 text-xs text-[var(--color-text-tertiary)]">{m['settings.forgejo.instance_url_desc']()}</p>
				</div>
				<div>
					<label for="forgejo-token" class="mb-1 block text-xs font-medium text-[var(--color-text-secondary)]">
						{m['settings.forgejo.token']()}
					</label>
					<Input
						id="forgejo-token"
						disabled={!canManage || connecting}
						type="password"
						autocomplete="off"
						bind:value={token}
						placeholder={m['settings.forgejo.token_placeholder']()}
						class="h-9 text-sm font-mono"
					/>
					<p class="mt-1 text-xs text-[var(--color-text-tertiary)]">{m['settings.forgejo.token_desc']()}</p>
				</div>
				<div class="flex justify-end">
					<Button
						size="sm"
						onclick={handleConnect}
						disabled={!canManage || connecting || token.trim().length < 20 || !instanceUrl.trim()}
					>
						{#if connecting}
							<LoaderCircle size={14} class="animate-spin" />
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
		{#if !canManage}<p class="text-sm text-[var(--color-text-tertiary)]">{m['settings.forgejo.owner_only']()}</p>{/if}
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
					{#if !status.global_config && canManage}
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
					{#if !showRepoSelector && canManage}
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
								<LoaderCircle size={18} class="animate-spin text-[var(--color-text-tertiary)]" />
							</div>
						{:else if !reposReady}
							<ErrorState
								message={reposError || m['settings.forgejo.failed_load_repos']()}
								onretry={loadAvailableRepos}
							/>
						{:else}
							{#if reposError}<p role="alert" class="mb-3 text-xs text-red-500">{reposError}</p>{/if}
							<div class="relative">
								<Search
									size={14}
									class="absolute left-2.5 top-1/2 -translate-y-1/2 text-[var(--color-text-tertiary)]"
								/>
								<Input
									type="text"
									placeholder={m['settings.forgejo.search_repos']()}
									bind:value={repoSearch}
									class="pl-8 h-8 text-sm"
								/>
							</div>

							<button
								onclick={toggleSelectAll}
								disabled={!canEditRepos}
								class="mt-2 flex w-full items-center gap-2.5 rounded-md px-2 py-1.5 text-sm text-[var(--color-text-secondary)] hover:bg-[var(--color-bg-hover)]"
							>
								<Checkbox
									checked={allFilteredSelected}
									indeterminate={someFilteredSelected}
									class="pointer-events-none"
								/>
								<span class="text-xs"
									>{repoSearch
										? m['settings.forgejo.select_all_filtered']({ n: filteredRepos.length })
										: m['settings.forgejo.select_all']()} ({filteredRepos.length})</span
								>
							</button>

							<div class="mt-1 max-h-64 space-y-0.5 overflow-y-auto">
								{#each filteredRepos as repo}
									<button
										onclick={() => toggleRepo(repo.forgejo_repo_id)}
										disabled={!canEditRepos}
										class="flex w-full items-center gap-2.5 rounded-md px-2 py-1.5 text-sm hover:bg-[var(--color-bg-hover)] {selectedRepoIds.has(
											repo.forgejo_repo_id
										)
											? 'bg-[var(--color-bg-hover)]/50'
											: ''}"
									>
										<Checkbox checked={selectedRepoIds.has(repo.forgejo_repo_id)} class="pointer-events-none" />
										<GitBranch size={14} class="shrink-0 text-[var(--color-text-tertiary)]" />
										<span class="truncate text-[var(--color-text-primary)]">{repo.full_name}</span>
										{#if repo.private}
											<Badge variant="outline" class="ml-auto shrink-0 text-[9px]"
												>{m['settings.forgejo.private']()}</Badge
											>
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
									<Button
										variant="outline"
										size="sm"
										onclick={() => {
											showRepoSelector = false;
											repoSearch = '';
										}}>{m['settings.cancel']()}</Button
									>
									<Button size="sm" onclick={saveRepoSelection} disabled={!canEditRepos}>
										{#if savingRepos}
											<LoaderCircle size={14} class="animate-spin" />
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
							{@const repositoryUrl = forgejoRepositoryHomeUrl(
								repo.full_name,
								repo.instance_url || status.instance_url || ''
							)}
							<div class="flex items-center justify-between rounded-md border border-[var(--app-border)] px-3 py-2">
								<div class="flex items-center gap-2">
									<GitBranch size={14} class="text-[var(--color-text-tertiary)]" />
									<span class="text-sm text-[var(--color-text-primary)]">{repo.full_name}</span>
									<span class="text-xs text-[var(--color-text-tertiary)]">{repo.default_branch}</span>
								</div>
								{#if repositoryUrl}
									<a
										href={repositoryUrl}
										target="_blank"
										rel="noopener noreferrer"
										class="text-[var(--color-text-tertiary)] hover:text-[var(--color-text-secondary)]"
									>
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
				{#if mappingsError}<div class="mt-3">
						<ErrorState message={mappingsError} onretry={() => loadMappings(slug, generation)} />
					</div>{/if}
				<div class="mt-3 space-y-2">
					{#each userMappings as mapping (mapping.forgejo_login)}
						<div class="flex items-center justify-between gap-3 rounded-md border border-[var(--app-border)] px-3 py-2">
							<span class="min-w-0 truncate text-sm text-[var(--color-text-primary)]"
								>{mapping.forgejo_login} → {members.find((member) => member.user_id === mapping.user_id)?.name ??
									m['settings.forgejo.unavailable_assignee']()}</span
							>
							<button
								aria-label={m['settings.forgejo.remove_mapping']({ login: mapping.forgejo_login })}
								disabled={!canManage || savingMappings || loadingMappings || !!mappingsError}
								onclick={() =>
									saveMappings(userMappings.filter((item) => item.forgejo_login !== mapping.forgejo_login))}
								class="shrink-0 text-[var(--color-text-tertiary)] hover:text-[var(--color-text-primary)] disabled:opacity-50"
								><Trash2 size={15} /></button
							>
						</div>
					{/each}
				</div>
				<div class="mt-3 flex flex-wrap items-center gap-2">
					<Input
						aria-label={m['settings.forgejo.forgejo_login']()}
						placeholder={m['settings.forgejo.forgejo_login']()}
						bind:value={mappingLogin}
						disabled={!canManage || savingMappings || loadingMappings || !!mappingsError}
						class="h-8 min-w-32 flex-1 text-sm"
						maxlength={100}
					/>
					<select
						aria-label={m['settings.forgejo.ku_member']()}
						class="h-8 min-w-36 flex-1 rounded-md border border-[var(--app-border)] bg-[var(--color-bg-secondary)] px-2 text-xs text-[var(--color-text-primary)]"
						bind:value={mappingUserID}
						disabled={!canManage || savingMappings || loadingMappings || !!mappingsError}
					>
						<option value="">{m['settings.forgejo.ku_member']()}</option>
						{#each members as member (member.user_id)}
							<option value={member.user_id}>{member.name || member.email}</option>
						{/each}
					</select>
					<Button
						size="sm"
						onclick={addMapping}
						disabled={!canManage ||
							!!mappingsError ||
							loadingMappings ||
							savingMappings ||
							!mappingLogin.trim() ||
							!mappingUserID}
					>
						<Plus size={14} />
						{m['settings.forgejo.add_mapping']()}
					</Button>
				</div>
			</div>

			<!-- Per-event status and assignee rules -->
			<div>
				<h3 class="text-sm font-medium text-[var(--color-text-primary)]">{m['settings.forgejo.automations']()}</h3>
				<p class="mt-1 text-xs text-[var(--color-text-tertiary)]">{m['settings.forgejo.automations_desc']()}</p>
				{#if automationsError}<ErrorState
						message={automationsError}
						onretry={() => loadConfiguration(slug, generation)}
					/>{/if}
				{#if workflowLoadFailed}<p role="alert" class="mt-3 text-xs text-red-500">
						{m['settings.forgejo.failed_workflow_states']()}
					</p>
					<Button variant="ghost" size="sm" onclick={() => loadWorkflowStates(slug, generation)}
						>{m['forgejo_actions.retry']()}</Button
					>{/if}
				<div class="mt-3 space-y-2">
					{#each TRANSITION_EVENTS as meta}
						{@const rule = ruleFor(meta.event)}
						{#if rule}
							{@const coverage = workflowStates.find((state) => state.value === rule.target_status)}
							<div
								data-forgejo-rule={meta.event}
								class="flex flex-wrap items-center justify-between gap-3 rounded-md border border-[var(--app-border)] px-3 py-2.5"
							>
								<div class="min-w-0">
									<span class="text-sm text-[var(--color-text-primary)]">{meta.label()}</span>
									<p class="text-xs text-[var(--color-text-tertiary)]">{meta.description()}</p>
								</div>
								<div class="flex flex-wrap items-center gap-2">
									<select
										aria-label={m['settings.forgejo.target_status']()}
										class="h-8 rounded-md border border-[var(--app-border)] bg-[var(--color-bg-secondary)] px-2 text-xs text-[var(--color-text-primary)]"
										value={rule.target_status}
										disabled={!canManage || savingEvents.has(meta.event) || workflowLoadFailed}
										onchange={(e) => {
											const value = (e.currentTarget as HTMLSelectElement).value;
											patchRule(meta.event, { target_status: value, target_status_id: null });
										}}
									>
										{#each pickerOptions(rule.target_status) as option}
											<option value={option.value} selected={rule.target_status === option.value}>{option.label}</option
											>
										{/each}
									</select>
									<select
										aria-label={m['settings.forgejo.target_assignee']()}
										class="h-8 max-w-40 rounded-md border border-[var(--app-border)] bg-[var(--color-bg-secondary)] px-2 text-xs text-[var(--color-text-primary)]"
										value={rule.assign_forgejo_user ? '__forgejo_user__' : (rule.target_assignee_id ?? '')}
										disabled={!canManage || savingEvents.has(meta.event)}
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
										aria-label={m['settings.forgejo.enable_rule']({ event: meta.label() })}
										disabled={!canManage || savingEvents.has(meta.event) || (!rule.is_active && !canEnable(rule))}
										onCheckedChange={(v) => patchRule(meta.event, { is_active: v })}
									/>
								</div>
								<div class="w-full text-xs text-[var(--color-text-tertiary)]">
									{#if savingEvents.has(meta.event)}<span role="status">{m['settings.forgejo.saving_rule']()}</span
										>{/if}
									{#if ruleErrors[meta.event]}<p role="alert" class="text-red-500">{ruleErrors[meta.event]}</p>{/if}
									{#if !rule.is_active}<p>{m['settings.forgejo.configure_inactive']()}</p>{/if}
									{#if rule.target_status && !workflowLoadFailed}
										{#if !coverage}<p class="text-amber-500">{m['settings.forgejo.missing_status']()}</p>
										{:else}<p>{m['settings.forgejo.applies_to']({ teams: coverage.teams.join(', ') })}</p>
											{#if coverage.missingTeams.length}<p class="text-amber-500">
													{m['settings.forgejo.missing_teams']({ teams: coverage.missingTeams.join(', ') })}
												</p>{/if}{/if}
									{/if}
									{#if rule.target_status === 'pr'}<p>{m['settings.forgejo.review_created_auto']()}</p>{/if}
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
