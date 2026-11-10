<script lang="ts">
	import Mic from '@lucide/svelte/icons/mic';
	import MicOff from '@lucide/svelte/icons/mic-off';
	import Headphones from '@lucide/svelte/icons/headphones';
	import HeadphoneOff from '@lucide/svelte/icons/headphone-off';
	import PhoneOff from '@lucide/svelte/icons/phone-off';
	import Settings2 from '@lucide/svelte/icons/settings-2';
	import LoaderCircle from '@lucide/svelte/icons/loader-circle';
	import Waveform from '@lucide/svelte/icons/audio-waveform';
	import GripVertical from '@lucide/svelte/icons/grip-vertical';
	import MessageSquare from '@lucide/svelte/icons/message-square';
	import * as Switch from '$lib/components/ui/switch';
	import Avatar from '$lib/components/shared/Avatar.svelte';
	import { m } from '$lib/paraglide/messages.js';
	import { chatState } from '../chat.state.svelte';
	import { voiceState, type VoiceInputMode } from './voice.state.svelte';
	import { movableVoicePanel } from './movable-panel';

	const session = $derived(voiceState.session);
	const channel = $derived(session ? chatState.channelById(session.channelId) : undefined);
	const roomName = $derived(
		session
			? channel
				? channel.type === 'dm'
					? (channel.dm_peer?.display_name || channel.dm_peer?.name || '—')
					: `#${channel.name}`
				: '#…'
			: ''
	);
	const participants = $derived(session ? voiceState.currentRoom : []);
	const speakColor = 'bg-[var(--color-success)]';
	let showSettings = $state(false);
	let capturingKey = $state(false);

	function participantName(participant: { user?: { display_name: string; name: string } | null; user_id: string }): string {
		return participant.user?.display_name || participant.user?.name || chatState.memberName(participant.user_id) || '…';
	}

	async function handleLeave() {
		await voiceState.leave();
	}

	async function handleModeChange(mode: VoiceInputMode) {
		voiceState.updateSettings({ inputMode: mode });
	}

	/** One keypress captures the next key for push-to-talk. */
	function capturePttKey(e: KeyboardEvent) {
		e.preventDefault();
		if (e.code === 'Escape') {
			capturingKey = false;
			return;
		}
		voiceState.updateSettings({ pttKey: e.code });
		capturingKey = false;
	}
</script>

