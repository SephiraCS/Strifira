<script lang="ts">
	import { untrack } from 'svelte';
	import { page } from '$app/state';
	import ArrowLeft from '@lucide/svelte/icons/arrow-left';
	import Download from '@lucide/svelte/icons/download';
	import RefreshCw from '@lucide/svelte/icons/refresh-cw';
	import { cancelDevBoxJob, getDevBoxJob, listDevBoxJobLogs } from '$lib/api/dev-boxes';
	import type { DevBoxJob, DevBoxJobLog } from '$lib/types/dev-box';
	import { authState } from '$lib/features/auth/auth.state.svelte';
	import { appToast } from '$lib/features/toast/toast';
	import SidebarToggle from '$lib/components/layout/SidebarToggle.svelte';
	import LoadingState from '$lib/components/shared/LoadingState.svelte';
	import ErrorState from '$lib/components/shared/ErrorState.svelte';
	import DevBoxJobStatus from '$lib/features/dev-boxes/DevBoxJobStatus.svelte';
	import { isActiveJob, providerName } from '$lib/features/dev-boxes/dev-boxes';
	import { Button } from '$lib/components/ui/button';
	import { m } from '$lib/paraglide/messages.js';

	const slug = $derived(page.params.workspaceSlug ?? '');
	const jobId = $derived(page.params.jobId ?? '');
	let job = $state<DevBoxJob | null>(null);
	let logs = $state<DevBoxJobLog[]>([]);
	let logsLimited = $state(false);
	let loading = $state(true);
	let failed = $state(false);
	let logsFailed = $state(false);
	let cancelBusy = $state(false);
	let requestVersion = 0;
	let logsAfterId = 0;
	let nextJobRefresh = 0;
	let timer: ReturnType<typeof setTimeout> | undefined;
	const logLimit = 500;
	const retainedCharacters = 250_000;
	const canCancel = $derived(
		!!job && isActiveJob(job.status) && !job.cancel_requested && job.requested_by_user_id === authState.user?.id
	);

	$effect(() => {
		const currentSlug = slug;
		const currentJobId = jobId;
		untrack(() => {
			job = null;
			logs = [];
			logsLimited = false;
			logsAfterId = 0;
			nextJobRefresh = 0;
			loading = true;
			failed = false;
			logsFailed = false;
			cancelBusy = false;
			clearTimeout(timer);
			void poll(currentSlug, currentJobId, ++requestVersion);
		});
		return () => {
			requestVersion++;
			clearTimeout(timer);
		};
	});

	function isCurrent(currentSlug: string, currentJobId: string, version: number) {
		return version === requestVersion && slug === currentSlug && jobId === currentJobId;
	}

	function retainLogs(incoming: DevBoxJobLog[]) {
		const ids = new Set(logs.map((log) => log.id));
		const merged = [...logs, ...incoming.filter((log) => !ids.has(log.id))];
		let characters = merged.reduce((total, log) => total + log.content.length, 0);
		while (merged.length > logLimit || (characters > retainedCharacters && merged.length > 1)) {
			characters -= merged.shift()!.content.length;
			logsLimited = true;
		}
		logs = merged;
		logsAfterId = Math.max(logsAfterId, ...incoming.map((log) => log.id));
	}

	async function poll(currentSlug: string, currentJobId: string, version: number) {
		let hasMore = false;
		try {
			if (!job || Date.now() >= nextJobRefresh) {
				const result = await getDevBoxJob(currentSlug, currentJobId);
				if (!isCurrent(currentSlug, currentJobId, version)) return;
				job = result;
				failed = false;
				nextJobRefresh = Date.now() + 5000;
			}
			for (let count = 0; count < 5; count++) {
				const result = await listDevBoxJobLogs(currentSlug, currentJobId, logsAfterId, logLimit);
				if (!isCurrent(currentSlug, currentJobId, version)) return;
				const incoming = result ?? [];
				retainLogs(incoming);
				logsFailed = false;
				hasMore = incoming.length === logLimit;
				if (!hasMore) break;
			}
		} catch {
			if (!isCurrent(currentSlug, currentJobId, version)) return;
			if (!job) failed = true;
			else logsFailed = true;
		} finally {
			if (isCurrent(currentSlug, currentJobId, version)) {
				loading = false;
				if (hasMore || !job || isActiveJob(job.status) || logsFailed) {
					timer = setTimeout(() => void poll(currentSlug, currentJobId, version), hasMore ? 0 : 2000);
				}
			}
		}
	}

	function refresh() {
		clearTimeout(timer);
		nextJobRefresh = 0;
		void poll(slug, jobId, ++requestVersion);
	}

	async function cancel() {
		if (!canCancel || cancelBusy) return;
		clearTimeout(timer);
		const currentSlug = slug;
		const currentJobId = jobId;
		const version = ++requestVersion;
		cancelBusy = true;
		try {
			const result = await cancelDevBoxJob(currentSlug, currentJobId);
			if (!isCurrent(currentSlug, currentJobId, version)) return;
			job = result;
		} catch (error) {
			if (isCurrent(currentSlug, currentJobId, version)) appToast.apiError(error, m['dev_boxes.failed_cancel']());
		} finally {
			if (isCurrent(currentSlug, currentJobId, version)) {
				cancelBusy = false;
				nextJobRefresh = 0;
				void poll(currentSlug, currentJobId, version);
			}
		}
	}

	function downloadPatch() {
		if (!job?.result?.patch) return;
		const url = URL.createObjectURL(new Blob([job.result.patch], { type: 'text/plain;charset=utf-8' }));
		const link = document.createElement('a');
		link.href = url;
		link.download = `${job.issue_identifier.replace(/[^a-zA-Z0-9_-]/g, '_')}-${job.id}.patch`;
		link.click();
		setTimeout(() => URL.revokeObjectURL(url), 1000);
	}
