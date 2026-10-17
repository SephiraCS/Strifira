<script lang="ts">
	import { onMount } from 'svelte';
	import { flip } from 'svelte/animate';
	import { page } from '$app/state';
	import { dndzone, SHADOW_ITEM_MARKER_PROPERTY_NAME, TRIGGERS } from 'svelte-dnd-action';
	import { sharesState } from '$lib/features/shares/shares.state.svelte';
	import { presenterSession } from '$lib/features/shares/presenter.session.svelte';
	import { wallState, type WallLayout } from '$lib/features/shares/wall.state.svelte';
	import { sortTilesByOrder, spotlightOrder } from '$lib/features/shares/wall-layout';
	import { createKeyboardHandler } from '$lib/utils/keyboard';
	import ShareTile from '$lib/features/shares/ShareTile.svelte';
	import { Button } from '$lib/components/ui/button';
	import * as ToggleGroup from '$lib/components/ui/toggle-group';
	import EmptyState from '$lib/components/shared/EmptyState.svelte';
	import MonitorUp from '@lucide/svelte/icons/monitor-up';
	import MonitorPlay from '@lucide/svelte/icons/monitor-play';
	import LoaderCircle from '@lucide/svelte/icons/loader-circle';
	import LayoutGrid from '@lucide/svelte/icons/layout-grid';
	import Focus from '@lucide/svelte/icons/focus';
	import { m } from '$lib/paraglide/messages.js';

	const slug = $derived(page.params.workspaceSlug ?? '');
	const activeShares = $derived(sharesState.active);

	// The wall fills the viewport: the grid keeps the squarest arrangement,
	// the spotlight gives the focused share the main area and squeezes the
	// rest into a rail (a column beside the stage, a strip under it on
	// phones).
	let compact = $state(false);
	onMount(() => {
		const mq = window.matchMedia('(max-width: 767px)');
		const update = () => (compact = mq.matches);
		update();
		mq.addEventListener('change', update);
		return () => mq.removeEventListener('change', update);
	});

	interface WallTile {
		id: string;
		mode: 'present' | 'view';
		presenterName: string;
		presenterUserId?: string;
	}

	function presenterName(share: NonNullable<typeof presenterSession.share>): string {
		return share.started_by_user?.display_name || share.started_by_user?.name || '';
	}

	// The presenter's share is the persistent session store — it survives
	// navigation to issues and back; the wall only renders its view. Every
	// other active share mounts a viewer tile that negotiates its own peer
	// connection.
	const tiles = $derived.by<WallTile[]>(() => {
		const list: WallTile[] = [];
		if (presenterSession.share) {
			list.push({
				id: presenterSession.share.id,
				mode: 'present',
				presenterName: presenterName(presenterSession.share)
			});
		}
		for (const share of activeShares) {
			if (share.id === presenterSession.share?.id) continue;
			list.push({
				id: share.id,
				mode: 'view',
				presenterName: presenterName(share),
				presenterUserId: share.started_by
			});
		}
		return list;
	});

	// Keep the wall's saved order and focus aligned with the live shares:
	// new ones append, ended ones drop.
	$effect(() => {
		wallState.syncShares(tiles.map((tile) => tile.id));
	});

	const spotlight = $derived(wallState.layout === 'spotlight' && tiles.length > 1);
	const orderedTiles = $derived(sortTilesByOrder(tiles, wallState.order));
	// The stage in the spotlight: the focused share, falling back to the
	// first wall tile. Derived from the wall order (stable), never from the
	// display order — the display order rotates with every focus.
	const stageId = $derived(
		spotlight
			? (orderedTiles.find((tile) => tile.id === wallState.focusedId)?.id ?? orderedTiles[0]?.id ?? null)
			: null
	);
	// While a drag is in flight the wall renders the zone's items verbatim
	// — dnd-action's shadow item included — so it owns the drop preview.
	// The saved order only changes on finalize, once the shadow is gone.
	let dragItems = $state<WallTile[] | null>(null);
	const displayTiles = $derived(dragItems ?? (spotlight ? spotlightOrder(orderedTiles, wallState.focusedId) : orderedTiles));
	const tileCount = $derived(tiles.length);

	const gridStyle = $derived.by(() => {
		const count = displayTiles.length;
		if (!spotlight) {
			const cols = compact || count <= 1 ? 1 : count === 2 ? 2 : Math.min(4, Math.ceil(Math.sqrt(count)));
			return `grid-template-columns: repeat(${cols}, minmax(0, 1fr)); grid-template-rows: repeat(${Math.ceil(count / cols)}, minmax(0, 1fr));`;
		}
		if (count <= 1) {
			return 'grid-template-columns: minmax(0, 1fr); grid-template-rows: minmax(0, 1fr);';
		}
		if (compact) {
			return `grid-template-columns: minmax(0, 1fr); grid-template-rows: minmax(0, 1fr) repeat(${count - 1}, 4.5rem);`;
		}
		return `grid-template-columns: minmax(0, 1fr) 11rem; grid-template-rows: repeat(${count}, minmax(0, 1fr));`;
	});

	function tileStyle(index: number): string {
		// Spotlight desktop: the first tile (the stage) spans every row of the
		// main column; the rail tiles fill the second column row by row.
		if (spotlight && !compact && index === 0) {
			return `grid-column: 1; grid-row: 1 / span ${displayTiles.length};`;
		}
		return '';
	}

	type DndEvent = CustomEvent<{ items: WallTile[]; info?: { trigger?: string; id?: string } }>;

	const handleConsider = (event: DndEvent) => {
		// The keyboard drag's terminal dragStopped arrives after finalize;
		// re-arming dragItems there would freeze the wall on a stale
		// snapshot — finalize already wrote the order.
		if (event.detail.info?.trigger === TRIGGERS.DRAG_STOPPED) return;
		dragItems = event.detail.items;
	};

	const handleFinalize = (event: DndEvent) => {
		dragItems = null;
		const items = event.detail.items;
		const ids = items
			.filter((tile) => !(tile as unknown as Record<string, unknown>)[SHADOW_ITEM_MARKER_PROPERTY_NAME])
			.map((tile) => tile.id)
			// A stale shadow id (the placeholder) is not a share.
			.filter((id) => tiles.some((tile) => tile.id === id));
		if (!spotlight) {
			wallState.setOrder(ids);
			return;
		}
		// The stage keeps its slot: a drag orders the minimized rail.
		const rest = ids.filter((id) => id !== stageId);
		wallState.setOrder(stageId ? [stageId, ...rest] : rest);
	};

	const dndOptions = {
		// Kanban-grade drag tracking: cursor-based hit testing and no zone
		// flip/drop animation, so the index follows the pointer at full
		// cadence even over the spanning stage tile. Item moves still
		// animate via animate:flip on each tile.
		useCursorForDetection: true,
		flipDurationMs: 0,
		dropAnimationDisabled: true,
		morphDisabled: true,
		dropTargetStyle: { outline: 'none' },
		dropTargetClasses: []
	};

	// Arrow keys cycle the spotlight, Escape returns to the grid. Only wired
	// while the spotlight is on so the grid never hijacks scrolling keys.
	// Cycling follows the wall order, not the display order — the display
	// order rotates with every focus and would oscillate between two tiles.
	const shortcuts = $derived(
		spotlight
			? [
					{ key: 'ArrowRight', handler: () => wallState.cycleFocus(orderedTiles.map((tile) => tile.id), 1) },
					{ key: 'ArrowDown', handler: () => wallState.cycleFocus(orderedTiles.map((tile) => tile.id), 1) },
					{ key: 'ArrowLeft', handler: () => wallState.cycleFocus(orderedTiles.map((tile) => tile.id), -1) },
					{ key: 'ArrowUp', handler: () => wallState.cycleFocus(orderedTiles.map((tile) => tile.id), -1) },
					{ key: 'Escape', handler: () => wallState.clearFocus() }
				]
			: []
	);
	const keyHandler = $derived(createKeyboardHandler(shortcuts));

	onMount(() => {
		void sharesState.load(slug);
	});
