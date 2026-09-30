import type { DevBoxJobStatus, DevBoxProviderId } from '$lib/types/dev-box';
import { m } from '$lib/paraglide/messages.js';

export function providerName(provider: DevBoxProviderId): string {
	return { 'claude-code': 'Claude Code', codex: 'Codex', opencode: 'OpenCode' }[provider];
}

export function isActiveJob(status: DevBoxJobStatus): boolean {
	return status === 'queued' || status === 'leased' || status === 'running';
}

export function jobStatusLabel(status: DevBoxJobStatus): string {
	const labels: Record<DevBoxJobStatus, () => string> = {
		queued: m['dev_boxes.status_queued'],
		leased: m['dev_boxes.status_leased'],
		running: m['dev_boxes.status_running'],
		succeeded: m['dev_boxes.status_succeeded'],
		failed: m['dev_boxes.status_failed'],
		cancelled: m['dev_boxes.status_cancelled'],
		timeout: m['dev_boxes.status_timeout']
	};
	return labels[status]();
}

export function parseTestCommand(value: string): string[] {
	const command: unknown = JSON.parse(value);
	if (
		!Array.isArray(command) ||
		command.length === 0 ||
		command.length > 32 ||
		command.some((arg) => typeof arg !== 'string' || !arg.trim() || arg.length > 4096 || arg.includes('\0'))
	) {
		throw new Error(m['dev_boxes.test_command_invalid']());
	}
	return command;
}
