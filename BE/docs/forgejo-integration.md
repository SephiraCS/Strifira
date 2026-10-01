# Forgejo Integration

Ku links a Forgejo instance (Gitea API v1 compatible) to a workspace so
development activity appears on issues and drives status automation.

## How it works

Two configuration modes:

- **Per-workspace** (self-hosted default): Settings → Forgejo → paste the
  instance URL and a personal access token. The token is validated against
  `GET /api/v1/user` and stored AES-256-GCM encrypted
  (key derived from `JWT_SECRET + ":forgejo"`). Connecting seeds the default
  transition rules exactly once and adds a `PR` status (started category) to
  every team that lacks one.
- **Global** (SaaS-style): set `FORGEJO_URL` and `FORGEJO_TOKEN` in the
  environment; every workspace shares the connection and the settings page
  shows it as server-managed.

Linking a repository creates a webhook **on the Forgejo side** via the API
(`POST /repos/{owner}/{repo}/hooks`, type `forgejo`, events
`push`, `pull_request`, `create`, `action_run_success`,
`action_run_failure`, `action_run_recover`) with a per-repository random
secret stored encrypted. Unlinking deletes the webhook again. Deliveries are
authenticated with `X-Forgejo-Signature` (hex HMAC-SHA256 over the raw body);
the repository `html_url` in the payload pins the instance so `full_name`
lookups cannot collide across instances. `X-Gitea-*`/`X-Gogs-*` header
variants are accepted for compatibility.

## Environment

| Variable | Purpose |
| --- | --- |
| `FORGEJO_URL` | Shared instance origin (global mode), e.g. `https://git.terrascale.tech` |
| `FORGEJO_TOKEN` | Shared access token (global mode) |
| `FORGEJO_WEBHOOK_URL` | Webhook target override (dev tunnels); defaults to `FRONTEND_URL` + `/api/forgejo/webhook` |

The token needs `read:user` to validate the connection and `read:repository`
to browse repositories. Creating signed webhooks and cancelling Actions
require `write:repository`. Reruns use the native Forgejo run page and your
normal Forgejo login and repository permissions. Instance URLs must be HTTP(S) origins,
without a path, query, fragment, or embedded credentials. Ku refuses to change
the instance while repositories remain linked; unlink them first. Rotating the
token on the same instance is supported. The status separates a configured
connection from one whose token currently authenticates successfully.

## Actions

The workspace Actions page lists runs from linked repositories, with status
and branch/ref filters, paginated history, run details, jobs, attempts, and
plaintext logs. All workspace members can read Actions. Cancellation uses
the existing `workspace:manage` permission (currently the workspace owner);
pending, running, and blocked runs can be cancelled.

For completed runs (`success`, `failure`, `cancelled`, `skipped`), the owner's
**Rerun in Forgejo** link opens the native run page. Sign in with your normal
Forgejo account and use the rerun controls there; your Forgejo repository
permissions apply. Forgejo 16.0.5 has no REST endpoint for requesting reruns.
Ku does not submit a rerun request or report that one succeeded.

The REST base is
`/api/workspaces/:slug/forgejo/repos/:repoId/actions`, where `repoId` is the
**Ku linked repository UUID** from `/forgejo/status`, not the numeric Forgejo
repository ID. Run and job IDs are the numeric IDs returned by Forgejo.

| Method and suffix | Result |
| --- | --- |
| `GET /runs` | `{data, total_count, page, per_page, has_more}`; default 20 runs, maximum 100 per page |
| `GET /runs/:runId` | Run metadata and status |
| `GET /runs/:runId/jobs` | Paginated job list; default 100 per page |
| `GET /runs/:runId/jobs/:jobId` | Job metadata resolved from the run's native job list |
| `GET /runs/:runId/jobs/:jobId/logs?attempt=N` | `{content, truncated, max_bytes}`; plaintext content, latest attempt by default |
| `POST /runs/:runId/cancel` | Request cancellation of an active run |

