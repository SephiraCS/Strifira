# Forgejo Integration

Ku links a Forgejo instance (Gitea API v1 compatible) to a workspace so
development activity appears on issues and drives status automation.

## How it works

Two configuration modes:

- **Per-workspace** (self-hosted default): Settings → Forgejo → paste the
  instance URL and a personal access token. The token is validated against
  `GET /api/v1/user` and stored AES-256-GCM encrypted
  (key derived from `JWT_SECRET + ":forgejo"`). Connecting seeds the default
  transition rules exactly once.
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

The token needs repository read scope; webhook creation requires repository
write scope.

## Issue linking

Issue identifiers (`[A-Z][A-Z0-9]+-\d+`, e.g. `KUA-12`, matched
case-insensitively) are extracted from:

- pull request title, body, and head branch name — every referenced issue is
  linked (many-to-many), not just the first;
- commit messages on push;
- branch names on creation.

Closing keywords (`fixes|closes|resolves KUA-12`) additionally mark those
issues for closing when the pull request merges, even when no `pr_merged`
rule is configured.

## Status automation

Per-workspace rules (Settings → Forgejo → Automations, each with a
configurable target status and optional target assignee):

| Event | Fires when | Default target |
| --- | --- | --- |
| `branch_created` | a branch whose name contains an identifier is pushed | `in_progress` |
| `pr_opened` | a referencing pull request opens | `in_progress` |
| `pr_checks_green` | **all** CI checks on an open PR's head commit succeed (combined status API; at least one check must exist). Forgejo Actions completion webhooks trigger a status refresh; a `status` webhook is also accepted from versions that deliver one | `pr` (the Pull Request Review state) |
| `pr_merged` | the pull request merges | `done` |

Slug targets resolve against each issue's team workflow at fire time, so one
rule covers teams with custom status sets. Transitions write issue history and
trigger the existing sub-issue/parent auto-close cascades.

Selecting a workspace member under **Assign to** replaces the issue's current
assignees when that event fires; **Keep assignee** preserves them. A rule may
also assign someone without moving the issue by selecting **No transition**.
**Match Forgejo user** first resolves the event's Forgejo login through
**User mappings** in the same settings page. Without a mapping, it compares
the login with the Ku `name` of current workspace members, ignoring case. It
assigns only on a unique match; an absent or ambiguous match leaves the issue's
assignees unchanged. Branch creation uses the webhook sender; PR events use the
PR author. Mapping rows are tied to workspace membership and disappear when
that member is removed.
New `in_progress` rules use this mode by default, and migration `000045`
enables it for existing `in_progress` rules without a fixed assignee.
Fixed assignees are checked against current workspace membership when saving
and when the rule fires. To hand an issue to a reviewer after merge, set **Pull
request merged** to the team's review status and choose that person under
**Assign to**.

Forgejo 16 does not emit commit-status webhooks, but its Actions run completion
webhooks carry the original pull request head SHA. Ku checks the aggregate
status after each completion, performs a catch-up pass on startup, and
reconciles open PRs every five minutes if a delivery was missed. Existing
hooks are recreated once with their current signing secret because Forgejo 16's
hook edit API ignores Actions event subscriptions. The older per-team "PR"
status remains available for workspaces that already use it.

## Surface

- REST: `PUT/DELETE /api/workspaces/:slug/forgejo/connection`,
  `GET /forgejo/status|repos`, `POST/DELETE /forgejo/repos[/:id]`,
  `GET/PATCH /forgejo/auto-transitions`, `GET/PUT /forgejo/user-mappings`,
  `GET /issues/:identifier/forgejo`, `GET /forgejo/issue-links`; public
  `POST /api/forgejo/webhook`.
- MCP: `forgejo_status`, `forgejo_link_repos`, `forgejo_unlink_repo`
  (write tools require the `workspace:manage` permission).
- UI: Settings → Forgejo; per-issue code activity (pull requests with live
  state and a CI-green badge, branches, commits) in the issue view's right
  sidebar; realtime `forgejo:pr_updated|branch_created|commit_pushed`
  WebSocket events.
- Dev Machines: checkouts clone/push over HTTPS with the workspace Forgejo
  token (`FORGEJO_TOKEN` in machine containers), pull requests are opened via
  the API, and URLs are validated against the linked instance.
