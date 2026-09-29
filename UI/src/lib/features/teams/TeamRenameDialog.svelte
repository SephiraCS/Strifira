<script lang="ts">
	import * as Dialog from '$lib/components/ui/dialog';
	import { Button } from '$lib/components/ui/button';
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';
	import { updateTeam } from '$lib/api/teams';
	import { appToast } from '$lib/features/toast/toast';
	import { m } from '$lib/paraglide/messages.js';
	import type { Team } from '$lib/types/team';

	let {
		open = $bindable(false),
		slug = '',
		team,
		onupdated
	}: {
		open?: boolean;
		slug?: string;
		team: Team | undefined;
		onupdated?: (team: Team) => void;
	} = $props();

	let name = $state('');
	let key = $state('');
	let saving = $state(false);
	let collision = $state(false);

	$effect(() => {
		if (open && team) {
			name = team.name;
			key = team.key;
			collision = false;
		}
	});

	const keyValid = $derived(/^[A-Z]{1,10}$/.test(key));
	const keyChanged = $derived(!!team && key !== team.key && keyValid);
	const dirty = $derived(!!team && (name.trim() !== team.name || keyChanged));

	function onKeyInput(event: Event) {
		const input = event.currentTarget as HTMLInputElement;
		key = input.value.toUpperCase().replace(/[^A-Z]/g, '').slice(0, 10);
		input.value = key;
	}

	async function save() {
		if (!team || saving || !dirty) return;
		saving = true;
		collision = false;
		try {
			const payload: { name: string; key?: string } = { name: name.trim() };
			if (keyChanged) payload.key = key;
			const updated = await updateTeam(slug, team.id, payload);
			appToast.success(m['teams.rename.success']());
			onupdated?.(updated);
			open = false;
		} catch (err: any) {
			if (err?.error?.code === 'TEAM_KEY_COLLISION') {
				collision = true;
			} else {
				appToast.apiError(err, m['teams.rename.failed']());
			}
		} finally {
			saving = false;
		}
	}
</script>

<Dialog.Root bind:open>
	<Dialog.Content class="sm:max-w-md">
		<Dialog.Header>
			<Dialog.Title>{m['teams.rename.title']()}</Dialog.Title>
			<Dialog.Description>{m['teams.rename.desc']()}</Dialog.Description>
		</Dialog.Header>
		<div class="space-y-4 py-2">
			<div class="space-y-1.5">
				<Label for="team-rename-name">{m['teams.rename.name']()}</Label>
				<Input id="team-rename-name" bind:value={name} maxlength={100} />
			</div>
			<div class="space-y-1.5">
				<Label for="team-rename-key">{m['teams.rename.key']()}</Label>
				<Input
					id="team-rename-key"
					value={key}
					oninput={onKeyInput}
					placeholder="ENG"
					class="font-mono uppercase"
				/>
				<p class="text-xs text-[var(--color-text-tertiary)]">
					{m['teams.rename.key_hint']({ old: team?.key ?? '', new: key || '…' })}
				</p>
				{#if key && !keyValid}
					<p class="text-xs text-[var(--color-text-tertiary)]">{m['teams.rename.key_format']()}</p>
				{/if}
				{#if collision}
					<p class="text-xs text-[var(--color-text-primary)]">{m['teams.rename.collision']()}</p>
				{/if}
			</div>
		</div>
		<Dialog.Footer>
			<Button variant="outline" onclick={() => (open = false)}>{m['common.cancel']()}</Button>
			<Button onclick={save} disabled={!dirty || saving || (key !== '' && !keyValid)}>
				{saving ? m['settings.saving']() : m['teams.rename.save']()}
			</Button>
		</Dialog.Footer>
	</Dialog.Content>
</Dialog.Root>
