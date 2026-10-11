export const activeActionStatuses = new Set(['queued', 'waiting', 'running', 'blocked', 'pending', 'in_progress']);

export function isActiveAction(status: string): boolean {
	return activeActionStatuses.has(status);
}

export function actionStatus(status: string, conclusion?: string): string {
	if (status === 'completed' && conclusion) return conclusion;
	if (status === 'in_progress') return 'running';
	if (status === 'pending') return 'queued';
	if (status === 'canceled') return 'cancelled';
	return status;
}

export function actionDuration(start: string | null, end: string | null): string {
	if (!start || !end) return '';
	const milliseconds = new Date(end).getTime() - new Date(start).getTime();
	if (!Number.isFinite(milliseconds) || milliseconds < 0) return '';
	const seconds = Math.round(milliseconds / 1000);
	return seconds < 60 ? `${seconds}s` : `${Math.floor(seconds / 60)}m ${seconds % 60}s`;
}

// Strip terminal control sequences before displaying or downloading plain text.
export function plainActionLogs(content: string): string {
	return content
		.replace(/\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)/g, '')
		.replace(/\x1b\[[0-?]*[ -/]*[@-~]/g, '')
		.replace(/[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]/g, '');
}
