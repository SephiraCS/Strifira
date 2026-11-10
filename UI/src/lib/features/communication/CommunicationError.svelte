<script lang="ts">
	import { m } from '$lib/paraglide/messages.js';
	let { message = m['communication.load_failed'](), onretry }: { message?: string; onretry: () => void } = $props();
</script>

<div role="alert" class="flex flex-col items-center justify-center gap-3 p-6 text-center">
	<p class="text-sm text-[var(--color-error)]">{message}</p>
	<button
		type="button"
		onclick={onretry}
		class="rounded-md border border-[var(--app-border)] px-3 py-1.5 text-sm hover:bg-[var(--color-bg-hover)]"
		>{m['shares.retry']()}</button
	>
</div>
