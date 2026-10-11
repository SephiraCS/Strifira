export type DevBoxProviderId = 'claude-code' | 'codex' | 'opencode';

export interface DevBox {
	id: string;
	workspace_id: string;
	created_by_user_id: string;
	name: string;
	enabled: boolean;
	hostname: string;
	os: string;
	arch: string;
	version: string;
	providers: Array<{ id: DevBoxProviderId; version: string }>;
	last_seen_at?: string;
	created_at: string;
	updated_at: string;
	revoked_at?: string;
	online: boolean;
	token_prefix: string;
}

export interface DevBoxRegistration {
	box: DevBox;
	token: string;
}

export type DevBoxJobStatus = 'queued' | 'leased' | 'running' | 'succeeded' | 'failed' | 'cancelled' | 'timeout';

export interface DevBoxJobResult {
	status: DevBoxJobStatus;
	exit_code: number;
	summary: string;
	changed_files: string[];
	branch: string;
	patch: string;
	test_status: 'passed' | 'failed' | 'not_run';
	test_exit_code?: number;
	tests_run: string[];
	risk_notes: string[];
}

export interface DevBoxJob {
	id: string;
	workspace_id: string;
	box_id: string;
	requested_by_user_id: string;
	issue_id: string;
	issue_identifier: string;
	provider: DevBoxProviderId;
	prompt: string;
	github_repo_id: string;
	repository_url: string;
	repository_full_name: string;
	base_branch: string;
	working_branch: string;
	test_command: string[];
	acceptance_criteria: string[];
	max_runtime_seconds: number;
	status: DevBoxJobStatus;
	created_at: string;
	started_at?: string;
	completed_at?: string;
	deadline_at?: string;
	cancel_requested: boolean;
	logs_bytes: number;
	logs_truncated: boolean;
	result?: DevBoxJobResult;
}

export interface DevBoxJobLog {
	id: number;
	job_id: string;
	sequence: number;
	stream: string;
	content: string;
	created_at: string;
}

export interface CreateDevBoxJobInput {
	issue_id: string;
	provider: DevBoxProviderId;
	prompt?: string;
	test_command: string[];
	acceptance_criteria: string[];
	max_runtime_seconds: number;
}