</script>

<div class="flex h-full flex-col">
	<header class="flex min-h-[49px] items-center justify-between gap-3 border-b px-4 sm:px-6">
		<div class="flex min-w-0 items-center gap-2">
			<SidebarToggle /><a href="/{slug}/dev-boxes" aria-label={m['dev_boxes.back']()} class="rounded p-1 hover:bg-muted"
				><ArrowLeft size={16} /></a
			>
			<h1 class="truncate text-sm font-medium">
				{m['dev_boxes.job_title']()}{job ? ` · ${job.issue_identifier}` : ''}
			</h1>
		</div>
		<Button size="sm" variant="ghost" disabled={cancelBusy} onclick={refresh}
			><RefreshCw size={14} />{m['dev_boxes.refresh']()}</Button
		>
	</header>
	<div class="min-h-0 flex-1 overflow-y-auto p-4 sm:p-6">
		{#if loading}<LoadingState />{:else if failed || !job}<ErrorState
				message={m['dev_boxes.failed_job']()}
				onretry={refresh}
			/>{:else}
			<div class="mx-auto max-w-5xl space-y-6">
				<section class="space-y-4 rounded-xl border p-4">
					<div class="flex flex-wrap items-center justify-between gap-3">
						<a href="/{slug}/issue/{job.issue_identifier}" class="text-sm font-semibold underline underline-offset-4"
							>{job.issue_identifier}</a
						>
						<div class="flex items-center gap-2">
							<DevBoxJobStatus status={job.status} cancelRequested={job.cancel_requested} />{#if canCancel}<Button
									size="sm"
									variant="outline"
									disabled={cancelBusy}
									onclick={cancel}>{m['dev_boxes.cancel_job']()}</Button
								>{/if}
						</div>
					</div>
					<dl class="grid gap-4 text-sm sm:grid-cols-2">
						<div>
							<dt class="text-xs text-muted-foreground">{m['dev_boxes.repository']()}</dt>
							<dd class="mt-1 break-all">{job.repository_full_name}</dd>
						</div>
						<div>
							<dt class="text-xs text-muted-foreground">{m['dev_boxes.provider']()}</dt>
							<dd class="mt-1">{providerName(job.provider)}</dd>
						</div>
						<div>
							<dt class="text-xs text-muted-foreground">{m['dev_boxes.branch']()}</dt>
							<dd class="mt-1 break-all">{job.base_branch} → {job.working_branch}</dd>
						</div>
						<div>
							<dt class="text-xs text-muted-foreground">{m['dev_boxes.runtime_minutes']()}</dt>
							<dd class="mt-1">{Math.round(job.max_runtime_seconds / 60)}</dd>
						</div>
					</dl>
					{#if job.prompt}<details>
							<summary class="cursor-pointer text-sm font-medium">{m['dev_boxes.instructions']()}</summary>
							<p class="mt-2 whitespace-pre-wrap break-words text-xs text-muted-foreground">{job.prompt}</p>
						</details>{/if}
					{#if job.acceptance_criteria?.length}<div>
							<h2 class="text-xs text-muted-foreground">{m['dev_boxes.acceptance_criteria']()}</h2>
							<ul class="mt-2 list-disc space-y-1 pl-5 text-sm">
								{#each job.acceptance_criteria as criterion}<li class="break-words">{criterion}</li>{/each}
							</ul>
						</div>{/if}
					<div>
						<h2 class="text-xs text-muted-foreground">{m['dev_boxes.test_command']()}</h2>
						<pre class="mt-2 overflow-x-auto rounded-lg bg-muted p-3 text-xs">{JSON.stringify(job.test_command)}</pre>
					</div>
				</section>
				{#if job.result}
					<section class="space-y-4 rounded-xl border p-4" aria-label={m['dev_boxes.result']()}>
						<h2 class="text-sm font-semibold">{m['dev_boxes.result']()}</h2>
						<p class="whitespace-pre-wrap break-words text-sm">{job.result.summary}</p>
						<p class="text-sm">
							<span class="text-muted-foreground">{m['dev_boxes.tests']()}: </span>{job.result.test_status === 'passed'
								? m['dev_boxes.tests_passed']()
								: job.result.test_status === 'failed'
									? m['dev_boxes.tests_failed']()
									: m['dev_boxes.tests_not_run']()}{job.result.test_exit_code !== undefined
								? ` (${m['dev_boxes.exit_code']()}: ${job.result.test_exit_code})`
								: ''}
						</p>
						{#if job.result.tests_run?.length}<ul class="list-disc space-y-1 pl-5 font-mono text-xs">
								{#each job.result.tests_run as command}<li class="break-all">{command}</li>{/each}
							</ul>{/if}
						{#if job.result.risk_notes?.length}<div>
								<h3 class="text-sm font-medium">{m['dev_boxes.review_notes']()}</h3>
								<ul class="mt-2 list-disc space-y-1 pl-5 text-sm">
									{#each job.result.risk_notes as note}<li class="break-words">{note}</li>{/each}
								</ul>
							</div>{/if}
						{#if job.result.changed_files?.length}<details>
								<summary class="cursor-pointer text-sm font-medium"
									>{m['dev_boxes.changed_files']()} ({job.result.changed_files.length})</summary
								>
								<ul class="mt-2 space-y-1 font-mono text-xs">
									{#each job.result.changed_files as file}<li class="break-all">{file}</li>{/each}
								</ul>
							</details>{/if}
						{#if job.result.patch}<div class="space-y-2">
								<div class="flex items-center justify-between gap-3">
									<h3 class="text-sm font-medium">{m['dev_boxes.patch']()}</h3>
									<Button size="sm" variant="outline" onclick={downloadPatch}
										><Download size={14} />{m['dev_boxes.download_patch']()}</Button
									>
								</div>
								<p class="text-xs text-muted-foreground">{m['dev_boxes.review_only']()}</p>
								<pre
									class="max-h-[32rem] overflow-auto rounded-lg bg-muted p-3 text-xs"
									data-testid="dev-box-patch">{job.result.patch}</pre>
							</div>{:else}<p class="text-sm text-muted-foreground">{m['dev_boxes.no_patch']()}</p>{/if}
					</section>
				{/if}
				<section class="space-y-3" aria-label={m['dev_boxes.logs']()}>
					<h2 class="text-sm font-semibold">{m['dev_boxes.logs']()}</h2>
					{#if logsFailed}<p role="alert" class="text-xs text-destructive">{m['dev_boxes.failed_logs']()}</p>{/if}
					{#if logsLimited || job.logs_truncated}<p class="text-xs text-muted-foreground">
							{m['dev_boxes.logs_limited']()}
						</p>{/if}
					<pre
						class="max-h-[32rem] overflow-auto rounded-xl border bg-muted/40 p-3 font-mono text-xs"
						data-testid="dev-box-logs">{logs.length
							? logs.map((log) => `[${log.stream}] ${log.content}`).join('')
							: m['dev_boxes.waiting_logs']()}</pre>
				</section>
			</div>
		{/if}
	</div>
</div>
