<script lang="ts">
	import * as Dialog from '$lib/components/ui/dialog';
	import { Button } from '$lib/components/ui/button';
	import { createSharedLink, deleteSharedLink, listSharedLinks } from '$lib/api/shared-links';
	import { appToast } from '$lib/features/toast/toast';
	import { m } from '$lib/paraglide/messages.js';
	import Copy from '@lucide/svelte/icons/copy';
	import Link from '@lucide/svelte/icons/link';
	import Trash2 from '@lucide/svelte/icons/trash-2';
	import { onMount } from 'svelte';

	let {
		open = $bindable(false),
		slug,
		docId,
		docTitle
	}: { open?: boolean; slug: string; docId: string; docTitle: string } = $props();

	type LinkEntry = { id: string; token: string; url: string; expires_at?: string; is_active: boolean };
	let links = $state<LinkEntry[]>([]);
	let creating = $state(false);

	onMount(() => {
		$effect(() => {
			if (open) void load();
		});
	});

	async function load() {
		try {
			const all = await listSharedLinks(slug);
			links = all.filter((l: { scope: string; scope_id?: string }) => l.scope === 'document' && l.scope_id === docId);
		} catch {
			links = [];
		}
	}

	async function createLink() {
		if (creating) return;
		creating = true;
		try {
			await createSharedLink(slug, { scope: 'document', scope_id: docId });
			await load();
		} catch (err) {
			appToast.apiError(err, m['docs.share.failed']());
		} finally {
			creating = false;
		}
	}

	async function revoke(id: string) {
		try {
			await deleteSharedLink(slug, id);
			await load();
		} catch (err) {
			appToast.apiError(err, m['docs.share.failed_revoke']());
		}
	}

	function copyLink(url: string) {
		void navigator.clipboard.writeText(url);
		appToast.success(m['docs.share.copied']());
	}
</script>

<Dialog.Root bind:open>
	<Dialog.Content class="sm:max-w-[480px] border-[var(--app-border)] bg-[var(--color-bg-secondary)]">
		<Dialog.Header>
			<Dialog.Title>{m['docs.share.title']()}</Dialog.Title>
			<Dialog.Description>{m['docs.share.description']({ title: docTitle || m['docs.untitled']() })}</Dialog.Description>
		</Dialog.Header>

		<div class="space-y-2">
			{#each links as link (link.id)}
				<div class="flex items-center gap-2 rounded-md border border-[var(--app-border)] px-3 py-2">
					<Link size={14} class="shrink-0 text-[var(--color-text-tertiary)]" />
					<span class="min-w-0 flex-1 truncate text-xs text-[var(--color-text-secondary)]">
						{link.url}
					</span>
					<button
						type="button"
						class="rounded p-1 text-[var(--color-text-tertiary)] hover:bg-[var(--color-bg-hover)] hover:text-[var(--color-text-primary)]"
						title={m['docs.share.copy']()}
						onclick={() => copyLink(link.url)}
					>
						<Copy size={13} />
					</button>
					<button
						type="button"
						class="rounded p-1 text-[var(--color-text-tertiary)] hover:bg-[var(--color-bg-hover)] hover:text-[var(--color-error)]"
						title={m['docs.share.revoke']()}
						onclick={() => void revoke(link.id)}
					>
						<Trash2 size={13} />
					</button>
				</div>
			{:else}
				<p class="text-sm text-[var(--color-text-tertiary)]">{m['docs.share.empty']()}</p>
			{/each}
		</div>

		<Dialog.Footer>
			<Button variant="outline" onclick={() => (open = false)}>{m['docs.done']()}</Button>
			<Button onclick={createLink} disabled={creating}>
				{creating ? m['docs.working']() : m['docs.share.create']()}
			</Button>
		</Dialog.Footer>
	</Dialog.Content>
</Dialog.Root>