</script>

<svelte:head>
	<title>{m['shares.page_title']()} · {slug}</title>
</svelte:head>

<svelte:window onkeydown={keyHandler} />

<div class="flex h-full flex-col">
	<header class="flex items-center justify-between border-b border-[var(--color-border)] px-6 py-4">
		<div class="flex items-center gap-2">
			<MonitorPlay size={18} class="text-[var(--color-text-secondary)]" />
			<h1 class="text-base font-semibold">{m['shares.title']()}</h1>
		</div>
		<div class="flex items-center gap-2">
			{#if tiles.length > 1}
				<ToggleGroup.Root
					type="single"
					value={wallState.layout}
					onValueChange={(value) => value && wallState.setLayout(value as WallLayout)}
					variant="outline"
					size="sm"
				>
					<ToggleGroup.Item
						value="grid"
						aria-label={m['shares.layout_grid']()}
						aria-pressed={wallState.layout === 'grid'}
					>
						<LayoutGrid size={14} />
					</ToggleGroup.Item>
					<ToggleGroup.Item
						value="spotlight"
						aria-label={m['shares.layout_spotlight']()}
						aria-pressed={wallState.layout === 'spotlight'}
					>
						<Focus size={14} />
					</ToggleGroup.Item>
				</ToggleGroup.Root>
			{/if}
			{#if !presenterSession.share}
				<Button onclick={() => void presenterSession.start(slug)} disabled={presenterSession.starting}>
					{#if presenterSession.starting}
						<LoaderCircle size={14} class="animate-spin" />
					{:else}
						<MonitorUp size={14} />
					{/if}
					{m['shares.start']()}
				</Button>
			{/if}
		</div>
	</header>

	<div class="min-h-0 flex-1 overflow-y-auto px-6 py-4">
		{#if tiles.length === 0}
			<EmptyState title={m['shares.empty_active_title']()} description={m['shares.empty_active_description']()} />
		{:else}
			<div
				class="grid h-full min-h-0 gap-4"
				style={gridStyle}
				data-tile-count={tileCount}
				data-layout={wallState.layout}
				use:dndzone={{ items: displayTiles, type: 'share-tile', ...dndOptions }}
				onconsider={handleConsider}
				onfinalize={handleFinalize}
			>
				{#each displayTiles as tile, index (tile.id)}
					{@const isShadow = Boolean((tile as unknown as Record<string, unknown>)[SHADOW_ITEM_MARKER_PROPERTY_NAME])}
					<div
						animate:flip={{ duration: 180 }}
						class="min-h-0 min-w-0"
						style={tileStyle(index)}
						data-share-id={isShadow ? undefined : tile.id}
						data-stage={spotlight && index === 0 && !isShadow ? 'true' : undefined}
					>
						{#if isShadow}
							<div
								class="h-full w-full rounded-lg border-2 border-dashed border-[var(--color-border)] bg-[var(--color-bg-secondary)]"
							></div>
						{:else}
							<ShareTile
								workspaceSlug={slug}
								shareId={tile.id}
								mode={tile.mode}
								presenterName={tile.presenterName}
								presenterUserId={tile.presenterUserId}
								variant={spotlight ? (index === 0 ? 'stage' : 'mini') : 'grid'}
								onactivate={spotlight && index === 0 ? undefined : () => wallState.focusShare(tile.id)}
							/>
						{/if}
					</div>
				{/each}
			</div>
		{/if}
	</div>
</div>
