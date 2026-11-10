<script lang="ts">
	import * as Dialog from '$lib/components/ui/dialog';
	import { Button } from '$lib/components/ui/button';
	import { Label } from '$lib/components/ui/label';
	import { Input } from '$lib/components/ui/input';
	import type { TeamStatus } from '$lib/types/team-status';
	import { getCategoryLabel } from '$lib/types/team-status';
	import { updateTeamStatus } from '$lib/api/team-statuses';
	import { COLUMN_ICONS, columnIcon } from './column-icons';
	import IssueStatusIcon from './IssueStatusIcon.svelte';
	import { appToast } from '$lib/features/toast/toast';
	import { m } from '$lib/paraglide/messages.js';
	import Archive from '@lucide/svelte/icons/archive';

	let {
		open = $bindable(false),
		status,
		slug,
		teamId,
		onsaved
	}: {
		open: boolean;
		status: TeamStatus | null;
		slug: string;
		teamId: string;
		onsaved?: (status: TeamStatus) => void;
	} = $props();

	let name = $state('');
	let color = $state('');
	let icon = $state('');
	let wipText = $state('');
	let archived = $state(false);
	let saving = $state(false);

	const iconNames = Object.keys(COLUMN_ICONS);
	const PRESET_COLORS = ['#ef4444', '#f97316', '#eab308', '#22c55e', '#06b6d4', '#3b82f6', '#8b5cf6', '#ec4899'];

	/** Empty removes the limit; otherwise a whole number of 1 or more.
	 * Svelte hands number inputs a real number, so coerce before trimming. */
	const wipLimit = $derived.by(() => {
		const text = String(wipText ?? '').trim();
		if (text === '') return null;
		const value = Number(text);
		if (!Number.isInteger(value) || value < 1) return undefined;
		return value;
	});
	const wipInvalid = $derived(wipLimit === undefined);

	$effect(() => {
		if (open && status) {
			name = status.name;
			color = status.color ?? '';
			icon = status.icon ?? '';
			wipText = status.wip_limit != null ? String(status.wip_limit) : '';
			archived = Boolean(status.archived_at);
		}
	});

	async function handleSave(e: Event) {
		e.preventDefault();
		if (!status || saving || !name.trim() || wipInvalid) return;
		saving = true;
		try {
			// Empty strings clear color/icon and 0 removes the WIP limit (the
			// API's nullable convention); an unset field would keep the old value.
			const updated = await updateTeamStatus(slug, teamId, status.id, {
				name: name.trim(),
				color,
				icon,
				wip_limit: wipLimit ?? 0,
				archived
			});
			appToast.success(m['team_settings.toast.status_updated']());
			onsaved?.(updated);
			open = false;
		} catch (err: any) {
			appToast.apiError(err, m['team_settings.toast.status_update_failed']());
		} finally {
			saving = false;
		}
	}
</script>

