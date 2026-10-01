import { api } from './client';
import type { PaginatedResponse } from '$lib/types/common';
import type { CreateDevBoxJobInput, DevBox, DevBoxJob, DevBoxJobLog, DevBoxRegistration } from '$lib/types/dev-box';

const base = (slug: string) => `/api/workspaces/${encodeURIComponent(slug)}`;

export function listDevBoxes(slug: string): Promise<DevBox[]> {
	return api.get(`${base(slug)}/dev-boxes`);
}

export function registerDevBox(slug: string, name: string): Promise<DevBoxRegistration> {
	return api.post(`${base(slug)}/dev-boxes`, { name });
}

export function updateDevBox(
	slug: string,
	boxId: string,
	input: { enabled?: boolean; name?: string }
): Promise<DevBox> {
	return api.patch(`${base(slug)}/dev-boxes/${encodeURIComponent(boxId)}`, input);
}

export function rotateDevBoxToken(slug: string, boxId: string): Promise<DevBoxRegistration> {
	return api.post(`${base(slug)}/dev-boxes/${encodeURIComponent(boxId)}/rotate-token`, {});
}

export function removeDevBox(slug: string, boxId: string): Promise<void> {
	return api.delete(`${base(slug)}/dev-boxes/${encodeURIComponent(boxId)}`);
}

export function delegateDevBoxJob(slug: string, boxId: string, input: CreateDevBoxJobInput): Promise<DevBoxJob> {
	return api.post(`${base(slug)}/dev-boxes/${encodeURIComponent(boxId)}/jobs`, input);
}

export function listDevBoxJobs(
	slug: string,
	options: { issueId?: string; boxId?: string; page?: number; perPage?: number } = {}
): Promise<PaginatedResponse<DevBoxJob>> {
	const query = new URLSearchParams({ page: String(options.page ?? 1), per_page: String(options.perPage ?? 20) });
	if (options.issueId) query.set('issue_id', options.issueId);
	if (options.boxId) query.set('box_id', options.boxId);
	return api.get(`${base(slug)}/dev-box-jobs?${query}`);
}

export function getDevBoxJob(slug: string, jobId: string): Promise<DevBoxJob> {
	return api.get(`${base(slug)}/dev-box-jobs/${encodeURIComponent(jobId)}`);
}

export function cancelDevBoxJob(slug: string, jobId: string): Promise<DevBoxJob> {
	return api.post(`${base(slug)}/dev-box-jobs/${encodeURIComponent(jobId)}/cancel`);
}

export function listDevBoxJobLogs(slug: string, jobId: string, afterId = 0, limit = 500): Promise<DevBoxJobLog[]> {
	const query = new URLSearchParams({ after_id: String(afterId), limit: String(limit) });
	return api.get(`${base(slug)}/dev-box-jobs/${encodeURIComponent(jobId)}/logs?${query}`);
}