{#if session}
	<div
		use:movableVoicePanel
		data-testid="voice-room-bar"
		class="fixed bottom-4 left-4 z-50 w-72 overflow-y-auto overscroll-contain rounded-xl border border-[var(--app-border)] bg-[var(--color-bg-secondary)] shadow-lg"
	>
		<div class="flex items-center gap-2 border-b border-[var(--app-border)] px-3 py-2">
			<button
				type="button"
				data-voice-drag-handle
				data-testid="voice-move"
				class="flex min-w-0 flex-1 touch-none select-none items-center gap-2 rounded-md py-1 text-left cursor-grab focus-visible:outline-2 focus-visible:outline-[var(--app-accent)]"
				aria-label={`${m['voice.move_panel']()} ${roomName}`}
				aria-describedby="voice-move-instructions"
				title={m['voice.move_hint']()}
			>
				<GripVertical size={15} class="shrink-0 text-[var(--color-text-tertiary)]" />
				{#if session.connecting}
					<LoaderCircle size={15} class="animate-spin text-[var(--color-text-tertiary)]" />
					<span class="min-w-0 flex-1 truncate text-sm font-semibold text-[var(--color-text-secondary)]">
						{m['voice.connecting']()}
					</span>
				{:else if session.error}
					<span class="min-w-0 flex-1 truncate text-sm font-semibold text-[var(--color-error)]">
						{session.error === 'mic' ? m['voice.mic_failed']() : m['voice.join_failed']()}
					</span>
				{:else}
					<Waveform size={15} class="text-[var(--color-success)]" />
					<span class="min-w-0 flex-1 truncate text-sm font-semibold text-[var(--color-text-primary)]">
						{roomName}
					</span>
				{/if}
			</button>
			<span id="voice-move-instructions" class="sr-only">{m['voice.move_hint']()}</span>
			<a
				href={`/${session.slug}/chat/${session.channelId}`}
				data-testid="voice-open-chat"
				class="rounded-md p-1.5 text-[var(--color-text-tertiary)] hover:bg-[var(--color-bg-hover)] hover:text-[var(--color-text-primary)] focus-visible:outline-2 focus-visible:outline-[var(--app-accent)]"
				title={m['voice.open_chat']()}
				aria-label={m['voice.open_chat']()}
			>
				<MessageSquare size={15} />
			</a>
			<button
				type="button"
				class="rounded-md p-1.5 text-[var(--color-text-tertiary)] hover:bg-[var(--color-bg-hover)] hover:text-[var(--color-text-primary)]"
				title={m['voice.settings']()}
				aria-label={m['voice.settings']()}
				onclick={() => (showSettings = !showSettings)}
			>
				<Settings2 size={15} />
			</button>
			<button
				type="button"
				class="rounded-md p-1.5 text-[var(--color-error)] hover:bg-[var(--color-bg-hover)]"
				title={m['voice.leave']()}
				aria-label={m['voice.leave']()}
				data-testid="voice-leave"
				onclick={handleLeave}
			>
				<PhoneOff size={15} />
			</button>
		</div>

		{#if showSettings}
			<div class="space-y-3 border-b border-[var(--app-border)] px-3 py-3 text-xs">
				<div>
					<p class="mb-1.5 font-medium text-[var(--color-text-secondary)]">{m['voice.input_mode']()}</p>
					<div class="flex gap-1">
						<button
							type="button"
							class="flex-1 rounded-md border px-2 py-1 {voiceState.settings.inputMode === 'vad'
								? 'border-[var(--app-accent)] text-[var(--color-text-primary)]'
								: 'border-[var(--app-border)] text-[var(--color-text-tertiary)] hover:text-[var(--color-text-primary)]'}"
							data-testid="voice-mode-vad"
							onclick={() => handleModeChange('vad')}
						>
							{m['voice.mode_vad']()}
						</button>
						<button
							type="button"
							class="flex-1 rounded-md border px-2 py-1 {voiceState.settings.inputMode === 'ptt'
								? 'border-[var(--app-accent)] text-[var(--color-text-primary)]'
								: 'border-[var(--app-border)] text-[var(--color-text-tertiary)] hover:text-[var(--color-text-primary)]'}"
							data-testid="voice-mode-ptt"
							onclick={() => handleModeChange('ptt')}
						>
							{m['voice.mode_ptt']()}
						</button>
					</div>
					{#if voiceState.settings.inputMode === 'ptt'}
						<button
							type="button"
							class="mt-1.5 w-full rounded-md border border-[var(--app-border)] px-2 py-1 text-[var(--color-text-secondary)] hover:text-[var(--color-text-primary)]"
							onclick={() => (capturingKey = true)}
							onkeydown={capturingKey ? capturePttKey : undefined}
						>
							{capturingKey
								? m['voice.ptt_press_key']()
								: m['voice.ptt_key']({ key: voiceState.settings.pttKey })}
						</button>
					{/if}
				</div>
				<label class="flex items-center justify-between gap-2">
					<span class="text-[var(--color-text-secondary)]">{m['voice.noise_suppression']()}</span>
					<Switch.Switch
						checked={voiceState.settings.noiseSuppression}
						onCheckedChange={(checked) => voiceState.updateSettings({ noiseSuppression: checked })}
					/>
				</label>
				<label class="flex items-center justify-between gap-2">
					<span class="text-[var(--color-text-secondary)]">{m['voice.echo_cancellation']()}</span>
					<Switch.Switch
						checked={voiceState.settings.echoCancellation}
						onCheckedChange={(checked) => voiceState.updateSettings({ echoCancellation: checked })}
					/>
				</label>
				<label class="block">
					<span class="text-[var(--color-text-secondary)]">{m['voice.gain']()}</span>
					<input
						type="range"
						class="mt-1 w-full accent-[var(--app-accent)]"
						min="0.5"
						max="2"
						step="0.05"
						value={voiceState.settings.gain}
						oninput={(e) => voiceState.updateSettings({ gain: Number((e.currentTarget as HTMLInputElement).value) })}
					/>
				</label>
				<label class="block">
					<span class="text-[var(--color-text-secondary)]">{m['voice.output_volume']()}</span>
					<input
						type="range"
						class="mt-1 w-full accent-[var(--app-accent)]"
						min="0"
						max="1"
						step="0.05"
						value={voiceState.settings.outputVolume}
						oninput={(e) =>
							voiceState.updateSettings({ outputVolume: Number((e.currentTarget as HTMLInputElement).value) })}
					/>
				</label>
			</div>
		{/if}

		<ul class="max-h-40 space-y-0.5 overflow-y-auto px-2 py-2">
			{#each participants as participant (participant.peer_id)}
				{@const speaking = voiceState.isSpeaking(participant.peer_id)}
				<li
					class="flex items-center gap-2 rounded-md px-1.5 py-1 text-sm {speaking
						? 'bg-[var(--color-success)]/10'
						: ''}"
					data-testid="voice-participant"
				>
					<span class="relative shrink-0">
						<Avatar name={participantName(participant)} avatarUrl={participant.user?.avatar_url} size="xs" />
						{#if speaking}
							<span
								class="absolute -right-0.5 -bottom-0.5 h-2 w-2 rounded-full {speakColor}"
								title={m['voice.speaking']()}
								data-testid="voice-speaking-dot"
							></span>
						{/if}
					</span>
					<span class="min-w-0 flex-1 truncate {speaking ? 'text-[var(--color-text-primary)]' : 'text-[var(--color-text-secondary)]'}">
						{participantName(participant)}
					</span>
					{#if participant.deafened}
						<HeadphoneOff size={13} class="shrink-0 text-[var(--color-error)]" data-testid="voice-participant-muted-icon" />
					{:else if participant.muted}
						<MicOff size={13} class="shrink-0 text-[var(--color-error)]" data-testid="voice-participant-muted-icon" />
					{/if}
				</li>
			{:else}
				{#if !session.connecting && !session.error}
					<li class="px-1.5 py-1 text-xs text-[var(--color-text-tertiary)]">
						{m['voice.empty_room']()}
					</li>
				{/if}
			{/each}
		</ul>

		{#if !session.error}
			<div class="flex items-center justify-center gap-2 border-t border-[var(--app-border)] px-3 py-2">
				<button
					type="button"
					class="flex flex-1 flex-col items-center gap-0.5 rounded-md px-2 py-1 text-[10px] {session.muted
						? 'bg-[var(--color-error)]/10 text-[var(--color-error)]'
						: 'text-[var(--color-text-secondary)] hover:bg-[var(--color-bg-hover)]'}"
					data-testid="voice-mute"
					disabled={session.connecting}
					onclick={() => voiceState.toggleMute()}
				>
					{#if session.muted}
						<MicOff size={16} />
						{m['voice.unmute']()}
					{:else}
						<Mic size={16} />
						{m['voice.mute']()}
					{/if}
				</button>
				<button
					type="button"
					class="flex flex-1 flex-col items-center gap-0.5 rounded-md px-2 py-1 text-[10px] {session.deafened
						? 'bg-[var(--color-error)]/10 text-[var(--color-error)]'
						: 'text-[var(--color-text-secondary)] hover:bg-[var(--color-bg-hover)]'}"
					data-testid="voice-deafen"
					disabled={session.connecting}
					onclick={() => voiceState.toggleDeafen()}
				>
					{#if session.deafened}
						<HeadphoneOff size={16} />
						{m['voice.undeafen']()}
					{:else}
						<Headphones size={16} />
						{m['voice.deafen']()}
					{/if}
				</button>
			</div>
		{/if}
	</div>
{/if}