<Dialog.Root bind:open>
	<Dialog.Content
		class="sm:max-w-[460px] border-[var(--app-border)] bg-[var(--color-bg-secondary)] p-0 overflow-hidden rounded-xl"
	>
		<form onsubmit={handleSave}>
			<div class="px-5 pt-5 pb-4 space-y-4">
				<h2 class="text-base font-semibold text-[var(--color-text-primary)]">
					{m['team_settings.column_edit.title']()}
				</h2>

				<div class="space-y-1.5">
					<Label class="text-xs text-[var(--color-text-secondary)]" for="column-edit-name">
						{m['team_settings.column_edit.name_label']()}
					</Label>
					<Input
						id="column-edit-name"
						bind:value={name}
						placeholder={m['team_settings.status_name_placeholder']()}
						class="bg-[var(--color-bg)] border-[var(--app-border)] text-[var(--color-text-primary)] text-sm"
					/>
				</div>

				<div class="space-y-1.5">
					<Label class="text-xs text-[var(--color-text-secondary)]">{m['team_settings.column_edit.color_label']()}</Label>
					<div class="flex items-center gap-1.5">
						{#each PRESET_COLORS as c (c)}
							<button
								type="button"
								onclick={() => (color = color === c ? '' : c)}
								class="h-5 w-5 rounded-full transition-transform hover:scale-110 {color === c
									? 'ring-2 ring-[var(--app-accent)] ring-offset-2 ring-offset-[var(--color-bg-secondary)]'
									: ''}"
								style="background-color: {c}"
								aria-label={m['team_settings.select_color_aria']({ color: c })}
							></button>
						{/each}
						<button
							type="button"
							onclick={() => (color = '')}
							class="ml-1 rounded px-1.5 py-0.5 text-xs text-[var(--color-text-tertiary)] hover:bg-[var(--color-bg-tertiary)] hover:text-[var(--color-text-primary)] transition-colors"
						>
							{m['team_settings.column_edit.color_none']()}
						</button>
					</div>
				</div>

				<div class="space-y-1.5">
					<Label class="text-xs text-[var(--color-text-secondary)]">{m['team_settings.column_edit.icon_label']()}</Label>
					<div class="flex flex-wrap gap-1 rounded-md border border-[var(--app-border)] bg-[var(--color-bg)] p-2 max-h-[104px] overflow-y-auto">
						<button
							type="button"
							onclick={() => (icon = '')}
							class="flex h-8 w-8 items-center justify-center rounded transition-colors {icon === ''
								? 'bg-[var(--app-accent)]/15 ring-1 ring-[var(--app-accent)]'
								: 'hover:bg-[var(--color-bg-hover)]'}"
							title={m['team_settings.column_edit.icon_default']()}
							aria-label={m['team_settings.column_edit.icon_default']()}
						>
							<IssueStatusIcon category={status?.category ?? 'backlog'} color={status?.color} size={16} />
						</button>
						{#each iconNames as iconName (iconName)}
							{@const Icon = COLUMN_ICONS[iconName]}
							<button
								type="button"
								onclick={() => (icon = iconName)}
								class="flex h-8 w-8 items-center justify-center rounded transition-colors {icon === iconName
									? 'bg-[var(--app-accent)]/15 ring-1 ring-[var(--app-accent)]'
									: 'hover:bg-[var(--color-bg-hover)]'}"
								style={color ? `color: ${color}` : ''}
								title={iconName}
								aria-label={iconName}
							>
								<Icon size={16} />
							</button>
						{/each}
					</div>
				</div>

				<div class="space-y-1.5">
					<Label class="text-xs text-[var(--color-text-secondary)]" for="column-edit-wip">
						{m['team_settings.column_edit.wip_label']()}
					</Label>
					<Input
						id="column-edit-wip"
						type="number"
						min={1}
						step={1}
						bind:value={wipText}
						placeholder={m['team_settings.column_edit.wip_placeholder']()}
						class="bg-[var(--color-bg)] border-[var(--app-border)] text-[var(--color-text-primary)] text-sm {wipInvalid
							? 'border-[var(--color-error)]'
							: ''}"
					/>
					<p class="text-xs text-[var(--color-text-tertiary)]">{m['team_settings.column_edit.wip_desc']()}</p>
					{#if wipInvalid}
						<p class="text-xs text-[var(--color-error)]">{m['team_settings.column_edit.wip_invalid']()}</p>
					{/if}
				</div>

				<div class="flex items-center justify-between rounded-md border border-[var(--app-border)] bg-[var(--color-bg)] px-3 py-2.5">
					<div class="flex items-center gap-2 min-w-0">
						<Archive size={14} class="shrink-0 text-[var(--color-text-tertiary)]" />
						<div class="min-w-0">
							<p class="text-xs font-medium text-[var(--color-text-primary)]">
								{m['team_settings.column_edit.archive_label']()}
							</p>
							<p class="text-xs text-[var(--color-text-tertiary)] truncate">
								{m['team_settings.column_edit.archive_desc']()}
							</p>
						</div>
					</div>
					<input type="checkbox" bind:checked={archived} class="mt-0.5 shrink-0 rounded border-[var(--app-border)]" />
				</div>

				<p class="text-xs text-[var(--color-text-tertiary)]">
					{m['team_settings.column_edit.workflow_state_label']()}:
					<span class="text-[var(--color-text-secondary)]">{getCategoryLabel(status?.category ?? 'backlog')}</span>
				</p>
			</div>

			<div class="flex justify-end gap-2 border-t border-[var(--app-border)] px-5 py-3">
				<Button variant="outline" size="sm" type="button" onclick={() => (open = false)}>{m['common.cancel']()}</Button>
				<Button size="sm" type="submit" disabled={saving || !name.trim() || wipInvalid}>
					{m['common.save']()}
				</Button>
			</div>
		</form>
	</Dialog.Content>
</Dialog.Root>
