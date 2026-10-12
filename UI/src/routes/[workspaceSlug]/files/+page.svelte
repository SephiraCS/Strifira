<script lang="ts">
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { Button } from '$lib/components/ui/button';
	import * as DropdownMenu from '$lib/components/ui/dropdown-menu';
	import EmptyState from '$lib/components/shared/EmptyState.svelte';
	import { deleteFile, listFiles, shareFile } from '$lib/api/docs';
	import type { AssetFileEntry } from '$lib/types/docs';
	import { appToast } from '$lib/features/toast/toast';
	import { m } from '$lib/paraglide/messages.js';
	import Upload from '@lucide/svelte/icons/upload';
	import Search from '@lucide/svelte/icons/search';
	import Copy from '@lucide/svelte/icons/copy';
	import Share2 from '@lucide/svelte/icons/share-2';
	import Trash2 from '@lucide/svelte/icons/trash-2';
	import FileText from '@lucide/svelte/icons/file-text';
	import FileImage from '@lucide/svelte/icons/file-image';
import FileArchive from '@lucide/svelte/icons/file-archive';
import FileSpreadsheet from '@lucide/svelte/icons/file-spreadsheet';
	import File from '@lucide/svelte/icons/file';
	import * as AlertDialog from '$lib/components/ui/alert-dialog';

	const slug = $derived(page.params.workspaceSlug ?? '');

	let items = $state<AssetFileEntry[]>([]);
	let total = $state(0);
	let page_ = $state(1);
	let search = $state('');
	let loading = $state(true);
	let uploadInput: HTMLInputElement | undefined = $state();
	let deleting = $state<AssetFileEntry | null>(null);

	const perPage = 30;
	const totalPages = $derived(Math.max(1, Math.ceil(total / perPage)));

	onMount(() => {
		void load();
	});

	async function load() {
		loading = true;
		try {
			const result = await listFiles(slug, {
				search: search.trim() || undefined,
				page: page_,
				per_page: perPage
			});
			items = result.items ?? [];
			total = result.total ?? 0;
		} catch {
			items = [];
			total = 0;
		} finally {
			loading = false;
		}
	}

	function runSearch() {
		page_ = 1;
		void load();
	}

	async function upload(files: FileList | null) {
		if (!files || files.length === 0) return;
		let ok = 0;
		for (const file of Array.from(files)) {
			try {
				const form = new FormData();
				form.append('file', file);
				const { api } = await import('$lib/api/client');
				await api.post('/api/workspaces/' + slug + '/upload', form);
				ok++;
			} catch {
				// Keep uploading the rest.
			}
		}
		if (ok > 0) {
			appToast.success(m['files.uploaded']({ count: String(ok) }));
			page_ = 1;
			await load();
		} else {
			appToast.error(m['files.upload_failed']());
		}
	}

	function copyLink(entry: AssetFileEntry) {
		void navigator.clipboard.writeText(new URL(entry.url, window.location.origin).toString());
		appToast.success(m['files.link_copied']());
	}

	async function share(entry: AssetFileEntry, ttl: '1h' | '24h' | '7d' | '30d' | '365d') {
		try {
			const result = await shareFile(slug, entry.id, ttl);
			void navigator.clipboard.writeText(new URL(result.url, window.location.origin).toString());
			appToast.success(m['files.share_copied']());
		} catch (err) {
			appToast.apiError(err, m['files.share_failed']());
		}
	}

	async function remove() {
		if (!deleting) return;
		try {
			await deleteFile(slug, deleting.id);
			items = items.filter((i) => i.id !== deleting?.id);
			total = Math.max(0, total - 1);
			appToast.success(m['files.deleted']());
		} catch (err) {
			appToast.apiError(err, m['files.delete_failed']());
		} finally {
			deleting = null;
		}
	}

	function formatSize(size: number): string {
		if (size < 1024) return `${size} B`;
		if (size < 1024 * 1024) return `${(size / 1024).toFixed(1)} KB`;
		return `${(size / (1024 * 1024)).toFixed(1)} MB`;
	}

	function fileIcon(type: string) {
		if (type.startsWith('image/')) return FileImage;
		if (type.startsWith('text/')) return FileText;
		if (type.includes('zip') || type.includes('tar') || type.includes('gz') || type.includes('rar') || type.includes('7z')) return FileArchive;
		if (type.includes('sheet') || type === 'text/csv') return FileSpreadsheet;
		return File;
	}
</script>

