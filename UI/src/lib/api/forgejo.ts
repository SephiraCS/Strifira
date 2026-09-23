import { api } from './client';
import type {
	ForgejoStatus,
	ForgejoAvailableRepo,
	ForgejoIssueActivity,
	ForgejoAutoTransition,
	ForgejoUserMapping
} from '$lib/types/forgejo';
import {
	safeForgejoBranchUrl,
	safeForgejoCommitUrl,
	safeForgejoPullRequestUrl
} from '$lib/security/forgejo-url';

export function getForgejoStatus(slug: string): Promise<ForgejoStatus> {
	return api.get<ForgejoStatus>(`/api/workspaces/${slug}/forgejo/status`);
}

export function connectForgejo(slug: string, instanceUrl: string, token: string): Promise<{ login: string }> {
	return api.put<{ login: string }>(`/api/workspaces/${slug}/forgejo/connection`, {
		instance_url: instanceUrl,
		token
	});
}

export function disconnectForgejo(slug: string): Promise<void> {
	return api.delete<void>(`/api/workspaces/${slug}/forgejo/connection`);
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
	instanceUrl: string
): Promise<ForgejoIssueActivity> {
	const activity = await api.get<ForgejoIssueActivity>(
		`/api/workspaces/${slug}/issues/${identifier}/forgejo`
	);
	return {
		pull_requests: activity.pull_requests.map((pullRequest) => ({
			...pullRequest,
			html_url:
				safeForgejoPullRequestUrl(pullRequest.html_url, pullRequest.repo_full_name, instanceUrl) ?? ''
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
