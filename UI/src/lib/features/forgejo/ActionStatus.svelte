<script lang="ts">
	import CircleCheck from '@lucide/svelte/icons/circle-check';
	import CircleX from '@lucide/svelte/icons/circle-x';
	import Clock from '@lucide/svelte/icons/clock';
	import LoaderCircle from '@lucide/svelte/icons/loader-circle';
	import CircleMinus from '@lucide/svelte/icons/circle-minus';
	import { m } from '$lib/paraglide/messages.js';
	import { actionStatus } from './actions';

	let {
		status,
		conclusion = '',
		compact = false
	}: { status: string; conclusion?: string; compact?: boolean } = $props();
	const value = $derived(actionStatus(status, conclusion));
	const Icon = $derived(
		value === 'success'
			? CircleCheck
			: value === 'failure'
				? CircleX
				: value === 'running'
					? LoaderCircle
					: value === 'queued' || value === 'waiting'
						? Clock
						: CircleMinus
	);
	const color = $derived(
		value === 'success'
			? 'text-emerald-500'
			: value === 'failure'
				? 'text-red-500'
				: value === 'running'
					? 'text-blue-500'
					: value === 'queued' || value === 'waiting'
						? 'text-amber-500'
						: 'text-[var(--color-text-tertiary)]'
	);
	const label = $derived.by(() => {
		switch (value) {
			case 'queued':
				return m['forgejo_actions.status_queued']();
			case 'waiting':
				return m['forgejo_actions.status_waiting']();
			case 'running':
				return m['forgejo_actions.status_running']();
			case 'success':
				return m['forgejo_actions.status_success']();
			case 'failure':
				return m['forgejo_actions.status_failure']();
			case 'cancelled':
				return m['forgejo_actions.status_cancelled']();
			case 'skipped':
				return m['forgejo_actions.status_skipped']();
			case 'blocked':
				return m['forgejo_actions.status_blocked']();
			default:
				return value || m['forgejo_actions.unknown']();
		}
	});
</script>

<span class="inline-flex shrink-0 items-center gap-1.5 text-xs {color}" title={label}>
	<Icon size={compact ? 14 : 15} class={value === 'running' ? 'animate-spin' : ''} />
	{#if !compact}<span>{label}</span>{:else}<span class="sr-only">{label}</span>{/if}
</span>
