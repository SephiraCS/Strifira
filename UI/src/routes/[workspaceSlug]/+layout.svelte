<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { authState } from '$lib/features/auth/auth.state.svelte';
	import { getWorkspace } from '$lib/api/workspaces';
	import { listTeams, createTeam, deleteTeam, leaveTeam } from '$lib/api/teams';
	import { listProjects } from '$lib/api/projects';
	import { listLabels } from '$lib/api/labels';
	import { listMembers } from '$lib/api/members';
	import { resetWorkspaceResources, shareWorkspaceResource } from '$lib/features/layout/workspace-resources';
	import { listViews } from '$lib/api/views';
	import { listNotifications } from '$lib/api/notifications';
	import type { Workspace } from '$lib/types/workspace';
	import type { Team } from '$lib/types/team';
	import type { Project } from '$lib/types/project';
	import type { Label } from '$lib/types/label';
	import type { WorkspaceMember } from '$lib/types/workspace';
	import type { View } from '$lib/types/view';
	import type { IssuePriority } from '$lib/types/issue';
	import Sidebar from '$lib/components/layout/Sidebar.svelte';
	import CommandPalette from '$lib/components/layout/CommandPalette.svelte';
	import CreateIssueDialog from '$lib/features/issues/LazyCreateIssueDialog.svelte';
	import CreateTeamDialog from '$lib/features/teams/CreateTeamDialog.svelte';
	import * as Dialog from '$lib/components/ui/dialog';
	import ShortcutHelp from '$lib/components/shared/ShortcutHelp.svelte';
	import ErrorState from '$lib/components/shared/ErrorState.svelte';
	import * as Sheet from '$lib/components/ui/sheet';
	import { Button } from '$lib/components/ui/button';
	import { IsMobile } from '$lib/hooks/is-mobile.svelte';
	import { issuesState } from '$lib/features/issues/issues.state.svelte';
	import { showIssueCreatedToast } from '$lib/features/issues/issue-created-toast';
	import { preferencesState } from '$lib/features/preferences/preferences.state.svelte';
	import { teamStatusesState } from '$lib/features/issues/team-statuses.state.svelte';
	import { sidebarState } from '$lib/features/layout/sidebar.state.svelte';
	import { sharesState } from '$lib/features/shares/shares.state.svelte';
	import { createShortcutEngine, type ShortcutDef } from '$lib/utils/keyboard';
	import Menu from '@lucide/svelte/icons/menu';
	import Search from '@lucide/svelte/icons/search';
	import SquarePen from '@lucide/svelte/icons/square-pen';
	import { appToast } from '$lib/features/toast/toast';
	import { loadLucideLibrary } from '$lib/components/shared/lucide-library.svelte';
	import { m } from '$lib/paraglide/messages.js';
	import { getLocale, setLocale } from '$lib/paraglide/runtime.js';
	import TerminalDock from '$lib/features/dev-machines/TerminalDock.svelte';
	import { setTerminalDock } from '$lib/features/dev-machines/terminal-dock-context.svelte';
	import { presenterSession } from '$lib/features/shares/presenter.session.svelte';

	let { children } = $props();
	let workspace = $state<Workspace | null>(null);
	let teams = $state<Team[]>([]);
	let projects = $state<Project[]>([]);
	let labels = $state<Label[]>([]);
	let members = $state<WorkspaceMember[]>([]);
	let views = $state<View[]>([]);
	let unreadCount = $state(0);
	let showCommandPalette = $state(false);
	let showCreateIssue = $state(false);
	let showCreateTeam = $state(false);
	let showShortcutHelp = $state(false);
	let showMobileSidebar = $state(false);
	let confirmTeam = $state<Team | null>(null);
	let confirmAction = $state<'leave' | 'delete' | null>(null);
	let confirmOpen = $state(false);
	let confirmSubmitting = $state(false);
	let authReady = $state(false);
	let workspaceLoadFailed = $state(false);
	let workspaceLoading = $state(false);
	let workspaceLoadId = 0;
	let workspaceRequestId = 0;
	const ancillaryResources = ['projects', 'labels', 'members', 'views', 'notifications'] as const;
	type AncillaryResource = (typeof ancillaryResources)[number];
	let ancillaryRequestIds: Partial<Record<AncillaryResource, number>> = {};
	let ancillaryErrors = $state<Partial<Record<AncillaryResource, boolean>>>({});
	let ancillaryLoading = $state<Partial<Record<AncillaryResource, boolean>>>({});
	const ancillaryLoadFailed = $derived(Object.values(ancillaryErrors).some(Boolean));
	const retryDisabled = $derived(
		!(workspaceLoadFailed && !workspaceLoading) &&
			!ancillaryResources.some((resource) => ancillaryErrors[resource] && !ancillaryLoading[resource])
	);
	const isMobile = new IsMobile();
	const terminalDock = setTerminalDock();
	const slug = $derived(page.params.workspaceSlug ?? '');
	const isSettings = $derived(page.url.pathname.includes('/settings'));

	$effect(() => {
		terminalDock.setWorkspace(slug);
		presenterSession.setWorkspace(slug);
	});

	async function loadWorkspaceShell(workspaceSlug: string) {
		const loadId = workspaceLoadId;
		const requestId = ++workspaceRequestId;
		const isCurrent = () => loadId === workspaceLoadId && requestId === workspaceRequestId && workspaceSlug === slug;
		workspaceLoading = true;
		try {
			// Team icons need the icon set; fetch it alongside the teams.
			loadLucideLibrary();
			const [ws, t] = await Promise.all([
				getWorkspace(workspaceSlug),
				shareWorkspaceResource(workspaceSlug, 'teams', listTeams(workspaceSlug))
			]);
			if (!isCurrent()) return;
			// Ancillary requests must never hold the shell or mounted view hostage.
			workspace = ws;
			sidebarState.workspaceRole = ws.current_user_role ?? 'guest';
			teams = t;
			sidebarState.teams = t;
			sharesState.setEnabled(ws.screen_sharing_enabled);
			if (ws.screen_sharing_enabled) void sharesState.load(workspaceSlug);
			workspaceLoadFailed = false;
		} catch {
			if (isCurrent()) workspaceLoadFailed = true;
		} finally {
			if (isCurrent()) workspaceLoading = false;
		}
	}

	async function loadAncillary<T>(
		resource: AncillaryResource,
		workspaceSlug: string,
		fetchData: () => Promise<T>,
		apply: (data: T) => void
	) {
		const loadId = workspaceLoadId;
		const requestId = (ancillaryRequestIds[resource] ?? 0) + 1;
		ancillaryRequestIds[resource] = requestId;
		const isCurrent = () =>
			loadId === workspaceLoadId && ancillaryRequestIds[resource] === requestId && workspaceSlug === slug;
		ancillaryLoading = { ...ancillaryLoading, [resource]: true };
		try {
			const data = await fetchData();
			if (!isCurrent()) return;
			apply(data);
			ancillaryErrors = { ...ancillaryErrors, [resource]: false };
		} catch {
			// Keep each collection's previous value; retry only failed requests.
			if (isCurrent()) ancillaryErrors = { ...ancillaryErrors, [resource]: true };
		} finally {
			if (isCurrent()) ancillaryLoading = { ...ancillaryLoading, [resource]: false };
		}
	}

	function loadAncillaryResource(resource: AncillaryResource, workspaceSlug: string) {
		switch (resource) {
			case 'projects':
				void loadAncillary(
					resource,
					workspaceSlug,
					() => shareWorkspaceResource(workspaceSlug, 'projects', listProjects(workspaceSlug)),
					(data) => {
						projects = data;
						sidebarState.projects = data;
					}
				);
				break;
			case 'labels':
				void loadAncillary(
					resource,
					workspaceSlug,
					() => shareWorkspaceResource(workspaceSlug, 'labels', listLabels(workspaceSlug)),
					(data) => (labels = data)
				);
				break;
			case 'members':
				void loadAncillary(
					resource,
					workspaceSlug,
					() => shareWorkspaceResource(workspaceSlug, 'members', listMembers(workspaceSlug)),
					(data) => (members = data)
				);
				break;
			case 'views':
				void loadAncillary(
					resource,
					workspaceSlug,
					() => listViews(workspaceSlug),
					(data) => (views = data)
				);
				break;
			case 'notifications':
				void loadAncillary(resource, workspaceSlug, listNotifications, (data) => (unreadCount = data.unread_count));
				break;
		}
	}

	function loadWorkspaceData(workspaceSlug: string) {
		void loadWorkspaceShell(workspaceSlug);
		for (const resource of ancillaryResources) loadAncillaryResource(resource, workspaceSlug);
	}

	function retryWorkspaceData() {
		if (workspaceLoadFailed && !workspaceLoading) void loadWorkspaceShell(slug);
		for (const resource of ancillaryResources) {
			if (ancillaryErrors[resource] && !ancillaryLoading[resource]) loadAncillaryResource(resource, slug);
		}
	}

	// WebSocket bursts (webhook processing emits issue.updated + app.refresh
	// together; bulk ops emit one event per issue) must collapse into a single
	// trailing refetch — one reload per event made the issue boards flicker.
	let issuesRefreshTimer: ReturnType<typeof setTimeout> | null = null;
	function scheduleIssuesRefresh() {
		if (issuesRefreshTimer) clearTimeout(issuesRefreshTimer);
		issuesRefreshTimer = setTimeout(() => {
			issuesRefreshTimer = null;
			// Background reload: keep the current list on screen (loading stays
			// false — boards render nothing while it is true). refresh() reuses
			// the view's exact params, including board fetch-all paging.
			if (slug && issuesState.issues.length > 0) {
				void issuesState.refresh(false);
			}
		}, 300);
	}

	function handleAppRefresh(e: Event) {
		const detail = (e as CustomEvent<{ slug?: string; resources?: string[] }>).detail;
		if (detail?.slug && detail.slug !== slug) return;
		const resources = detail?.resources;
		if (!authReady || !slug) return;
		if (!resources || resources.length === 0) {
			loadWorkspaceData(slug);
			if (issuesState.issues.length > 0) {
				scheduleIssuesRefresh();
			}
			return;
		}
		if (resources.includes('issues') && issuesState.issues.length > 0) {
			scheduleIssuesRefresh();
		}
		if (resources.includes('workspace') || resources.includes('teams')) void loadWorkspaceShell(slug);
		for (const resource of ancillaryResources) {
			if (resources.includes(resource)) loadAncillaryResource(resource, slug);
		}
	}

	onMount(async () => {
		await authState.init();
		if (!authState.authenticated) {
			goto('/login');
			return;
		}
		void preferencesState.syncRemote();
		authReady = true;
	});

	// Re-fetch all data when workspace slug changes (e.g. workspace switch)
	let loadedSlug = '';
	$effect(() => {
		if (authReady && slug && slug !== loadedSlug) {
			loadedSlug = slug;
			workspaceLoadId++;
			workspace = null;
			workspaceLoadFailed = false;
			ancillaryErrors = {};
			ancillaryLoading = {};
			unreadCount = 0;
			sidebarState.workspaceRole = 'guest';
			teams = [];
			projects = [];
			labels = [];
			members = [];
			views = [];
			sidebarState.teams = [];
			sidebarState.projects = [];
			resetWorkspaceResources(slug);
			void loadWorkspaceData(slug);
		}
	});

	// Full shortcut definitions
	const shortcutDefs = $derived<ShortcutDef[]>([
		// Navigation sequences (G + key)
		{
			keys: ['g', 'i'],
			handler: () => goto(`/${slug}/inbox`),
			label: m['sidebar.go_inbox'](),
			category: m['sidebar.navigation']()
		},
		{
			keys: ['g', 'm'],
			handler: () => goto(`/${slug}/my-issues`),
			label: m['sidebar.go_my_issues'](),
			category: m['sidebar.navigation']()
		},
		{
			keys: ['g', 'a'],
			handler: () => goto(`/${slug}/insights`),
			label: m['sidebar.go_insights'](),
			category: m['sidebar.navigation']()
		},
		{
			keys: ['g', 'p'],
			handler: () => goto(`/${slug}/projects`),
			label: m['sidebar.go_projects'](),
			category: m['sidebar.navigation']()
		},
		{
			keys: ['g', 's'],
			handler: () => goto(`/${slug}/settings`),
			label: m['sidebar.go_settings'](),
			category: m['sidebar.navigation']()
		},
		// Actions
		{
			key: 'c',
			handler: () => {
				if (teams.length === 0) {
					showCreateTeam = true;
				} else {
					// Ensure statuses are loaded for the target team
					const targetTeam = getCreateTeamId();
					if (targetTeam) {
						teamStatusesState.load(slug, targetTeam);
					}
					showCreateIssue = true;
				}
			},
			label: m['sidebar.create_issue'](),
			category: m['sidebar.actions']()
		},
		{
			key: 'k',
			meta: true,
			handler: () => (showCommandPalette = !showCommandPalette),
			label: m['sidebar.command_palette'](),
			category: m['sidebar.actions']()
		},
		{
			key: '/',
			handler: () => (showCommandPalette = true),
			label: m['sidebar.search'](),
			category: m['sidebar.actions']()
		},
		{
			key: '?',
			shift: true,
			handler: () => (showShortcutHelp = !showShortcutHelp),
			label: m['sidebar.keyboard_shortcuts'](),
			category: m['sidebar.help']()
		}
	]);

	const shortcutEngine = createShortcutEngine(() => shortcutDefs);

	onMount(() => {
		document.addEventListener('keydown', shortcutEngine.handler);
		window.addEventListener('app:refresh', handleAppRefresh);
		return () => {
			document.removeEventListener('keydown', shortcutEngine.handler);
			window.removeEventListener('app:refresh', handleAppRefresh);
		};
	});

	async function handleCreateTeam(data: { name: string; key: string; description?: string }) {
		try {
			const team = await createTeam(slug, data);
			teams = [...teams, team];
			sidebarState.teams = teams;
			appToast.success(m['sidebar.team_created']());
		} catch (err: any) {
			appToast.apiError(err, m['sidebar.failed_create_team']());
		}
	}

	function removeTeamFromState(teamId: string) {
		teams = teams.filter((team) => team.id !== teamId);
		sidebarState.teams = teams;

		const teamPath = `/${slug}/teams/${teamId}`;
		const settingsPath = `/${slug}/settings/teams/${teamId}`;
		if (page.url.pathname.startsWith(teamPath) || page.url.pathname.startsWith(settingsPath)) {
			goto(`/${slug}/my-issues`);
		}
	}

	function openTeamConfirm(team: Team, action: 'leave' | 'delete') {
		confirmTeam = team;
		confirmAction = action;
		confirmOpen = true;
	}

	function handleLeaveTeam(team: Team) {
		openTeamConfirm(team, 'leave');
	}

	function handleDeleteTeam(team: Team) {
		openTeamConfirm(team, 'delete');
	}

	async function confirmTeamAction() {
		if (!confirmTeam || !confirmAction) return;
		confirmSubmitting = true;
		try {
			if (confirmAction === 'leave') {
				const result = await leaveTeam(slug, confirmTeam.id);
				removeTeamFromState(confirmTeam.id);
				appToast.success(result.status === 'deleted' ? m['sidebar.team_deleted']() : m['sidebar.left_team']());
			} else {
				await deleteTeam(slug, confirmTeam.id);
				removeTeamFromState(confirmTeam.id);
				appToast.success(m['sidebar.team_deleted']());
			}
			confirmOpen = false;
			confirmTeam = null;
			confirmAction = null;
		} catch (err: any) {
			appToast.apiError(
				err,
				confirmAction === 'leave' ? m['sidebar.failed_leave_team']() : m['sidebar.failed_delete_team']()
			);
		} finally {
			confirmSubmitting = false;
		}
	}

	function openCreateIssue() {
		if (teams.length === 0) {
			showCreateTeam = true;
		} else {
			const targetTeam = getCreateTeamId();
			if (targetTeam) {
				teamStatusesState.load(slug, targetTeam);
			}
			showCreateIssue = true;
		}
		showMobileSidebar = false;
	}

	function singleFilterValue(value?: string): string | undefined {
		if (!value) return undefined;
		const values = value.split(',').filter(Boolean);
		return values.length === 1 ? values[0] : undefined;
	}

	function getActiveIssueFilters(): Record<string, string> {
		const pathname = page.url.pathname;
		const teamPath = page.params.teamId ? `/${slug}/teams/${page.params.teamId}` : '';
		const projectPath = page.params.projectId ? `/${slug}/projects/${page.params.projectId}` : '';

		if (pathname === `/${slug}/my-issues`) return issuesState.filters;
		if (teamPath && (pathname === teamPath || pathname === `${teamPath}/board`)) return issuesState.filters;
		if (projectPath && pathname === projectPath) return issuesState.filters;
		return {};
	}

	function getCreateTeamId(): string | undefined {
		const activeFilters = getActiveIssueFilters();
		const routeProject = projects.find((project) => project.id === page.params.projectId);
		return page.params.teamId ?? routeProject?.team_id ?? singleFilterValue(activeFilters.team) ?? teams[0]?.id;
	}

	function getCreateStatusId(): string | undefined {
		const value = singleFilterValue(getActiveIssueFilters().status);
		if (!value) return undefined;
		return (
			teamStatusesState.statusById.get(value)?.id ??
			teamStatusesState.statusOrder.find((status) => status.slug === value)?.id
		);
	}

	function getCreatePriority(): IssuePriority | undefined {
		const value = singleFilterValue(getActiveIssueFilters().priority);
		const priority = value === undefined ? NaN : Number(value);
		return [0, 1, 2, 3, 4].includes(priority) ? (priority as IssuePriority) : undefined;
	}

	function getCreateProjectId(): string | null | undefined {
		const routeProjectId = page.params.projectId;
		if (routeProjectId) return routeProjectId;
		const value = singleFilterValue(getActiveIssueFilters().project);
		if (value === 'none') return null;
		return value;
	}

	function getCreateAssigneeIds(): string[] | undefined {
		const value = singleFilterValue(getActiveIssueFilters().assignee);
		if (value === 'none') return [];
		return value ? [value] : undefined;
	}

	function getCreateLabelIds(): string[] | undefined {
		const value = singleFilterValue(getActiveIssueFilters().label);
		return value ? [value] : undefined;
	}

	// WebSocket connection — reconnects when slug changes. Reconnects use
	// exponential backoff with jitter so a down backend (restart, proxy
	// blip) is retried quickly at first and gently later, instead of a
	// fixed 3s drum; the browser coming back online reconnects immediately.
	let ws_conn: WebSocket | null = null;
	let wsSlug = '';
	let wsReconnectTimer: ReturnType<typeof setTimeout> | null = null;
	let wsDestroyed = false;
	let wsBackoffMs = 1000;
	// Whether the socket for wsSlug has opened before: a later open is a
	// reconnect, after which everything that changed while offline is reloaded.
	let wsHasConnected = false;
	const WS_BACKOFF_MAX_MS = 15_000;

	$effect(() => {
		if (slug && slug !== wsSlug) {
			wsSlug = slug;
			wsHasConnected = false;
			wsBackoffMs = 1000;
			clearWebSocketReconnect();
			ws_conn?.close();
			connectWebSocket(slug);
		}
	});

	onDestroy(() => {
		workspaceLoadId++;
		presenterSession.setWorkspace('');
		wsDestroyed = true;
		clearWebSocketReconnect();
		if (issuesRefreshTimer) {
			clearTimeout(issuesRefreshTimer);
			issuesRefreshTimer = null;
		}
		ws_conn?.close();
		window.removeEventListener('ws:send', handleWSSend as EventListener);
		window.removeEventListener('online', handleWSOnline);
	});

	// Allow child components to send WebSocket messages
	function handleWSSend(e: CustomEvent<any>) {
		if (e.detail.workspaceSlug && e.detail.workspaceSlug !== wsSlug) return;
		if (ws_conn?.readyState === WebSocket.OPEN) {
			ws_conn.send(JSON.stringify({ type: e.detail.type, payload: e.detail.payload }));
		}
	}
	window.addEventListener('ws:send', handleWSSend as EventListener);

	// Network came back (sleep/wake, Wi-Fi roam): reconnect now rather than
	// waiting out the backoff.
	function handleWSOnline() {
		if (wsDestroyed || !wsSlug) return;
		if (ws_conn && ws_conn.readyState === WebSocket.OPEN) return;
		clearWebSocketReconnect();
		connectWebSocket(wsSlug);
	}
	window.addEventListener('online', handleWSOnline);

	function scheduleWebSocketReconnect(workspaceSlug: string) {
		const delay = Math.min(wsBackoffMs, WS_BACKOFF_MAX_MS) * (0.75 + Math.random() * 0.5);
		wsBackoffMs = Math.min(wsBackoffMs * 2, WS_BACKOFF_MAX_MS);
		wsReconnectTimer = setTimeout(() => connectWebSocket(workspaceSlug), delay);
	}

	function connectWebSocket(workspaceSlug: string) {
		if (wsDestroyed || wsSlug !== workspaceSlug) return;
		clearWebSocketReconnect();
		const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
		const wsUrl = `${protocol}//${window.location.host}/api/workspaces/${workspaceSlug}/ws`;
		const socket = new WebSocket(wsUrl);
		ws_conn = socket;

		socket.onmessage = (event) => {
			if (socket !== ws_conn) return;
			try {
				const data = JSON.parse(event.data);
				handleWSMessage(data);
			} catch {
				// Ignore malformed messages
			}
		};

		socket.onopen = () => {
			if (socket !== ws_conn) return;
			wsBackoffMs = 1000;
			const resumed = wsHasConnected;
			wsHasConnected = true;
			window.dispatchEvent(new CustomEvent('ws:reconnected', { detail: { workspaceSlug } }));
			// Events sent while the socket was down are lost: reload the
			// workspace collections (and drop the shared requests pages reuse)
			// so nothing stays stale until the next mutation.
			if (resumed) window.dispatchEvent(new CustomEvent('app:refresh', { detail: { slug: workspaceSlug } }));
			// Share lists drift while disconnected (starts/ends/GC); refetch.
			if (sharesState.enabled) void sharesState.load(workspaceSlug);
		};

		socket.onerror = () => {
			socket.close();
		};

		socket.onclose = () => {
			if (socket !== ws_conn) return;
			// Only reconnect if still on the same workspace
			if (!wsDestroyed && wsSlug === workspaceSlug) {
				window.dispatchEvent(new CustomEvent('ws:disconnected', { detail: { workspaceSlug } }));
				scheduleWebSocketReconnect(workspaceSlug);
			}
		};
	}

	function clearWebSocketReconnect() {
		if (wsReconnectTimer) {
			clearTimeout(wsReconnectTimer);
			wsReconnectTimer = null;
		}
	}

	function handleWSMessage(msg: { type: string; payload: any }) {
		switch (msg.type) {
			case 'issue.created':
			case 'issue.updated':
			case 'issue.triaged':
			case 'issues.bulk_updated':
			case 'issues.bulk_deleted': {
				scheduleIssuesRefresh();
				window.dispatchEvent(new CustomEvent('ws:issue-updated', { detail: msg.payload }));
				break;
			}
			case 'issue.deleted': {
				scheduleIssuesRefresh();
				window.dispatchEvent(new CustomEvent('ws:issue-deleted', { detail: msg.payload }));
				break;
			}
			case 'comment.created': {
				window.dispatchEvent(new CustomEvent('ws:comment-created', { detail: msg.payload }));
				break;
			}
			case 'view.created':
			case 'view.updated':
			case 'view.deleted': {
				window.dispatchEvent(new CustomEvent('app:refresh', { detail: { ...msg.payload, resources: ['views'] } }));
				break;
			}
			case 'app.refresh': {
				window.dispatchEvent(new CustomEvent('app:refresh', { detail: msg.payload ?? {} }));
				break;
			}
			case 'forgejo:pr_updated':
			case 'forgejo:branch_created':
			case 'forgejo:commit_pushed': {
				window.dispatchEvent(new CustomEvent(`ws:${msg.type}`, { detail: msg.payload }));
				break;
			}
			case 'notification.created': {
				unreadCount++;
				window.dispatchEvent(new CustomEvent('ws:notification', { detail: msg.payload }));
				break;
			}
			case 'presence.join':
			case 'presence.leave':
			case 'presence.sync':
			case 'cursor.move':
			case 'focus.update':
			case 'focus.leave': {
				window.dispatchEvent(new CustomEvent(`ws:${msg.type}`, { detail: msg.payload }));
				break;
			}
			case 'share.started': {
				// sharesState itself listens for the dispatched event.
				window.dispatchEvent(new CustomEvent('ws:share.started', { detail: msg.payload }));
				announceShareStarted(msg.payload);
				break;
			}
			case 'share.ended': {
				window.dispatchEvent(new CustomEvent('ws:share.ended', { detail: msg.payload }));
				break;
			}
			// P2P WebRTC share signaling: presence summaries and relayed
			// SDP/ICE messages (consumed by the share tiles).
			case 'share.presence':
			case 'share.signal': {
				window.dispatchEvent(new CustomEvent(`ws:${msg.type}`, { detail: msg.payload }));
				break;
			}
		}
	}

	function announceShareStarted(payload: { id?: string; started_by?: string }) {
		if (!payload?.id || payload.started_by === authState.user?.id) return;
		const member = members.find((mem) => mem.user_id === payload.started_by);
		const who = member?.name ?? '';
		appToast.info(who ? m['shares.toast_started']({ name: who }) : m['shares.toast_started_anon'](), {
			duration: 8000
		});
	}
