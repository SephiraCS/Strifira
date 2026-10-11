export interface GitHubAccount {
	login: string;
	avatar_url?: string;
}

export interface GitHubRepo {
	id: string;
	github_repo_id: number;
	full_name: string;
	instance_url: string;
	default_branch: string;
	is_active: boolean;
}

export interface GitHubAvailableRepo {
	github_repo_id: number;
	full_name: string;
	default_branch: string;
	private: boolean;
	linked: boolean;
}

export interface GitHubStatus {
	configured: boolean;
	connected: boolean;
	global_config: boolean;
	instance_url?: string;
	account?: GitHubAccount;
	repos: GitHubRepo[];
	disabled: boolean;
	/** Present only for workspace-managed connections restricted to one organization. */
	organization?: string;
	managed_by?: '' | 'workspace' | 'server';
}

export interface GitHubPullRequest {
	id: string;
	number: number;
	title: string;
	state: 'open' | 'closed' | 'merged' | 'draft';
	checks_green: boolean;
	author_login: string;
	author_avatar_url: string;
	html_url: string;
	head_branch: string;
	head_sha: string;
	base_branch: string;
	additions: number;
	deletions: number;
	repo_full_name: string;
	/** ku repository UUID, used to query the repository's GitHub Actions. */
	repo_id: string;
	merged_at: string | null;
	created_at: string;
	updated_at: string;
}

export interface GitHubBranch {
	id: string;
	name: string;
	html_url: string;
	repo_full_name: string;
	/** ku repository UUID. */
	repo_id: string;
	created_at: string;
}

export interface GitHubCommit {
	id: string;
	sha: string;
	short_sha: string;
	message: string;
	author_login: string;
	author_avatar_url: string;
	html_url: string;
	repo_full_name: string;
	committed_at: string;
}

export interface GitHubIssueActivity {
	pull_requests: GitHubPullRequest[];
	branches: GitHubBranch[];
	commits: GitHubCommit[];
}

export interface GitHubAutoTransition {
	event: 'branch_created' | 'pr_opened' | 'pr_checks_green' | 'pr_merged';
	target_status: string;
	target_status_id: string | null;
	target_assignee_id: string | null;
	assign_github_user: boolean;
	is_active: boolean;
}

export interface GitHubUserMapping {
	github_login: string;
	user_id: string;
}

export interface GitHubActionsPage<T> {
	data: T[];
	total_count: number;
	page: number;
	per_page: number;
	has_more: boolean;
}

export interface GitHubActionRun {
	id: number;
	name: string;
	title: string;
	run_number: number;
	event: string;
	status: string;
	conclusion: string;
	workflow_id: string;
	head_branch: string;
	head_sha: string;
	html_url: string;
	created_at: string | null;
	updated_at: string | null;
	started_at: string | null;
	completed_at: string | null;
}

export interface GitHubActionJob {
	id: number;
	run_id: number;
	name: string;
	status: string;
	conclusion: string;
	attempt: number;
}

export interface GitHubActionLogs {
	content: string;
	truncated: boolean;
	max_bytes: number;
}
