import { api } from './client';
import type {
	GitHubStatus,
	GitHubAvailableRepo,
	GitHubIssueActivity,
	GitHubAutoTransition,
	GitHubUserMapping,
	GitHubActionsPage,
	GitHubActionRun,
	GitHubActionJob,
	GitHubActionLogs
} from '$lib/types/github';
import { safeGitHubBranchUrl, safeGitHubCommitUrl, safeGitHubPullRequestUrl } from '$lib/security/github-url';

const githubStatusCache = new Map<string, { expiresAt: number; request: Promise<GitHubStatus> }>();

export function getGitHubStatus(slug: string): Promise<GitHubStatus> {
	const request = api.get<GitHubStatus>(`/api/workspaces/${slug}/github/status`);
	githubStatusCache.set(slug, { expiresAt: Date.now() + 30000, request });
	void request.catch(() => {
		if (githubStatusCache.get(slug)?.request === request) githubStatusCache.delete(slug);
	});
	return request;
}

export function getCachedGitHubStatus(slug: string): Promise<GitHubStatus> {
	const cached = githubStatusCache.get(slug);
	return cached && cached.expiresAt > Date.now() ? cached.request : getGitHubStatus(slug);
}

export async function connectGitHub(
	slug: string,
	instanceUrl: string,
	token: string,
	organization?: string
): Promise<{ login: string }> {
	const trimmedOrganization = organization?.trim();
	const result = await api.put<{ login: string }>(`/api/workspaces/${slug}/github/connection`, {
		instance_url: instanceUrl,
		token,
		...(trimmedOrganization ? { organization: trimmedOrganization } : {})
	});
	githubStatusCache.delete(slug);
	return result;
}

export async function disconnectGitHub(slug: string): Promise<void> {
	await api.delete<void>(`/api/workspaces/${slug}/github/connection`);
	githubStatusCache.delete(slug);
}

export async function disableGitHub(slug: string): Promise<void> {
	await api.post<void>(`/api/workspaces/${slug}/github/disable`);
	githubStatusCache.delete(slug);
}

export async function enableGitHub(slug: string): Promise<void> {
	await api.post<void>(`/api/workspaces/${slug}/github/enable`);
	githubStatusCache.delete(slug);
}

export function listGitHubRepos(slug: string): Promise<GitHubAvailableRepo[]> {
	return api.get<GitHubAvailableRepo[]>(`/api/workspaces/${slug}/github/repos`);
}

export function linkGitHubRepos(slug: string, githubRepoIds: number[]): Promise<void> {
	return api.post<void>(`/api/workspaces/${slug}/github/repos`, { github_repo_ids: githubRepoIds });
}

export function unlinkGitHubRepo(slug: string, id: string): Promise<void> {
	return api.delete<void>(`/api/workspaces/${slug}/github/repos/${id}`);
}

export async function getIssueGitHubActivity(
	slug: string,
	identifier: string,
	instanceUrl: string,
	signal?: AbortSignal
): Promise<GitHubIssueActivity> {
	const activity = await api.fetch<GitHubIssueActivity>(`/api/workspaces/${slug}/issues/${identifier}/github`, {
		signal
	});
	return {
		pull_requests: activity.pull_requests.map((pullRequest) => ({
			...pullRequest,
			html_url: safeGitHubPullRequestUrl(pullRequest.html_url, pullRequest.repo_full_name, instanceUrl) ?? ''
		})),
		branches: activity.branches.map((branch) => ({
			...branch,
			html_url: safeGitHubBranchUrl(branch.html_url, branch.repo_full_name, instanceUrl) ?? ''
		})),
		commits: activity.commits.map((commit) => ({
			...commit,
			html_url: safeGitHubCommitUrl(commit.html_url, commit.repo_full_name, instanceUrl) ?? ''
		}))
	};
}

export function listAutoTransitions(slug: string): Promise<GitHubAutoTransition[]> {
	return api.get<GitHubAutoTransition[]>(`/api/workspaces/${slug}/github/auto-transitions`);
}

export function updateAutoTransitions(slug: string, transitions: GitHubAutoTransition[]): Promise<void> {
	return api.patch<void>(`/api/workspaces/${slug}/github/auto-transitions`, { transitions });
}

export function listUserMappings(slug: string): Promise<GitHubUserMapping[]> {
	return api.get<GitHubUserMapping[]>(`/api/workspaces/${slug}/github/user-mappings`);
}

export function updateUserMappings(slug: string, mappings: GitHubUserMapping[]): Promise<void> {
	return api.put<void>(`/api/workspaces/${slug}/github/user-mappings`, { mappings });
}

function actionsPath(slug: string, repoId: string): string {
	return `/api/workspaces/${encodeURIComponent(slug)}/github/repos/${encodeURIComponent(repoId)}/actions`;
}

export function listGitHubActionRuns(
	slug: string,
	repoId: string,
	filters: { page?: number; perPage?: number; status?: string; ref?: string; headSha?: string } = {},
	signal?: AbortSignal
): Promise<GitHubActionsPage<GitHubActionRun>> {
	const query = new URLSearchParams({ page: String(filters.page ?? 1), per_page: String(filters.perPage ?? 20) });
	if (filters.status) query.set('status', filters.status);
	if (filters.headSha) query.set('head_sha', filters.headSha);
	if (filters.ref) query.set('ref', filters.ref.startsWith('refs/') ? filters.ref : `refs/heads/${filters.ref}`);
	return api.fetch(`${actionsPath(slug, repoId)}/runs?${query}`, { signal });
}

export function getGitHubActionRun(
	slug: string,
	repoId: string,
	runId: number,
	signal?: AbortSignal
): Promise<GitHubActionRun> {
	return api.fetch(`${actionsPath(slug, repoId)}/runs/${runId}`, { signal });
}

export function listGitHubActionJobs(
	slug: string,
	repoId: string,
	runId: number,
	page = 1,
	signal?: AbortSignal
): Promise<GitHubActionsPage<GitHubActionJob>> {
	return api.fetch(`${actionsPath(slug, repoId)}/runs/${runId}/jobs?page=${page}&per_page=100`, { signal });
}

export function getGitHubActionJob(
	slug: string,
	repoId: string,
	runId: number,
	jobId: number,
	signal?: AbortSignal
): Promise<GitHubActionJob> {
	return api.fetch(`${actionsPath(slug, repoId)}/runs/${runId}/jobs/${jobId}`, { signal });
}

export function getGitHubActionLogs(
	slug: string,
	repoId: string,
	runId: number,
	jobId: number,
	attempt: number,
	signal?: AbortSignal
): Promise<GitHubActionLogs> {
	const query = new URLSearchParams();
	if (attempt > 0) query.set('attempt', String(attempt));
	return api.fetch(`${actionsPath(slug, repoId)}/runs/${runId}/jobs/${jobId}/logs${query.size ? `?${query}` : ''}`, {
		signal
	});
}

export function cancelGitHubActionRun(slug: string, repoId: string, runId: number): Promise<{ status: 'accepted' }> {
	return api.post(`${actionsPath(slug, repoId)}/runs/${runId}/cancel`);
}