Run filters are `status`, `ref`, `event`, `head_sha`, `workflow_id`, and
`run_number`, with `page` and `per_page` for pagination. `ref` is an exact Git
reference, such as `refs/heads/main` or `refs/tags/v1.0`; the UI converts a
branch name to `refs/heads/<name>`. Forgejo uses `waiting` for queued runs.
Job logs use **job IDs**, and `attempt` selects a job attempt; runs have no
single attempt number because jobs can be rerun independently.
The upstream contracts are defined in Forgejo's
[Forgejo 16.0.5 Actions API models](https://codeberg.org/forgejo/forgejo/src/tag/v16.0.5/modules/structs/action.go)
and [repository API handlers](https://codeberg.org/forgejo/forgejo/src/tag/v16.0.5/routers/api/v1/repo/action.go).

Plaintext job logs require Forgejo 16 or newer. Ku resolves job metadata through
Forgejo's supported `GET /repos/{owner}/{repo}/actions/runs/{runId}/jobs` and
checks that the requested job belongs to that run before returning metadata
or requesting logs. Forgejo 16's native job list exposes ID, run ID, name,
status, and attempt, without individual steps or their timings. The
**View workflow steps in Forgejo** link opens the native run page for those details.
Expired logs and jobs that have not yet executed can also return unavailable.
Each log request reads at most 2 MiB and reports truncation explicitly.

Before sending credentials, Ku checks the linked repository belongs to the
workspace, is active, and points to the configured instance. Clients reject
redirects to another origin, honor request cancellation, and use a 15-second
request timeout. Upstream error bodies are never forwarded to clients. A
failed link save returns an error and removes its newly created webhook.

## Issue linking

Issue identifiers (including single-letter team keys, e.g. `A-12` or `KUA-12`, matched
case-insensitively) are extracted from:

- pull request title, body, and head branch name — every referenced issue is
  linked (many-to-many), not just the first;
- commit messages on push;
- branch names on creation.

Closing keywords (`fixes|closes|resolves KUA-12`) additionally mark those
issues for closing when the pull request merges, even when no `pr_merged`
rule is configured. An explicitly disabled merge rule suppresses this fallback.

### Team identifier changes

Migration `000052` records issue identifiers against their stable issue UUID.
Team renames retain previous identifiers in the same transaction, so an existing
branch or pull request can still reference `OLD-12` after the issue becomes
`NEW-12`. Responses return the issue's current identifier. Identifiers stay
workspace-scoped and cannot be reassigned to another retained issue. Deleting
the issue removes its identifier history. Workspace transfer format 2 includes
this history.

Renames made before migration 52 have no recorded history. Restore only exact
identities verified from the rename and existing development activity; the
restoration helper does not infer former team keys from issue numbers:

```sh
# DATABASE_URL must already identify the intended database; psql is required.
python3 scripts/restore-issue-identifiers.py \
  --workspace example --map OLD-12=NEW-12

# After checking the dry-run receipt, explicitly commit the same mapping.
python3 scripts/restore-issue-identifiers.py \
  --workspace example --map OLD-12=NEW-12 --apply
```

Repeat `--map` for additional verified identities. Missing current identifiers
or identities already owned by another issue reject the whole operation.
Restoration is idempotent. The open-PR reconciler then links newly resolvable
references and applies a green-check transition even if the PR's checks were
already recorded as green. Migration `000053` retains the current PR description
from each accepted webhook, so this recovery includes description references.
Existing PR descriptions are captured by their next PR webhook; a current
description edit supplies one if a body-only reference needs immediate recovery.
Editing a PR's references replaces its explicit issue links, so correcting an
incorrect issue reference removes that link.

## Status automation

Per-workspace rules (Settings → Forgejo → Automations, each with a
configurable target status and optional target assignee):

| Event | Fires when | Default target |
| --- | --- | --- |
| `branch_created` | a branch whose name contains an identifier is pushed | `in_progress` |
| `pr_opened` | a referencing pull request opens or leaves draft mode | `in_progress` |
| `pr_checks_green` | CI checks on an open PR's head commit succeed across every result page, with no pending or failing checks. Forgejo Actions completion webhooks trigger a status refresh; a `status` webhook is also accepted from versions that deliver one | `pr` (the per-team PR state) |
| `pr_merged` | the pull request merges | `done` |

Slug targets resolve against each issue's own team workflow at fire time.
Status IDs selected from another team's workflow never become an issue's
status ID. Active rules must name a status present in the workspace, and the
settings page shows which teams have that status. The default `pr` state is
created when needed for teams added after connection. Transitions write issue
history and trigger the existing sub-issue/parent auto-close cascades.
Automation preserves completed and cancelled issues. Failed status writes
remain retryable; a green-check latch is recorded only after successful
processing. PR metadata edits retain check state when the head commit has not
changed.

Selecting a workspace member under **Assign to** adds that person to the
issue's current assignees when the event fires (existing assignees are never
removed — replacing the set made issues vanish from other assignees'
"My Issues" views); **Keep assignee** leaves the set untouched. A rule may
also assign someone without moving the issue by selecting **No transition**.
**Match Forgejo user** first resolves the event's Forgejo login through
**User mappings** in the same settings page. Without a mapping, it compares
the login with the Ku `name` of current workspace members, ignoring case. It
assigns only on a unique match; an absent or ambiguous match leaves the issue's
assignees unchanged and still applies the status transition. Branch creation
uses the webhook sender; PR events use the
PR author. Mapping rows are tied to workspace membership and disappear when
that member is removed.
New `in_progress` rules use this mode by default, and migration `000045`
enabled it for existing `in_progress` rules without a fixed assignee.
Assignee targets are checked against current workspace membership when saving
and when the rule fires; a stale target is skipped (with a warning log) while
the status transition still applies. To add a reviewer after merge, set **Pull
request merged** to the team's review status and choose that person under
**Assign to**.

Forgejo 16 does not emit commit-status webhooks, but its Actions run completion
webhooks carry the original pull request head SHA. Ku checks the aggregate
status after each completion, performs a catch-up pass on startup, and
reconciles open PRs every five minutes if a delivery was missed. Existing
hooks are recreated once with their current signing secret because Forgejo 16's
hook edit API ignores Actions event subscriptions.

## Surface

- REST: `PUT/DELETE /api/workspaces/:slug/forgejo/connection`,
  `GET /forgejo/status|repos`, `POST/DELETE /forgejo/repos[/:id]`,
  `GET/PATCH /forgejo/auto-transitions`, `GET/PUT /forgejo/user-mappings`,
  `GET/POST /forgejo/repos/:repoId/actions/...` (see Actions above),
  `GET /issues/:identifier/forgejo`, `GET /forgejo/issue-links`; public
  `POST /api/forgejo/webhook`.
- MCP: `forgejo_status`, `forgejo_link_repos`, `forgejo_unlink_repo`, and scoped [Actions tools](forgejo-actions-mcp.md) for runs, jobs, attempts, logs and cancellation.
  (write tools require the `workspace:manage` permission).
- UI: Settings → Forgejo; per-issue code activity (pull requests with live
  state and a CI-green badge, branches, commits) in the issue view's right
  sidebar; realtime `forgejo:pr_updated|branch_created|commit_pushed`
  WebSocket events.
- Dev Machines: checkouts clone/push over HTTPS with the workspace (or
  global) Forgejo token (`FORGEJO_TOKEN` in machine containers), pull requests
  are opened via the API, and URLs are validated against the linked instance.
