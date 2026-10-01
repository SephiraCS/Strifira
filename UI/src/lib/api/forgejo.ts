import { api } from './client';
import type {
	ForgejoStatus,
	ForgejoAvailableRepo,
	ForgejoIssueActivity,
	ForgejoAutoTransition,
	ForgejoUserMapping,
	ForgejoActionsPage,
	ForgejoActionRun,
	ForgejoActionJob,
	ForgejoActionLogs
} from '$lib/types/forgejo';
import { safeForgejoBranchUrl, safeForgejoCommitUrl, safeForgejoPullRequestUrl } from '$lib/security/forgejo-url';

const forgejoStatusCache = new Map<string, { expiresAt: number; request: Promise<ForgejoStatus> }>();

export function getForgejoStatus(slug: string): Promise<ForgejoStatus> {
	const request = api.get<ForgejoStatus>(`/api/workspaces/${slug}/forgejo/status`);
	forgejoStatusCache.set(slug, { expiresAt: Date.now() + 30000, request });
	void request.catch(() => {
		if (forgejoStatusCache.get(slug)?.request === request) forgejoStatusCache.delete(slug);
	});
	return request;
}

export function getCachedForgejoStatus(slug: string): Promise<ForgejoStatus> {
	const cached = forgejoStatusCache.get(slug);
	return cached && cached.expiresAt > Date.now() ? cached.request : getForgejoStatus(slug);
}

export async function connectForgejo(slug: string, instanceUrl: string, token: string): Promise<{ login: string }> {
	const result = await api.put<{ login: string }>(`/api/workspaces/${slug}/forgejo/connection`, {
		instance_url: instanceUrl,
		token
	});
	forgejoStatusCache.delete(slug);
	return result;
}

export async function disconnectForgejo(slug: string): Promise<void> {
	await api.delete<void>(`/api/workspaces/${slug}/forgejo/connection`);
	forgejoStatusCache.delete(slug);
}

export function listForgejoRepos(slug: string): Promise<ForgejoAvailableRepo[]> {
	return api.get<ForgejoAvailableRepo[]>(`/api/workspaces/${slug}/forgejo/repos`);
}

export function linkForgejoRepos(slug: string, forgejoRepoIds: number[]): Promise<void> {
	return api.post<void>(`/api/workspaces/${slug}/forgejo/repos`, { forgejo_repo_ids: forgejoRepoIds });
}

export function unlinkForgejoRepo(slug: string, id: string): Promise<void> {
	return api.delete<void>(`/api/workspaces/${slug}/forgejo/repos/${id}`);
}

export async function getIssueForgejoActivity(
	slug: string,
	identifier: string,
	instanceUrl: string,
	signal?: AbortSignal
): Promise<ForgejoIssueActivity> {
	const activity = await api.fetch<ForgejoIssueActivity>(`/api/workspaces/${slug}/issues/${identifier}/forgejo`, {
		signal
	});
	return {
		pull_requests: activity.pull_requests.map((pullRequest) => ({
			...pullRequest,
			html_url: safeForgejoPullRequestUrl(pullRequest.html_url, pullRequest.repo_full_name, instanceUrl) ?? ''
		})),
		branches: activity.branches.map((branch) => ({
			...branch,
			html_url: safeForgejoBranchUrl(branch.html_url, branch.repo_full_name, instanceUrl) ?? ''
		})),
		commits: activity.commits.map((commit) => ({
			...commit,
			html_url: safeForgejoCommitUrl(commit.html_url, commit.repo_full_name, instanceUrl) ?? ''
		}))
	};
}

export function listAutoTransitions(slug: string): Promise<ForgejoAutoTransition[]> {
	return api.get<ForgejoAutoTransition[]>(`/api/workspaces/${slug}/forgejo/auto-transitions`);
}

export function updateAutoTransitions(slug: string, transitions: ForgejoAutoTransition[]): Promise<void> {
	return api.patch<void>(`/api/workspaces/${slug}/forgejo/auto-transitions`, { transitions });
}

export function listUserMappings(slug: string): Promise<ForgejoUserMapping[]> {
	return api.get<ForgejoUserMapping[]>(`/api/workspaces/${slug}/forgejo/user-mappings`);
}

export function updateUserMappings(slug: string, mappings: ForgejoUserMapping[]): Promise<void> {
	return api.put<void>(`/api/workspaces/${slug}/forgejo/user-mappings`, { mappings });
}

function actionsPath(slug: string, repoId: string): string {
	return `/api/workspaces/${encodeURIComponent(slug)}/forgejo/repos/${encodeURIComponent(repoId)}/actions`;
}

export function listForgejoActionRuns(
	slug: string,
	repoId: string,
	filters: { page?: number; status?: string; ref?: string } = {},
	signal?: AbortSignal
): Promise<ForgejoActionsPage<ForgejoActionRun>> {
	const query = new URLSearchParams({ page: String(filters.page ?? 1), per_page: '20' });
	if (filters.status) query.set('status', filters.status);
	if (filters.ref) query.set('ref', filters.ref.startsWith('refs/') ? filters.ref : `refs/heads/${filters.ref}`);
	return api.fetch(`${actionsPath(slug, repoId)}/runs?${query}`, { signal });
}

export function getForgejoActionRun(
	slug: string,
	repoId: string,
	runId: number,
	signal?: AbortSignal
): Promise<ForgejoActionRun> {
	return api.fetch(`${actionsPath(slug, repoId)}/runs/${runId}`, { signal });
}

export function listForgejoActionJobs(
	slug: string,
	repoId: string,
	runId: number,
	page = 1,
	signal?: AbortSignal
): Promise<ForgejoActionsPage<ForgejoActionJob>> {
	return api.fetch(`${actionsPath(slug, repoId)}/runs/${runId}/jobs?page=${page}&per_page=100`, { signal });
}

export function getForgejoActionJob(
	slug: string,
	repoId: string,
	runId: number,
	jobId: number,
	signal?: AbortSignal
): Promise<ForgejoActionJob> {
	return api.fetch(`${actionsPath(slug, repoId)}/runs/${runId}/jobs/${jobId}`, { signal });
}

export function getForgejoActionLogs(
	slug: string,
	repoId: string,
	runId: number,
	jobId: number,
	attempt: number,
	signal?: AbortSignal
): Promise<ForgejoActionLogs> {
	const query = new URLSearchParams();
	if (attempt > 0) query.set('attempt', String(attempt));
	return api.fetch(`${actionsPath(slug, repoId)}/runs/${runId}/jobs/${jobId}/logs${query.size ? `?${query}` : ''}`, {
		signal
	});
}

export function cancelForgejoActionRun(slug: string, repoId: string, runId: number): Promise<{ status: 'accepted' }> {
	return api.post(`${actionsPath(slug, repoId)}/runs/${runId}/cancel`);
}
