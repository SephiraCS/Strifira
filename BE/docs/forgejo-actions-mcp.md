# Forgejo Actions through MCP

Ku exposes its native Forgejo Actions integration through the streamable HTTP
MCP endpoint at `/mcp`. Authenticate with a personal Ku API key in the
`Authorization: Bearer ku_…` header. The existing REST Actions endpoints continue
to use JWT authentication.

Every call checks the API key owner's current workspace membership and resolves
an active repository link within that workspace. Reads require the key's `read`
scope and the workspace role's `issue:read` permission. Cancellation requires
the key's `write` scope and `workspace:manage`, which the current role model grants
to workspace owners. A read-only key can inspect runs and logs but cannot cancel
a run. Revoked and expired keys are rejected by the MCP HTTP authentication
middleware.

## Tools and arguments

Every tool takes `workspace`, the Ku workspace slug, and `repo`, preferably the
linked repository's full name such as `TerraScale/ku`. `repo` also accepts the Ku
repository-link UUID. It does not accept an arbitrary upstream repository ID.
Repository names are matched without case sensitivity; an ambiguous name
requires the UUID.

Workspace slugs must be nonempty valid UTF-8, at most 100 bytes, and contain no
control characters. Workspace lookup and permission errors use fixed messages
without echoing the supplied slug or an internal exception.

| Tool | Additional arguments | Result |
| --- | --- | --- |
| `forgejo_list_action_runs` | Optional `page`, `per_page`, `status`, `ref`, `event`, `head_sha`, `workflow_id`, `run_number` | Paginated native run DTOs |
| `forgejo_get_action_run` | `run_id` | Native run DTO |
| `forgejo_list_action_jobs` | `run_id`; optional `page`, `per_page` | Paginated native job DTOs |
| `forgejo_get_action_job` | `run_id`, `job_id` | Native job DTO |
| `forgejo_get_action_job_logs` | `run_id`, `job_id`; optional `attempt` | `content`, `truncated`, `max_bytes` |
| `forgejo_cancel_action_run` | `run_id` | `{"status":"accepted"}` after Forgejo accepts cancellation |

`run_id` is the positive native Forgejo run ID, which differs from the displayed
run number. `job_id` must be positive and belong to that run. Omit `attempt` to
request the latest attempt, or supply a positive attempt number. An explicit
zero or negative attempt is rejected.

Pagination defaults to page 1, with 20 runs or 100 jobs per page. `page` must be
between 1 and 10000 and `per_page` between 1 and 100. The optional `run_number`
filter must be positive. String filters are limited to 512 bytes and reject NUL,
carriage return, and newline characters. Accepted status filters are `unknown`,
`waiting`, `queued`, `running`, `success`, `failure`, `cancelled`, `skipped`, and
`blocked`; `queued` is translated to Forgejo's native `waiting` state by the
shared service.

## Example calls

List failed runs:

```json
{
  "name": "forgejo_list_action_runs",
  "arguments": {
    "workspace": "terrascale",
    "repo": "TerraScale/ku",
    "status": "failure",
    "per_page": 20
  }
}
```

Read a specific job attempt, using IDs returned by the run and job tools:

```json
{
  "name": "forgejo_get_action_job_logs",
  "arguments": {
    "workspace": "terrascale",
    "repo": "TerraScale/ku",
    "run_id": 42,
    "job_id": 91,
    "attempt": 2
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
Forgejo origin, and verifies run/job membership before reading job metadata or
logs. Repository resolution reads stored links without invoking integration
status setup or changing automation rules. Service failures become fixed,
meaningful MCP tool errors; raw upstream errors, request URLs, response bodies,
and credentials are not returned.

Job logs require Forgejo 16 or newer and are bounded to 2 MiB. Inspect
`truncated` before treating returned content as a complete log. The same native
log endpoint supports the latest or selected attempt without requiring an
optional job-detail API. Cancellation is allowed only for runs whose native
state permits it, currently `waiting`, `running`, or `blocked`.

The available tools cover listing, inspection, logs, and cancellation. Reruns
and step APIs are not exposed.
