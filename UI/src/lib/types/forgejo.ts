export interface ForgejoAccount {
	login: string;
	avatar_url?: string;
}

export interface ForgejoRepo {
	id: string;
	forgejo_repo_id: number;
	full_name: string;
	instance_url: string;
	default_branch: string;
	is_active: boolean;
}

export interface ForgejoAvailableRepo {
	forgejo_repo_id: number;
	full_name: string;
	default_branch: string;
	private: boolean;
	linked: boolean;
}

export interface ForgejoStatus {
	configured: boolean;
	connected: boolean;
	global_config: boolean;
	instance_url?: string;
	account?: ForgejoAccount;
	repos: ForgejoRepo[];
}

export interface ForgejoPullRequest {
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
	/** ku repository UUID, used to query the repository's Forgejo Actions. */
	repo_id: string;
	merged_at: string | null;
	created_at: string;
	updated_at: string;
}

export interface ForgejoBranch {
	id: string;
	name: string;
	html_url: string;
	repo_full_name: string;
	/** ku repository UUID. */
	repo_id: string;
	created_at: string;
}

export interface ForgejoCommit {
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

export interface ForgejoIssueActivity {
	pull_requests: ForgejoPullRequest[];
	branches: ForgejoBranch[];
	commits: ForgejoCommit[];
}

export interface ForgejoAutoTransition {
	event: 'branch_created' | 'pr_opened' | 'pr_checks_green' | 'pr_merged';
	target_status: string;
	target_status_id: string | null;
	target_assignee_id: string | null;
	assign_forgejo_user: boolean;
	is_active: boolean;
}

export interface ForgejoUserMapping {
	forgejo_login: string;
	user_id: string;
}

export interface ForgejoActionsPage<T> {
	data: T[];
	total_count: number;
	page: number;
	per_page: number;
	has_more: boolean;
}

export interface ForgejoActionRun {
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

export interface ForgejoActionJob {
	id: number;
	run_id: number;
	name: string;
	status: string;
	conclusion: string;
	attempt: number;
}

export interface ForgejoActionLogs {
	content: string;
	truncated: boolean;
	max_bytes: number;
}
