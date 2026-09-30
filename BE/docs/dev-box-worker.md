# Persistent development box worker

The `dev-box-worker` connects outbound to Ku and runs issue jobs on an existing development VM. It requires a dedicated development box token; personal API keys and server credentials cannot authenticate it. The worker maintains heartbeats and one immutable job lease, streams bounded logs, executes the selected local coding CLI, runs the configured test argv independently, and returns a reviewable patch. It never pushes changes or creates pull requests.

## Install and register

Build on a Linux development box with the repository's Go version:

```sh
cd ku/BE
install -d "$HOME/.local/bin"
go build -o "$HOME/.local/bin/dev-box-worker" ./cmd/dev-box-worker
```

In Ku, register a box in the workspace's development box settings. Save the once-displayed `kub_` token in a private file without putting it in shell arguments, environment variables, or shell history. For example, this interactive command reads it without echoing:

```sh
install -d -m 700 "$HOME/.config/ku-worker"
read -r -s -p 'Development box token: ' worker_token
printf '\n'
umask 077
printf '%s\n' "$worker_token" > "$HOME/.config/ku-worker/worker.token"
unset worker_token
chmod 600 "$HOME/.config/ku-worker/worker.token"
```

An authenticated MCP client can also use `register_dev_box` with
`{"workspace":"your-workspace","name":"ku-dev"}`. The client authenticates
to `/mcp` with the owner's personal `ku_` API key; registration requires its
`write` scope and the `dev_machine:create` workspace permission. The response
contains `box`, a once-returned dedicated `token`, and a protection notice.
Capture that response privately, then deliver only the new `kub_` token to the
worker through a protected file or your scoped secret manager. Do not log the
response or repeat the token in chat, command arguments or provider prompts.
The personal API key stays with the MCP client and cannot authenticate the
worker protocol.

`list_dev_boxes` returns the caller's own boxes and provider inventory without
credentials. `update_dev_box` accepts `box_id` plus `name` and/or `enabled`;
`rotate_dev_box_token` returns a replacement token once and invalidates the
previous token. `revoke_dev_box` revokes the registration without stopping or
deleting the VM. Each mutation requires `write` scope and `dev_machine:manage`.
Box and job IDs are canonical nonzero UUIDs. Workspace administrators do not
gain access to another member's registrations or jobs.

After the configured worker has advertised its providers, queue work with
`create_dev_box_job`:

```json
{
  "workspace": "your-workspace",
  "box_id": "BOX_UUID_FROM_REGISTER_OR_LIST",
  "issue": "ENG-42",
  "repository_full_name": "owner/repo",
  "base_branch": "main",
  "provider": "claude-code",
  "prompt": "Implement the issue's acceptance criteria.",
  "test_command": ["make", "test"],
  "acceptance_criteria": ["The repository's independent tests pass."],
  "max_runtime_seconds": 300
}
```

The issue accepts its identifier or UUID. Repository and base branch are
required for this MCP operation; the service validates an active repository
linked to the same workspace, provider availability and development policy.
Selection applies to that job without changing workspace, team, project or
issue development defaults. The configured local repository and origin must
still match. Choose the repository's actual test argv; the example is not a
universal test command.

`list_dev_box_jobs` accepts optional `box_id` and `issue` filters, `page`
(default 1) and `per_page` (default 50, maximum 100). `get_dev_box_job` accepts
`job_id` and returns status and the completed result. `get_dev_box_job_logs`
accepts `job_id`, `after_id` (default 0) and `limit` (default 100, maximum 500);
use its `next_after_id` to continue reading. These reads require API key `read`
scope and `dev_machine:read`, and work with read-only personal keys. `cancel_dev_box_job`
requires `write` scope and `dev_machine:manage`. All tools resolve current
membership and use the same creator and demo restrictions as the UI. Worker
lease hashes and registration token hashes are never returned.

The token must be a regular file owned by the worker user, with mode `0600` or `0400`. Final symlinks, other permissions, and personal keys are rejected. Revoking or rotating the box token stops further execution; a lost lease stops the whole process group.

Authenticate installed Claude Code, Codex, or OpenCode locally using their normal login commands. Provider login files stay on the development box. Clone authorized repositories locally using your own origin-specific Git credentials. The worker does not fetch, clone, or accept server-supplied credentials; refresh the configured checkout before requesting work when needed.

Create `$HOME/.config/ku-worker/worker.json` with absolute paths:

```json
{
  "server_url": "https://ku.example.com",
  "token_file": "/home/developer/.config/ku-worker/worker.token",
  "work_root": "/home/developer/.local/state/ku-worker/worktrees",
  "execution_mode": "trusted-local",
  "repositories": {
    "owner/repo": {
      "path": "/home/developer/Projects/repo",
      "url": "https://forgejo.example.com/owner/repo",
      "origin_url": "https://forgejo.example.com/owner/repo.git"
    }
  },
  "providers": {
    "claude-code": "/usr/local/bin/claude",
    "codex": "/home/developer/.local/bin/codex",
    "opencode": "/usr/local/bin/opencode"
  }
}
```

Configure only installed providers. Repository keys and URLs must exactly match the linked Forgejo repository returned in a Ku job. If the existing checkout uses an SSH origin, add `"origin_url": "git@forgejo.example.com:owner/repo.git"` to its repository entry; `url` remains the server's HTTPS URL. The origin is compared to this locally configured value before work begins. A claim cannot substitute another origin or repository path.

`server_url` requires HTTPS. Isolated local development can opt into `"localhost_http": true` for literal loopback or `localhost` HTTP URLs. Redirects are rejected so authenticated requests cannot cross origins.

## Execution boundary

`execution_mode: "trusted-local"` explicitly authorizes coding tools and the issue's configured test argv to run as the development box user. Use a dedicated VM account for trusted workspace members and repositories. Git worktrees preserve the original checkout's working tree and index; they are not an operating system sandbox. Tools and repository tests may access the account's filesystem, network, and local provider login files. This mode is unsuitable for untrusted repositories or adversarial prompts.

The worker uses Claude's `dontAsk` mode and Codex's `workspace-write` sandbox. OpenCode uses its locally configured permissions. Provider API environment variables are included only for the matching provider; test processes do not receive them. Ku, Forgejo, GitHub, and unrelated credential environment variables are omitted. The worker token is never passed to child processes or sent inside log/result bodies. Git's default push configuration and the `origin` push URL are disabled for accidental publishing. This is a guard against accidental publishing, not an isolation boundary against code executing as the same account.

Run persistently:

```sh
"$HOME/.local/bin/dev-box-worker" --config "$HOME/.config/ku-worker/worker.json"
```

For a commissioning check, use `--once`. It heartbeats, claims at most one job, executes and completes it, then exits. With no pending job it exits after the heartbeat/claim check:

```sh
"$HOME/.local/bin/dev-box-worker" --config "$HOME/.config/ku-worker/worker.json" --once
```

## Results and recovery

Every job uses Ku's assigned unique `ku/<issue-identifier>-<job-id-prefix>` branch and a separate worktree under the private `0700` work root. The worker leaves the branch and worktree on disk so changes can be inspected after successful work, failed tests, cancellation, or a lost lease. It uses a separate temporary index to collect committed, staged, unstaged, and new files, without changing the original checkout's index. The uploaded patch is limited to 1 MiB; larger changes remain available locally and fail the completion's artifact check. Provider stdout/stderr and uploaded logs are bounded. Truncated provider output fails closed because a terminal success could have been omitted.

The job's `test_command` is a single argv array, executed by the worker after the provider exits. A failed test command overrides a successful provider result. An empty test command is reported as `not_run`; no passing tests are invented. Cancellation, shutdown, runtime expiry, token revocation, or failed lease renewal terminates the invocation's process group, escalating to `SIGKILL` after 500 ms. Completion after lease loss is deliberately suppressed.

Inspect a retained worktree before applying or publishing any patch:

```sh
git -C /absolute/path/to/worktree diff
git -C /absolute/path/to/original worktree list
```

Remove a worktree and its branch only after its result has been reviewed. No automatic cleanup removes unfinished work.

## Live acceptance

The opt-in `scripts/check-dev-box-live.py` launcher tests the actual API, dedicated token, installed coding CLI, independent tests, review patch, streamed logs, failed-test override, and running cancellation in a disposable PostgreSQL/backend/account/workspace/repository fixture. Run it inside the development VM after other build and acceptance jobs finish, using reviewed backend and worker artifacts:

```sh
python3 scripts/check-dev-box-live.py \
  --server-bin /absolute/path/ku-server \
  --worker-bin /absolute/path/dev-box-worker \
  --provider claude-code \
  --provider-path /absolute/path/claude \
  --report /absolute/path/dev-box-receipt.json
```

`--dry-run` describes the operations without reading credentials or creating resources. The launcher uses each coding CLI's existing local owner authentication and never accesses a deployed Ku service. The receipt includes tested artifact hashes and outcome metadata; temporary tokens and fixture resources are removed before it is written.
