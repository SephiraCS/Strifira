<script lang="ts">
	import { Button } from '$lib/components/ui/button';
	import Avatar from '$lib/components/shared/Avatar.svelte';
	import Upload from '@lucide/svelte/icons/upload';
	import X from '@lucide/svelte/icons/x';

	let {
		name,
		avatarUrl,
		busy = false,
		disabled = false,
		uploadLabel,
		uploadingLabel,
		removeLabel,
		class: className = '',
		onfile,
		onremove
	}: {
		name: string;
		avatarUrl?: string | null;
		busy?: boolean;
		disabled?: boolean;
		uploadLabel: string;
		uploadingLabel: string;
		removeLabel?: string;
		class?: string;
		onfile: (file: File) => void;
		onremove?: () => void;
	} = $props();

	const AVATAR_ACCEPT = 'image/png,image/jpeg,image/gif,image/webp,image/avif,image/bmp,image/svg+xml';

	let input = $state<HTMLInputElement | null>(null);

	function chosen(event: Event) {
		const el = event.currentTarget as HTMLInputElement;
		const file = el.files?.[0];
		el.value = '';
		if (file) onfile(file);
	}
</script>

<!-- Wraps (never overflows) when the row is too narrow: the description sits
     to the left, this cluster right-aligns and drops below it when needed. -->
<div class="flex min-w-0 flex-wrap items-center justify-end gap-2 {className}">
	<Avatar {name} {avatarUrl} size="lg" />
	<input type="file" accept={AVATAR_ACCEPT} class="hidden" bind:this={input} onchange={chosen} />
	<Button variant="outline" size="sm" disabled={busy || disabled} onclick={() => input?.click()}>
		<Upload size={13} />
		{busy ? uploadingLabel : uploadLabel}
	</Button>
	{#if onremove}
		<Button variant="outline" size="sm" disabled={busy || disabled} onclick={onremove}>
			<X size={13} />
			{removeLabel}
		</Button>
	{/if}
</div>
