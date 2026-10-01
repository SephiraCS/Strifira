<script lang="ts">
	import { Badge } from '$lib/components/ui/badge';
	import type { DevBoxJobStatus } from '$lib/types/dev-box';
	import { jobStatusLabel } from './dev-boxes';
	import { m } from '$lib/paraglide/messages.js';

	let { status, cancelRequested = false }: { status: DevBoxJobStatus; cancelRequested?: boolean } = $props();
</script>

<Badge
	variant={status === 'failed' || status === 'timeout' ? 'destructive' : 'outline'}
	class={status === 'succeeded' ? 'text-green-600 dark:text-green-400' : ''}
	>{cancelRequested && (status === 'queued' || status === 'leased' || status === 'running')
		? m['dev_boxes.stopping']()
		: jobStatusLabel(status)}</Badge
>
