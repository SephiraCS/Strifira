import { api } from './client';
import type { AISettings, UpdateAISettingsRequest } from '$lib/types/ai-settings';

export function getAISettings(slug: string): Promise<AISettings> {
	return api.get<AISettings>(`/api/workspaces/${slug}/ai-settings`);
}

export function getIssueCopyPrompt(
	slug: string
): Promise<Pick<AISettings, 'issue_copy_prompt' | 'default_issue_copy_prompt'>> {
	return api.get(`/api/workspaces/${slug}/ai-settings/issue-copy-prompt`);
}

export function updateAISettings(slug: string, req: UpdateAISettingsRequest): Promise<AISettings> {
	return api.patch<AISettings>(`/api/workspaces/${slug}/ai-settings`, req);
}

export function testAIConnection(slug: string): Promise<{ model: string; latency_ms: number }> {
	return api.post(`/api/workspaces/${slug}/ai-settings/test`);
}

export function reworkText(slug: string, req: { text: string; context?: string }): Promise<{ text: string }> {
	return api.post<{ text: string }>(`/api/workspaces/${slug}/ai/rework`, req);
}

/** Detects the backend's AI_NOT_CONFIGURED error code (see client.ts fetch). */
export function isAINotConfiguredError(err: unknown): boolean {
	return !!err && typeof err === 'object' && (err as { error?: { code?: string } }).error?.code === 'AI_NOT_CONFIGURED';
}
