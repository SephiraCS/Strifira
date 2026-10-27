<script lang="ts">
	import * as Popover from '$lib/components/ui/popover';
	import Camera from '@lucide/svelte/icons/camera';
	import Monitor from '@lucide/svelte/icons/monitor';
	import AppWindow from '@lucide/svelte/icons/app-window';
	import PanelTop from '@lucide/svelte/icons/panel-top';
	import SquareDashedMousePointer from '@lucide/svelte/icons/square-dashed-mouse-pointer';
	import { m } from '$lib/paraglide/messages.js';
	import { screenshotCapture, type CaptureMode } from './screenshot.state.svelte';

	let { class: classOverride = '' }: { class?: string } = $props();
	let menuOpen = $state(false);

	const modes: Array<{
		id: CaptureMode;
		icon: typeof Camera;
		title: string;
		description: string;
	}> = [
		{
			id: 'screen',
			icon: Monitor,
			title: m['screenshot.mode.screen'](),
			description: m['screenshot.mode.screen_desc']()
		},
		{
			id: 'window',
			icon: AppWindow,
			title: m['screenshot.mode.window'](),
			description: m['screenshot.mode.window_desc']()
		},
		{
			id: 'page',
			icon: PanelTop,
			title: m['screenshot.mode.page'](),
			description: m['screenshot.mode.page_desc']()
		},
		{
			id: 'area',
			icon: SquareDashedMousePointer,
			title: m['screenshot.mode.area'](),
			description: m['screenshot.mode.area_desc']()
		}
	];

	function pick(mode: CaptureMode) {
		menuOpen = false;
		void screenshotCapture.start(mode);
	}
</script>

<Popover.Root bind:open={menuOpen}>
	<Popover.Trigger disabled={screenshotCapture.busyOrSubmitting}>
		{#snippet child({ props })}
			<button
				{...props}
				class="rounded p-1.5 text-[var(--color-text-tertiary)] transition-colors hover:bg-[var(--color-bg-hover)] hover:text-[var(--color-text-primary)] disabled:opacity-50 {classOverride}"
				title={m['screenshot.button.title']()}
				aria-label={m['screenshot.button.title']()}
			>
				<Camera size={14} />
			</button>
		{/snippet}
	</Popover.Trigger>
	<Popover.Content class="w-72 max-w-[calc(100vw-1rem)] p-1" align="end">
		{#if screenshotCapture.supported}
			{#each modes as mode (mode.id)}
				<button
					type="button"
					onclick={() => pick(mode.id)}
					class="flex w-full items-start gap-2.5 rounded-md px-2 py-1.5 text-left hover:bg-[var(--color-bg-hover)]"
				>
					<mode.icon size={14} class="mt-0.5 shrink-0 text-[var(--color-text-tertiary)]" />
					<span class="min-w-0">
						<span class="block text-sm text-[var(--color-text-primary)]">{mode.title}</span>
						<span class="block text-xs text-[var(--color-text-tertiary)]">{mode.description}</span>
					</span>
				</button>
			{/each}
		{:else}
			<p class="px-2 py-1.5 text-xs text-[var(--color-text-tertiary)]">
				{m['screenshot.menu.unsupported']()}
			</p>
		{/if}
	</Popover.Content>
</Popover.Root>
