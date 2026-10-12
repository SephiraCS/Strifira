<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { documentGraph } from '$lib/api/docs';
	import type { DocGraph } from '$lib/types/docs';
	import { init as echartsInit, type ECharts } from '$lib/charts/echarts';
	import { m } from '$lib/paraglide/messages.js';
	import { appToast } from '$lib/features/toast/toast';

	let {
		slug,
		open = $bindable(false)
	}: { slug: string; open?: boolean } = $props();

	let container: HTMLDivElement | undefined = $state();
	let chart: ECharts | null = null;
	let graph = $state<DocGraph | null>(null);
	let loading = $state(false);

	const titleById = $derived(new Map((graph?.nodes ?? []).map((n) => [n.id, n.title || m['docs.untitled']()])));

	onMount(() => {
		$effect(() => {
			if (open && !graph && !loading) void load();
		});
		$effect(() => {
			if (open && graph && container) {
				requestAnimationFrame(() => render());
			}
		});
		return () => {
			chart?.dispose();
			chart = null;
		};
	});

	async function load() {
		loading = true;
		try {
			graph = await documentGraph(slug);
		} catch (err) {
			appToast.apiError(err, m['docs.graph.failed']());
			open = false;
		} finally {
			loading = false;
		}
	}

	function render() {
		if (!container || !graph) return;
		chart?.dispose();
		chart = echartsInit(container);
		chart.setOption({
			tooltip: {
				trigger: 'item',
				formatter: (params: { dataType?: string; data?: { name?: string; source?: string; target?: string } }) => {
					if (params.dataType === 'edge') {
						const source = titleById.get(params.data?.source ?? '') ?? '?';
						const target = titleById.get(params.data?.target ?? '') ?? '?';
						return `${source} → ${target}`;
					}
					return params.data?.name ?? '';
				}
			},
			series: [
				{
					type: 'graph',
					layout: 'force',
					roam: true,
					draggable: true,
					label: { show: true, color: 'var(--color-text-secondary)', fontSize: 11, position: 'right' },
					edgeSymbol: ['none', 'arrow'],
					edgeSymbolSize: 7,
					force: { repulsion: 220, edgeLength: [60, 140], gravity: 0.08 },
					data: (graph?.nodes ?? []).map((node) => ({
						id: node.id,
						name: node.title || m['docs.untitled'](),
						symbolSize: 26,
						itemStyle: { color: '#6366f1' }
					})),
					links: (graph?.edges ?? []).map((edge) => ({
						source: edge.source,
						target: edge.target,
						lineStyle: { color: '#71717a', width: 1.2, curveness: 0.15 }
					})),
					emphasis: { focus: 'adjacency' }
				}
			]
		});
		chart.on('click', (params: unknown) => {
			const event = params as { dataType?: string; data?: { id?: string } };
			if (event.dataType !== 'node' || !event.data?.id) return;
			open = false;
			goto(`/${slug}/docs/${event.data.id}`);
		});
	}
</script>

{#if open}
	<div class="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4" role="presentation">
		<div class="flex h-[80vh] w-full max-w-4xl flex-col rounded-lg border border-[var(--app-border)] bg-[var(--color-bg-secondary)] shadow-xl">
			<div class="flex items-center justify-between border-b border-[var(--app-border)] px-4 py-3">
				<h2 class="text-sm font-semibold text-[var(--color-text-primary)]">{m['docs.graph.title']()}</h2>
				<button
					type="button"
					class="rounded p-1 text-[var(--color-text-tertiary)] hover:bg-[var(--color-bg-hover)] hover:text-[var(--color-text-primary)]"
					onclick={() => (open = false)}
					aria-label={m['docs.close']()}
				>
					✕
				</button>
			</div>
			<div class="min-h-0 flex-1 p-2">
				{#if loading}
					<div class="flex h-full items-center justify-center text-sm text-[var(--color-text-tertiary)]">
						{m['docs.graph.loading']()}
					</div>
				{:else}
					<div bind:this={container} class="h-full w-full"></div>
				{/if}
			</div>
		</div>
	</div>
{/if}
