<script lang="ts">
	import { untrack } from 'svelte';
	import * as Dialog from '$lib/components/ui/dialog';
	import { Button } from '$lib/components/ui/button';
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';
	import { updateProject } from '$lib/api/projects';
	import { sidebarState } from '$lib/features/layout/sidebar.state.svelte';
	import { appToast } from '$lib/features/toast/toast';
	import { m } from '$lib/paraglide/messages.js';
	import type { Project } from '$lib/types/project';

	let {
		open = $bindable(false),
		slug,
		project,
		onupdated
	}: {
		open?: boolean;
		slug: string;
		project: Project;
		onupdated: (project: Project) => void;
	} = $props();

	let name = $state('');
	let saving = $state(false);
	let error = $state('');
	let attempted = $state(false);
	const trimmedName = $derived(name.trim());
	const valid = $derived(trimmedName.length > 0 && Array.from(trimmedName).length <= 200);
	const validation = $derived(attempted && !valid ? m['projects.rename.invalid_name']() : '');

	$effect(() => {
		if (open) {
			// Background project refreshes must not replace an unfinished draft.
			name = untrack(() => project.name);
			error = '';
			attempted = false;
		}
	});

	async function save(event: SubmitEvent) {
		event.preventDefault();
		if (saving) return;
		attempted = true;
		error = '';
		if (!valid) return;
		if (trimmedName === project.name) {
			open = false;
			return;
		}
		saving = true;
		try {
			const updated = await updateProject(slug, project.id, { name: trimmedName });
			sidebarState.projects = sidebarState.projects.map((item) =>
				item.id === updated.id ? { ...item, name: updated.name } : item
			);
			onupdated(updated);
			appToast.success(m['projects.rename.success']());
			open = false;
		} catch (err: unknown) {
			const code = (err as { error?: { code?: string } })?.error?.code;
			error =
				code === 'FORBIDDEN'
					? m['projects.rename.forbidden']()
					: code === 'VALIDATION_ERROR'
						? m['projects.rename.invalid_name']()
						: m['projects.rename.failed']();
		} finally {
			saving = false;
		}
	}
</script>

<Dialog.Root bind:open>
	<Dialog.Content
		class="sm:max-w-md"
		showCloseButton={!saving}
		onEscapeKeydown={(event) => {
			if (saving) event.preventDefault();
		}}
		onInteractOutside={(event) => {
			if (saving) event.preventDefault();
		}}
	>
		<form onsubmit={save} class="space-y-4" aria-busy={saving}>
			<Dialog.Header>
				<Dialog.Title>{m['projects.rename.title']()}</Dialog.Title>
				<Dialog.Description>{m['projects.rename.description']()}</Dialog.Description>
			</Dialog.Header>
			<div class="space-y-2">
				<Label for="project-rename-name">{m['projects.rename.name']()}</Label>
				<Input
					id="project-rename-name"
					bind:value={name}
					disabled={saving}
					aria-invalid={!!validation}
					aria-describedby="project-rename-hint project-rename-error"
					oninput={() => {
						error = '';
					}}
				/>
				<p id="project-rename-hint" class="text-xs text-[var(--color-text-tertiary)]">{m['projects.rename.hint']()}</p>
				<div id="project-rename-error" role="alert" class="text-sm text-[var(--color-error)]">
					{validation || error}
				</div>
			</div>
			<Dialog.Footer>
				<Button type="button" variant="outline" disabled={saving} onclick={() => (open = false)}
					>{m['common.cancel']()}</Button
				>
				<Button type="submit" disabled={saving}>{saving ? m['settings.saving']() : m['common.save']()}</Button>
			</Dialog.Footer>
		</form>
	</Dialog.Content>
</Dialog.Root>
