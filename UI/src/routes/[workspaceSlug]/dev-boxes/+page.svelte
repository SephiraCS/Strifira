<script lang="ts">
	import { onMount, untrack } from 'svelte';
	import { page } from '$app/state';
	import Server from '@lucide/svelte/icons/server';
	import Plus from '@lucide/svelte/icons/plus';
	import KeyRound from '@lucide/svelte/icons/key-round';
	import Trash2 from '@lucide/svelte/icons/trash';
	import RefreshCw from '@lucide/svelte/icons/refresh-cw';
	import { listDevBoxes, listDevBoxJobs, removeDevBox, updateDevBox } from '$lib/api/dev-boxes';
	import type { DevBox, DevBoxJob } from '$lib/types/dev-box';
	import { authState } from '$lib/features/auth/auth.state.svelte';
	import { sidebarState } from '$lib/features/layout/sidebar.state.svelte';
	import { ROLE_HIERARCHY } from '$lib/security/roles';
	import { appToast } from '$lib/features/toast/toast';
	import SidebarToggle from '$lib/components/layout/SidebarToggle.svelte';
	import LoadingState from '$lib/components/shared/LoadingState.svelte';
	import ErrorState from '$lib/components/shared/ErrorState.svelte';
	import RegisterDevBoxDialog from '$lib/features/dev-boxes/RegisterDevBoxDialog.svelte';
	import DevBoxJobStatus from '$lib/features/dev-boxes/DevBoxJobStatus.svelte';
	import { providerName } from '$lib/features/dev-boxes/dev-boxes';
	import * as AlertDialog from '$lib/components/ui/alert-dialog';
	import { Button } from '$lib/components/ui/button';
	import { Switch } from '$lib/components/ui/switch';
	import { m } from '$lib/paraglide/messages.js';

	const slug = $derived(page.params.workspaceSlug ?? '');
	const issueId = $derived(page.url.searchParams.get('issue_id') ?? '');
	const canRegister = $derived(
		(ROLE_HIERARCHY[sidebarState.workspaceRole as keyof typeof ROLE_HIERARCHY] ?? 0) >= ROLE_HIERARCHY.member
	);
	let boxes = $state<DevBox[]>([]);
	let jobs = $state<DevBoxJob[]>([]);
	let loading = $state(true);
	let failed = $state(false);
	let jobsFailed = $state(false);
	let registrationOpen = $state(false);
	let registrationBox = $state<DevBox | null>(null);
	let removalBox = $state<DevBox | null>(null);
	let removalOpen = $state(false);
	let busyBoxId = $state('');
	let jobsPage = $state(1);
	let hasMoreJobs = $state(false);
	let requestVersion = 0;
	let timer: ReturnType<typeof setTimeout> | undefined;
	let refreshWhenReady = false;

	function isVisible() {
		return document.visibilityState !== 'hidden';
	}

	onMount(() => {
		function visibilityChanged() {
			clearTimeout(timer);
			if (!isVisible()) return;
			if (busyBoxId) refreshWhenReady = true;
			else void refresh();
		}
		document.addEventListener('visibilitychange', visibilityChanged);
		return () => document.removeEventListener('visibilitychange', visibilityChanged);
	});

	$effect(() => {
		const currentSlug = slug;
		const currentIssueId = issueId;
		untrack(() => {
			jobsPage = 1;
			boxes = [];
			jobs = [];
			loading = true;
			busyBoxId = '';
			refreshWhenReady = false;
			registrationOpen = false;
			removalOpen = false;
			void refresh(currentSlug, currentIssueId);
		});
		return () => {
			requestVersion++;
			clearTimeout(timer);
		};
	});

	async function refresh(currentSlug = slug, currentIssueId = issueId) {
		clearTimeout(timer);
		if (!isVisible() || busyBoxId || slug !== currentSlug || issueId !== currentIssueId) return;
		refreshWhenReady = false;
		const version = ++requestVersion;
		const currentPage = jobsPage;
		const [boxResult, jobResult] = await Promise.allSettled([
			listDevBoxes(currentSlug),
			listDevBoxJobs(currentSlug, { issueId: currentIssueId || undefined, page: currentPage })
		]);
		if (version !== requestVersion || slug !== currentSlug || issueId !== currentIssueId) return;
		failed = boxResult.status === 'rejected';
		jobsFailed = jobResult.status === 'rejected';
		if (boxResult.status === 'fulfilled') boxes = boxResult.value ?? [];
		if (jobResult.status === 'fulfilled') {
			jobs = jobResult.value.data ?? [];
			hasMoreJobs = jobResult.value.has_more;
		}
		loading = false;
		scheduleRefresh(currentSlug, currentIssueId);
	}

	function scheduleRefresh(currentSlug = slug, currentIssueId = issueId) {
		clearTimeout(timer);
		if (!isVisible() || busyBoxId || slug !== currentSlug || issueId !== currentIssueId) return;
		timer = setTimeout(() => void refresh(currentSlug, currentIssueId), refreshWhenReady ? 0 : 5000);
	}

	function showRegistration(box: DevBox | null = null) {
		registrationBox = box;
		registrationOpen = true;
	}

	function registered(box: DevBox) {
		requestVersion++;
		clearTimeout(timer);
		boxes = [box, ...boxes.filter((item) => item.id !== box.id)];
		scheduleRefresh();
	}

	async function setEnabled(box: DevBox, enabled: boolean) {
		if (busyBoxId || box.created_by_user_id !== authState.user?.id) return;
		clearTimeout(timer);
		const version = ++requestVersion;
		const currentSlug = slug;
		busyBoxId = box.id;
		try {
			const updated = await updateDevBox(currentSlug, box.id, { enabled });
			if (version !== requestVersion || slug !== currentSlug) return;
			boxes = boxes.map((item) => (item.id === box.id ? updated : item));
		} catch (error) {
			if (version === requestVersion) appToast.apiError(error, m['dev_boxes.failed_update']());
		} finally {
			if (version === requestVersion) {
				busyBoxId = '';
				scheduleRefresh();
			}
		}
	}

	async function remove() {
		const box = removalBox;
		if (!box || busyBoxId || box.created_by_user_id !== authState.user?.id) return;
		clearTimeout(timer);
		const version = ++requestVersion;
		const currentSlug = slug;
		busyBoxId = box.id;
		try {
			await removeDevBox(currentSlug, box.id);
			if (version !== requestVersion || slug !== currentSlug) return;
			boxes = boxes.filter((item) => item.id !== box.id);
			removalOpen = false;
			removalBox = null;
		} catch (error) {
			if (version === requestVersion) appToast.apiError(error, m['dev_boxes.failed_remove']());
		} finally {
			if (version === requestVersion) {
				busyBoxId = '';
				scheduleRefresh();
			}
		}
	}

	function changeJobsPage(nextPage: number) {
		jobsPage = nextPage;
		void refresh();
	}