<div class="mx-auto w-full max-w-5xl px-6 py-8">
	<div class="mb-6 flex items-center gap-2">
		<h1 class="text-xl font-semibold text-[var(--color-text-primary)]">{m['files.title']()}</h1>
		<div class="ml-auto flex items-center gap-2">
			<div class="relative">
				<Search size={14} class="absolute left-2.5 top-1/2 -translate-y-1/2 text-[var(--color-text-tertiary)]" />
				<input
					type="search"
					class="w-56 rounded-md border border-[var(--app-border)] bg-[var(--color-bg-secondary)] py-1.5 pl-8 pr-3 text-sm text-[var(--color-text-primary)] outline-none placeholder:text-[var(--color-text-tertiary)] focus:border-[var(--app-accent)]"
					placeholder={m['files.search']()}
					bind:value={search}
					onkeydown={(e) => e.key === 'Enter' && runSearch()}
				/>
			</div>
			<Button onclick={() => uploadInput?.click()}>
				<Upload size={15} />
				{m['files.upload']()}
			</Button>
		</div>
	</div>

	<input
		bind:this={uploadInput}
		type="file"
		multiple
		class="hidden"
		onchange={(e) => {
			void upload(e.currentTarget.files);
			e.currentTarget.value = '';
		}}
	/>

	{#if loading}
		<p class="py-12 text-center text-sm text-[var(--color-text-tertiary)]">{m['docs.loading']()}</p>
	{:else if items.length === 0}
		<EmptyState
			title={m['files.empty']()}
			description={m['files.empty_description']()}
			action={{ label: m['files.upload'](), onclick: () => uploadInput?.click() }}
		/>
	{:else}
		<ul class="space-y-1">
			{#each items as entry (entry.id)}
				{@const Icon = fileIcon(entry.content_type)}
				<li class="flex items-center gap-3 rounded-md border border-[var(--app-border)] bg-[var(--color-bg-secondary)] px-3 py-2">
					<Icon size={16} class="shrink-0 text-[var(--color-text-tertiary)]" />
					<div class="min-w-0 flex-1">
						<a
							href={entry.url}
							class="block truncate text-sm text-[var(--color-text-primary)] hover:underline"
							target="_blank"
							rel="noreferrer"
						>
							{entry.filename}
						</a>
						<p class="text-xs text-[var(--color-text-tertiary)]">
							{formatSize(entry.size)} · {entry.uploader_name || '—'} · {new Date(entry.created_at).toLocaleDateString()}
						</p>
					</div>
					<div class="flex shrink-0 items-center gap-1">
						<Button variant="ghost" size="icon" title={m['files.copy_link']()} onclick={() => copyLink(entry)}>
							<Copy size={14} />
						</Button>
						<DropdownMenu.Root>
							<DropdownMenu.Trigger title={m['files.share']()}>
								<Share2 size={14} />
							</DropdownMenu.Trigger>
							<DropdownMenu.Content align="end" class="w-40">
								<DropdownMenu.Label class="text-xs text-[var(--color-text-tertiary)]">
									{m['files.share_expires']()}
								</DropdownMenu.Label>
								<DropdownMenu.Item onclick={() => void share(entry, '1h')}>1 {m['files.hour']()}</DropdownMenu.Item>
								<DropdownMenu.Item onclick={() => void share(entry, '24h')}>24 {m['files.hours']()}</DropdownMenu.Item>
								<DropdownMenu.Item onclick={() => void share(entry, '7d')}>7 {m['files.days']()}</DropdownMenu.Item>
								<DropdownMenu.Item onclick={() => void share(entry, '30d')}>30 {m['files.days']()}</DropdownMenu.Item>
								<DropdownMenu.Item onclick={() => void share(entry, '365d')}>1 {m['files.year']()}</DropdownMenu.Item>
							</DropdownMenu.Content>
						</DropdownMenu.Root>
						<Button
							variant="ghost"
							size="icon"
							class="text-[var(--color-text-tertiary)] hover:text-[var(--color-error)]"
							title={m['files.delete']()}
							onclick={() => (deleting = entry)}
						>
							<Trash2 size={14} />
						</Button>
					</div>
				</li>
			{/each}
		</ul>

		{#if totalPages > 1}
			<div class="mt-4 flex items-center justify-center gap-3 text-sm">
				<Button variant="outline" size="sm" disabled={page_ <= 1} onclick={() => { page_--; void load(); }}>
					{m['files.prev']()}
				</Button>
				<span class="text-[var(--color-text-secondary)]">{page_} / {totalPages}</span>
				<Button variant="outline" size="sm" disabled={page_ >= totalPages} onclick={() => { page_++; void load(); }}>
					{m['files.next']()}
				</Button>
			</div>
		{/if}
	{/if}
</div>

<AlertDialog.Root open={deleting !== null} onOpenChange={(o) => !o && (deleting = null)}>
	<AlertDialog.Content>
		<AlertDialog.Header>
			<AlertDialog.Title>{m['files.delete_confirm_title']()}</AlertDialog.Title>
			<AlertDialog.Description>
				{m['files.delete_confirm']({ name: deleting?.filename ?? '' })}
			</AlertDialog.Description>
		</AlertDialog.Header>
		<AlertDialog.Footer>
			<AlertDialog.Cancel variant="outline">{m['docs.cancel']()}</AlertDialog.Cancel>
			<AlertDialog.Action variant="destructive" onclick={remove}>{m['files.delete']()}</AlertDialog.Action>
		</AlertDialog.Footer>
	</AlertDialog.Content>
</AlertDialog.Root>
