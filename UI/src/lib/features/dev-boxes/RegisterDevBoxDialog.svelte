<script lang="ts">
	import { untrack } from 'svelte';
	import Copy from '@lucide/svelte/icons/copy';
	import { registerDevBox, rotateDevBoxToken } from '$lib/api/dev-boxes';
	import type { DevBox, DevBoxRegistration } from '$lib/types/dev-box';
	import * as Dialog from '$lib/components/ui/dialog';
	import { Button } from '$lib/components/ui/button';
	import { Input } from '$lib/components/ui/input';
	import { Textarea } from '$lib/components/ui/textarea';
	import { appToast } from '$lib/features/toast/toast';
	import { m } from '$lib/paraglide/messages.js';

	let {
		open = $bindable(false),
		slug,
		box = null,
		onregistered
	}: {
		open: boolean;
		slug: string;
		box?: DevBox | null;
		onregistered?: (box: DevBox) => void;
	} = $props();

	let name = $state('');
	let busy = $state(false);
	let registration = $state<DevBoxRegistration | null>(null);
	let formVersion = 0;

	$effect(() => {
		const currentOpen = open;
		const currentSlug = slug;
		const currentBox = box;
		untrack(() => {
			formVersion++;
			registration = null;
			busy = false;
			name = currentOpen ? (currentBox?.name ?? '') : '';
		});
		return () => {
			formVersion++;
			untrack(() => (registration = null));
		};
	});

	const configuration = $derived(
		JSON.stringify(
			{
				server_url: typeof window !== 'undefined' ? window.location.origin : '',
				token_file: '/home/YOUR_USER/.config/ku/worker.token',
				work_root: '/home/YOUR_USER/ku-worktrees',
				execution_mode: 'trusted-local',
				repositories: {
					'OWNER/REPOSITORY': {
						path: '/home/YOUR_USER/repositories/REPOSITORY',
						url: 'https://github.example/OWNER/REPOSITORY',
						origin_url: 'https://github.example/OWNER/REPOSITORY.git'
					}
				},
				providers: { codex: '/absolute/path/to/codex' }
			},
			null,
			2
		)
	);

	async function submit(event: SubmitEvent) {
		event.preventDefault();
		if (busy || (!box && !name.trim())) return;
		const version = formVersion;
		const currentSlug = slug;
		const boxId = box?.id;
		busy = true;
		try {
			const result = boxId
				? await rotateDevBoxToken(currentSlug, boxId)
				: await registerDevBox(currentSlug, name.trim());
			if (version !== formVersion || !open || slug !== currentSlug) return;
			registration = result;
			onregistered?.(result.box);
		} catch (error) {
			if (version === formVersion) appToast.apiError(error, m['dev_boxes.failed_register']());
		} finally {
			if (version === formVersion) busy = false;
		}
	}

	async function copy(value: string) {
		try {
			await navigator.clipboard.writeText(value);
			appToast.success(m['dev_boxes.copied']());
		} catch {
			appToast.error(m['dev_boxes.copy_failed']());
		}
	}
</script>

<Dialog.Root bind:open>
	<Dialog.Content
		class="max-h-[85vh] overflow-y-auto sm:max-w-2xl"
		showCloseButton={!busy}
		escapeKeydownBehavior={busy ? 'ignore' : 'close'}
		interactOutsideBehavior={busy ? 'ignore' : 'close'}
	>
		<Dialog.Header>
			<Dialog.Title
				>{registration
					? m['dev_boxes.setup_title']()
					: box
						? m['dev_boxes.replace_token']()
						: m['dev_boxes.register']()}</Dialog.Title
			>
			<Dialog.Description
				>{registration
					? m['dev_boxes.setup_description']()
					: box
						? m['dev_boxes.replace_token_description']()
						: m['dev_boxes.register_description']()}</Dialog.Description
			>
		</Dialog.Header>
		{#if registration}
			<div class="space-y-5">
				<div class="space-y-2">
					<label for="dev-box-token" class="text-sm font-medium">{m['dev_boxes.worker_token']()}</label>
					<p class="text-xs text-muted-foreground">{m['dev_boxes.token_once']()}</p>
					<Textarea
						id="dev-box-token"
						readonly
						value={registration.token}
						rows={2}
						spellcheck="false"
						class="break-all font-mono text-xs"
					/>
					<Button variant="outline" size="sm" onclick={() => copy(registration?.token ?? '')}
						><Copy size={14} />{m['dev_boxes.copy_token']()}</Button
					>
				</div>
				<ol class="list-decimal space-y-3 pl-5 text-sm">
					<li>{m['dev_boxes.setup_install']()}</li>
					<li>{m['dev_boxes.setup_token']()} <code class="break-all">/home/YOUR_USER/.config/ku/worker.token</code></li>
					<li>{m['dev_boxes.setup_config']()}</li>
					<li>
						{m['dev_boxes.setup_run']()}
						<code class="break-all">dev-box-worker --config /home/YOUR_USER/.config/ku/worker.json</code>
					</li>
				</ol>
				<div class="space-y-2">
					<label for="dev-box-config" class="text-sm font-medium">{m['dev_boxes.configuration']()}</label>
					<Textarea
						id="dev-box-config"
						readonly
						value={configuration}
						rows={12}
						spellcheck="false"
						class="font-mono text-xs"
					/>
					<Button variant="outline" size="sm" onclick={() => copy(configuration)}
						><Copy size={14} />{m['dev_boxes.copy_configuration']()}</Button
					>
				</div>
				<p class="text-xs text-muted-foreground">{m['dev_boxes.setup_trust']()}</p>
				<div class="flex justify-end"><Button onclick={() => (open = false)}>{m['dev_boxes.done']()}</Button></div>
			</div>
		{:else}
			<form onsubmit={submit} class="space-y-4">
				{#if box}
					<p class="text-sm font-medium">{box.name}</p>
				{:else}
					<label class="block space-y-2"
						><span class="text-sm font-medium">{m['dev_boxes.name']()}</span><Input
							bind:value={name}
							required
							maxlength={100}
							placeholder="ku"
							autofocus
						/></label
					>
				{/if}
				<div class="flex justify-end gap-2">
					<Button type="button" variant="outline" disabled={busy} onclick={() => (open = false)}
						>{m['common.cancel']()}</Button
					><Button type="submit" disabled={busy || (!box && !name.trim())}
						>{busy
							? m['dev_boxes.registering']()
							: box
								? m['dev_boxes.replace_token']()
								: m['dev_boxes.register']()}</Button
					>
				</div>
			</form>
		{/if}
	</Dialog.Content>
</Dialog.Root>