</script>

{#if workspace}
	<div class="flex h-dvh bg-[var(--color-bg)]">
		{#if !isSettings}
			<div class="hidden md:contents">
				<Sidebar
					{workspace}
					{teams}
					{views}
					{projects}
					{unreadCount}
					{slug}
					oncreateissue={openCreateIssue}
					oncreateteam={() => (showCreateTeam = true)}
					onleaveteam={handleLeaveTeam}
					ondeleteteam={handleDeleteTeam}
					onsearch={() => (showCommandPalette = true)}
					onshortcutshelp={() => (showShortcutHelp = true)}
				/>
			</div>

			<Sheet.Root bind:open={showMobileSidebar}>
				<Sheet.Content side="left" class="w-[min(88vw,320px)] p-0 [&>button]:hidden" showCloseButton={false}>
					<Sheet.Header class="sr-only">
						<Sheet.Title>{m['sidebar.workspace_navigation']()}</Sheet.Title>
						<Sheet.Description>{m['sidebar.navigate_sections']()}</Sheet.Description>
					</Sheet.Header>
					<Sidebar
						{workspace}
						{teams}
						{views}
						{projects}
						{unreadCount}
						{slug}
						mobile
						oncreateissue={openCreateIssue}
						oncreateteam={() => {
							showCreateTeam = true;
							showMobileSidebar = false;
						}}
						onleaveteam={(team) => {
							showMobileSidebar = false;
							handleLeaveTeam(team);
						}}
						ondeleteteam={(team) => {
							showMobileSidebar = false;
							handleDeleteTeam(team);
						}}
						onsearch={() => {
							showCommandPalette = true;
							showMobileSidebar = false;
						}}
						onnavigate={() => (showMobileSidebar = false)}
						onshortcutshelp={() => {
							showMobileSidebar = false;
							showShortcutHelp = true;
						}}
					/>
				</Sheet.Content>
			</Sheet.Root>
		{/if}
		<main class="flex min-w-0 flex-1 flex-col overflow-hidden">
			{#if workspaceLoadFailed || ancillaryLoadFailed}
				<div
					role="alert"
					class="flex shrink-0 items-center justify-between gap-3 border-b border-[var(--app-border)] px-4 py-2 text-sm"
				>
					<p class="text-[var(--color-error)]">{m['login.error_generic']()}</p>
					<Button variant="outline" size="sm" disabled={retryDisabled} onclick={retryWorkspaceData}>
						{m['settings.ai.retry_load']()}
					</Button>
				</div>
			{/if}
			{#if !isSettings}
				<div
					class="flex h-12 shrink-0 items-center justify-between border-b border-[var(--app-border)] bg-[var(--color-bg)] px-3 md:hidden"
				>
					<div class="flex min-w-0 items-center gap-2">
						<Button
							variant="ghost"
							size="icon-lg"
							onclick={() => (showMobileSidebar = true)}
							aria-label={m['sidebar.open_navigation']()}
						>
							<Menu size={18} />
						</Button>
						<span class="truncate text-sm font-medium text-[var(--color-text-primary)]">{workspace.name}</span>
					</div>
					<div class="flex shrink-0 items-center gap-1">
						<Button
							variant="ghost"
							size="icon-lg"
							onclick={() => (showCommandPalette = true)}
							aria-label={m['sidebar.search']()}
						>
							<Search size={18} />
						</Button>
						<Button variant="ghost" size="icon-lg" onclick={openCreateIssue} aria-label={m['sidebar.create_issue']()}>
							<SquarePen size={18} />
						</Button>
					</div>
				</div>
			{/if}
			<div class="min-h-0 flex-1 overflow-auto">
				{@render children()}
			</div>
			<TerminalDock />
		</main>
	</div>

	{#if showCommandPalette}
		<CommandPalette {slug} {teams} onclose={() => (showCommandPalette = false)} oncreateissue={openCreateIssue} />
	{/if}

	<CreateIssueDialog
		bind:open={showCreateIssue}
		{slug}
		{teams}
		{projects}
		{labels}
		{members}
		defaultTeamId={getCreateTeamId()}
		defaultStatusId={getCreateStatusId()}
		defaultPriority={getCreatePriority()}
		defaultProjectId={getCreateProjectId()}
		defaultAssigneeIds={getCreateAssigneeIds()}
		defaultLabelIds={getCreateLabelIds()}
		onlabelcreated={(label) => (labels = [label, ...labels.filter((existing) => existing.id !== label.id)])}
		onsubmit={async (req) => {
			try {
				const created = await issuesState.create(slug, req);
				showIssueCreatedToast(slug, created);
			} catch (err: any) {
				appToast.apiError(err, m['sidebar.failed_create_issue']());
			}
		}}
	/>

	<CreateTeamDialog bind:open={showCreateTeam} onsubmit={handleCreateTeam} />

	<Dialog.Root bind:open={confirmOpen}>
		<Dialog.Content class="sm:max-w-[420px] border-[var(--app-border)] bg-[var(--color-bg-secondary)]">
			<Dialog.Header>
				<Dialog.Title>
					{confirmAction === 'delete' ? m['sidebar.delete_team_title']() : m['sidebar.leave_team_title']()}
				</Dialog.Title>
				<Dialog.Description>
					{#if confirmAction === 'delete'}
						{m['sidebar.delete_team_desc']({ name: confirmTeam?.name ?? m['sidebar.this_team']() })}
					{:else}
						{m['sidebar.leave_team_desc']({ name: confirmTeam?.name ?? m['sidebar.this_team']() })}
					{/if}
				</Dialog.Description>
			</Dialog.Header>
			<Dialog.Footer>
				<Button variant="outline" onclick={() => (confirmOpen = false)} disabled={confirmSubmitting}
					>{m['sidebar.cancel']()}</Button
				>
				<Button variant="destructive" onclick={confirmTeamAction} disabled={confirmSubmitting}>
					{confirmSubmitting
						? m['sidebar.working']()
						: confirmAction === 'delete'
							? m['sidebar.delete_team_title']()
							: m['sidebar.leave_team_title']()}
				</Button>
			</Dialog.Footer>
		</Dialog.Content>
	</Dialog.Root>

	<ShortcutHelp bind:open={showShortcutHelp} shortcuts={shortcutDefs} />
{:else if workspaceLoadFailed && !workspaceLoading}
	<main class="flex h-dvh items-center justify-center bg-[var(--color-bg)]">
		<ErrorState message={m['login.error_generic']()} onretry={retryWorkspaceData} />
	</main>
{:else}
	<div class="flex h-screen items-center justify-center"></div>
{/if}
