# GitHub Actions through MCP

Strifira exposes its native GitHub Actions integration through the streamable HTTP
MCP endpoint at `/mcp`. Authenticate with a personal Strifira API key in the
`Authorization: Bearer strifira_…` header. The existing REST Actions endpoints continue
to use JWT authentication.

Every call checks the API key owner's current workspace membership and resolves
an active repository link within that workspace. Reads require the key's `read`
scope and the workspace role's `issue:read` permission. Cancellation requires
the key's `write` scope and `workspace:manage`, which the current role model grants
to workspace owners. A read-only key can inspect runs and logs but cannot cancel
a run. Revoked and expired keys are rejected by the MCP HTTP authentication
middleware.

## Tools and arguments

Every tool takes `workspace`, the Strifira workspace slug, and `repo`, preferably the
linked repository's full name such as `Tools/strifira`. `repo` also accepts the Strifira
repository-link UUID. It does not accept an arbitrary upstream repository ID.
Repository names are matched without case sensitivity; an ambiguous name
requires the UUID.

Workspace slugs must be nonempty valid UTF-8, at most 100 bytes, and contain no
control characters. Workspace lookup and permission errors use fixed messages
without echoing the supplied slug or an internal exception.

| Tool | Additional arguments | Result |
| --- | --- | --- |
| `github_list_action_runs` | Optional `page`, `per_page`, `status`, `ref`, `event`, `head_sha`, `workflow_id` | Paginated native run DTOs |
| `github_get_action_run` | `run_id` | Native run DTO |
| `github_list_action_jobs` | `run_id`; optional `page`, `per_page` | Paginated native job DTOs |
| `github_get_action_job` | `run_id`, `job_id` | Native job DTO |
| `github_get_action_job_logs` | `run_id`, `job_id`; optional `attempt` | `content`, `truncated`, `max_bytes` |
| `github_cancel_action_run` | `run_id` | `{"status":"accepted"}` after GitHub accepts cancellation |

`run_id` is the positive native GitHub run ID, which differs from the displayed
run number. `job_id` must be positive and belong to that run. Omit `attempt` to
request the latest attempt, or supply a positive attempt number; GitHub serves
the job's current attempt, so a different explicit attempt is rejected. An
explicit zero or negative attempt is rejected.

Pagination defaults to page 1, with 20 runs or 100 jobs per page. `page` must be
between 1 and 10000 and `per_page` between 1 and 100. String filters are limited
to 512 bytes and reject NUL, carriage return, and newline characters. Accepted
status filters are `queued`, `in_progress`, and `completed`, plus the aliases
`waiting` and `running`; the shared service translates aliases to GitHub's
native statuses.

## Example calls

List in-progress runs:

```json
{
  "name": "github_list_action_runs",
  "arguments": {
    "workspace": "sephira",
    "repo": "Tools/strifira",
    "status": "in_progress",
    "per_page": 20
  }
}
```

Read a specific job's logs, using IDs returned by the run and job tools:

```json
{
  "name": "github_get_action_job_logs",
  "arguments": {
    "workspace": "sephira",
    "repo": "Tools/strifira",
    "run_id": 42,
    "job_id": 91
  }
}
```

These are the `params` objects of MCP `tools/call` requests. MCP clients handle
the protocol handshake and authentication header configuration.

## Native behavior and boundaries

Outputs retain the existing native DTO fields and status vocabulary. Run DTOs
include the run ID, run number, workflow, event, branch/SHA, URL, status,
conclusion, and timestamps. Job DTOs include the job ID, run ID, name, status,
conclusion, and attempt. Lists retain `data`, `total_count`, `page`, `per_page`,
and `has_more`.

The shared Actions service verifies the repository's workspace and pinned
GitHub origin, and verifies run/job membership before reading job metadata or
logs. Repository resolution reads stored links without invoking integration
status setup or changing automation rules. Service failures become fixed,
meaningful MCP tool errors; raw upstream errors, request URLs, response bodies,
and credentials are not returned.

Job logs are bounded to 2 MiB. Inspect `truncated` before treating returned
content as a complete log. GitHub expires the logs of completed runs after
their retention period; expired logs return a fixed unavailable error.
Cancellation is allowed only for runs whose native state permits it, currently
`queued` or `in_progress`.

The available tools cover listing, inspection, logs, and cancellation. Reruns
and step APIs are not exposed.