</script>

<div class="flex h-full flex-col">
	<header class="flex min-h-[49px] items-center justify-between gap-3 border-b px-4 sm:px-6">
		<div class="flex min-w-0 items-center gap-2">
			<SidebarToggle /><Server size={16} />
			<h1 class="truncate text-sm font-medium">{m['dev_boxes.title']()}</h1>
		</div>
		{#if canRegister}<Button size="sm" disabled={!!busyBoxId} onclick={() => showRegistration()}
				><Plus size={14} />{m['dev_boxes.register']()}</Button
			>{/if}
	</header>
	<div class="min-h-0 flex-1 overflow-y-auto p-4 sm:p-6">
		<div class="mx-auto max-w-5xl space-y-6">
			<p class="text-sm text-muted-foreground">{m['dev_boxes.description']()}</p>
			{#if loading}<LoadingState />{:else if failed}<ErrorState
					message={m['dev_boxes.failed_load']()}
					onretry={() => refresh()}
				/>{:else if boxes.length === 0}<div class="rounded-xl border border-dashed p-8 text-center">
					<p class="font-medium">{m['dev_boxes.empty']()}</p>
					<p class="mt-2 text-sm text-muted-foreground">{m['dev_boxes.empty_description']()}</p>
				</div>{:else}
				<div class="grid gap-3 md:grid-cols-2">
					{#each boxes as box (box.id)}
						<article class="min-w-0 space-y-4 rounded-xl border p-4" aria-label={box.name}>
							<div class="flex items-start justify-between gap-3">
								<div class="min-w-0">
									<h2 class="truncate text-sm font-semibold">{box.name}</h2>
									<p class="mt-1 truncate text-xs text-muted-foreground">
										{box.hostname || m['dev_boxes.waiting_connection']()}{box.os ? ` · ${box.os} ${box.arch}` : ''}
									</p>
								</div>
								<span
									class="shrink-0 rounded-full border px-2 py-0.5 text-xs {box.online && box.enabled
										? 'text-green-600 dark:text-green-400'
										: 'text-muted-foreground'}"
									>{!box.enabled
										? m['dev_boxes.disabled']()
										: box.online
											? m['dev_boxes.online']()
											: m['dev_boxes.offline']()}</span
								>
							</div>
							<p class="text-xs text-muted-foreground">
								{box.providers.length
									? box.providers
											.map((item) => `${providerName(item.id)}${item.version ? ` ${item.version}` : ''}`)
											.join(' · ')
									: m['dev_boxes.no_providers']()}
							</p>
							<p class="text-xs text-muted-foreground">
								{m['dev_boxes.last_seen']()}: {box.last_seen_at
									? new Date(box.last_seen_at).toLocaleString()
									: m['dev_boxes.never_connected']()}
							</p>
							{#if box.created_by_user_id === authState.user?.id}
								<div class="flex flex-wrap items-center justify-between gap-3 border-t pt-3">
									<label class="flex items-center gap-2 text-xs"
										><Switch
											checked={box.enabled}
											disabled={!!busyBoxId}
											aria-label={m['dev_boxes.enable_named']({ name: box.name })}
											onCheckedChange={(enabled) => setEnabled(box, enabled)}
										/>{m['dev_boxes.enabled']()}</label
									>
									<div class="flex gap-1">
										<Button
											size="icon-sm"
											variant="ghost"
											title={m['dev_boxes.replace_token']()}
											aria-label={m['dev_boxes.replace_token']()}
											disabled={!!busyBoxId}
											onclick={() => showRegistration(box)}><KeyRound size={14} /></Button
										><Button
											size="icon-sm"
											variant="ghost"
											aria-label={m['dev_boxes.remove_named']({ name: box.name })}
											disabled={!!busyBoxId}
											onclick={() => {
												removalBox = box;
												removalOpen = true;
											}}><Trash2 size={14} /></Button
										>
									</div>
								</div>
								<p class="text-xs text-muted-foreground">{m['dev_boxes.disable_description']()}</p>
							{/if}
						</article>
					{/each}
				</div>
			{/if}
			<section class="space-y-3" aria-label={m['dev_boxes.jobs']()}>
				<div class="flex items-center justify-between">
					<h2 class="text-sm font-semibold">{issueId ? m['dev_boxes.issue_jobs']() : m['dev_boxes.jobs']()}</h2>
					<Button variant="ghost" size="sm" disabled={!!busyBoxId} onclick={() => refresh()}
						><RefreshCw size={14} />{m['dev_boxes.refresh']()}</Button
					>
				</div>
				{#if jobsFailed}<p class="text-sm text-destructive">
						{m['dev_boxes.failed_jobs']()}
					</p>{:else if jobs.length === 0}<p class="text-sm text-muted-foreground">{m['dev_boxes.no_jobs']()}</p>{:else}
					<div class="divide-y rounded-xl border">
						{#each jobs as job (job.id)}<a
								href="/{slug}/dev-box-jobs/{job.id}"
								class="flex flex-wrap items-center justify-between gap-3 p-3 hover:bg-muted/50"
								><div class="min-w-0">
									<p class="truncate text-sm font-medium">{job.issue_identifier} · {job.repository_full_name}</p>
									<p class="mt-1 text-xs text-muted-foreground">
										{providerName(job.provider)} · {new Date(job.created_at).toLocaleString()}
									</p>
								</div>
								<DevBoxJobStatus status={job.status} cancelRequested={job.cancel_requested} /></a
							>{/each}
					</div>
				{/if}
				{#if jobsPage > 1 || hasMoreJobs}<div class="flex justify-end gap-2">
						<Button
							variant="outline"
							size="sm"
							disabled={jobsPage <= 1 || !!busyBoxId}
							onclick={() => changeJobsPage(jobsPage - 1)}>{m['machines.prev']()}</Button
						><span class="self-center text-xs text-muted-foreground">{jobsPage}</span><Button
							variant="outline"
							size="sm"
							disabled={!hasMoreJobs || !!busyBoxId}
							onclick={() => changeJobsPage(jobsPage + 1)}>{m['machines.next']()}</Button
						>
					</div>{/if}
			</section>
		</div>
	</div>
</div>

<RegisterDevBoxDialog bind:open={registrationOpen} {slug} box={registrationBox} onregistered={registered} />
<AlertDialog.Root bind:open={removalOpen}
	><AlertDialog.Content
		><AlertDialog.Header
			><AlertDialog.Title>{m['dev_boxes.remove_title']()}</AlertDialog.Title><AlertDialog.Description
				>{m['dev_boxes.remove_description']({ name: removalBox?.name ?? '' })}</AlertDialog.Description
			></AlertDialog.Header
		><AlertDialog.Footer
			><AlertDialog.Cancel disabled={!!busyBoxId}>{m['common.cancel']()}</AlertDialog.Cancel><Button
				variant="destructive"
				disabled={!!busyBoxId}
				onclick={remove}>{m['dev_boxes.remove']()}</Button
			></AlertDialog.Footer
		></AlertDialog.Content
	></AlertDialog.Root
>
