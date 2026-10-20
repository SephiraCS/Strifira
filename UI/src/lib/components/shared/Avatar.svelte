<script lang="ts">
	let {
		name,
		avatarUrl,
		size = 'sm'
	}: { name: string; avatarUrl?: string | null; size?: '2xs' | 'xs' | 'sm' | 'md' | 'lg' } = $props();

	const sizes = {
		'2xs': 'h-4 w-4 text-[8px]',
		xs: 'h-5 w-5 text-[9px]',
		sm: 'h-6 w-6 text-xs',
		md: 'h-8 w-8 text-sm',
		lg: 'h-10 w-10 text-base'
	};
	const initial = $derived(name.charAt(0).toUpperCase());

	let failed = $state(false);
	$effect(() => {
		avatarUrl;
		failed = false;
	});
</script>

{#if avatarUrl && !failed}
	<img
		src={avatarUrl}
		alt={name}
		class="rounded-full object-cover {sizes[size]}"
		onerror={() => (failed = true)}
	/>
{:else}
	<div
		class="flex items-center justify-center rounded-full bg-[var(--app-accent)] font-medium text-[var(--app-accent-foreground)] {sizes[
			size
		]}"
	>
		{initial}
	</div>
{/if}
