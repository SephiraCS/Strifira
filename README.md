<div align="center">
  <img src="assets/logo_primary.svg" alt="Ku" width="250">
  <p><strong>快乐 (kuàilè) · happiness, joy</strong></p>
	<p><strong>The issue tracker with no paid tier.</strong></p>
	<p>Keyboard-driven, self-hosted, and available in one public Apache 2.0 repository.</p>

[Report Bug](https://github.com/carbogninalberto/kuayle/issues/new?labels=bug) · [Request Feature](https://github.com/carbogninalberto/kuayle/issues/new?labels=enhancement)

</div>

<br />

[![Ku Screenshot](assets/product-screenshot.png)](https://github.com/carbogninalberto/kuayle)

## 🌍 Contributors

<!-- contributors:start -->
<a href="https://github.com/carbogninalberto"><img src="https://avatars.githubusercontent.com/u/17618905?v=4&amp;s=64" width="64" height="64" alt="@carbogninalberto" title="@carbogninalberto"></a>
<a href="https://github.com/uiYzzi"><img src="https://avatars.githubusercontent.com/u/40852301?v=4&amp;s=64" width="64" height="64" alt="@uiYzzi" title="@uiYzzi"></a>
<a href="https://github.com/manelpb"><img src="https://avatars.githubusercontent.com/u/1784206?v=4&amp;s=64" width="64" height="64" alt="@manelpb" title="@manelpb"></a>
<!-- contributors:end -->

## 🚦 Current Implementation State

Ku's published releases are runnable MVPs, not a mature enterprise platform. This development branch also contains unreleased work described below. The repository includes the Go API, SvelteKit frontend, database migrations, development tooling, and reference self-hosting configuration.

| Area             | State                                                                                                                                                                                                                                                     |
| ---------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Core tracker** | Available: auth, workspaces, RBAC, teams, custom statuses, issues, multiple assignees, labels, comments, history, sub-issues, relations, triage, templates, favorites, saved views, notifications, public sharing, uploads, docs (collaborative wiki), and WebSocket events. |
| **Planning**     | Implemented: cycles with burndown/velocity charts, project management with Gantt view, and full cycle/project UI.                                                                                                                                         |
| **Integrations** | Available: workspace webhooks and GitHub with repository linking, branch/commit/PR activity, CI-green detection, keyword closing, configurable status transitions, and an Actions view for workflow runs, jobs, logs, links to rerun in GitHub, and cancellation. |
| **Analytics**    | Workspace and team overviews, event-based burn-up trends, and configurable issue insights based on current issue data and stored lifecycle timestamps.                                                                                                                                              |
| **Dev Machines** | Unreleased development-branch functionality with a multi-container runtime, authenticated routing, agent providers, collector, and UI. The production contract requires always-on availability; existing deployment gates remain migration debt. See [`TECHNICAL.md`](TECHNICAL.md). |
| **Self-hosting** | Reference Docker Compose stack with Caddy, PostgreSQL, Redis, backend, frontend, an update script, and dedicated config in [`selfhosting/`](selfhosting/).                                                                                                       |

## Production operating contract

Features and all regions are always on, with fixed production settings and no
operator feature activation or tuning. NetBird membership automatically admits
nodes; each node discovers every region and infers its own region. OCI Vault
creates and delivers first-party production secrets and retrieves vendor keys
stored once. Fakes, demo stores and skip-auth belong only in test code and
binaries. See [repository policy](AGENTS.md).

This is the required contract, not a statement that this branch implements all
of it. Existing environment readers, Compose profiles, demo paths and manual
secret/provider setup are migration inventory for later COS work. TS-795 changes
documentation and policy only. The examples below describe current development
and self-hosting prerequisites. They are not a completed zero-configuration
production deployment. Authentication, customer consent, repository access,
credential isolation, SSRF checks and recovery safeguards remain required.

## Why Ku?

Ku started with a narrow requirement: keep a fast, keyboard-oriented issue workflow while adding multiple assignees, self-hosting, and complete source access.

The distribution model is deliberately simple:

- one public repository under Apache 2.0;
- no paid tier or enterprise edition;
- no feature gate or license key;
- no per-user software fee;
- infrastructure, backups, monitoring, and updates remain the operator's responsibility.

The product is intentionally smaller than broad project-management suites. It covers issues, cycles, projects, docs, saved views, analytics, GitHub automation, public sharing, real-time events, and portable per-workspace transfer. It does not currently include enterprise identity or modules.

## ✨ Features

|     | Feature                | Description                                                                      |
| --- | ---------------------- | -------------------------------------------------------------------------------- |
| 🏢  | **Workspaces**         | Multi-tenant with role-based access (owner, admin, member, guest)                |
| 👥  | **Teams**              | Custom workflows, each team gets its own statuses and triage settings            |
| 📋  | **Issues**             | Priority, due dates, sub-issues, multi-assignee, labels, comments, audit history |
| 🔗  | **Issue Relations**    | Blocking/blocked, duplicate, and related issue links                             |
| 🔄  | **Cycles**             | Sprint planning with burndown/velocity charts and time-boxed iterations          |
| 📁  | **Projects**           | Cross-team work grouped under a single umbrella with Gantt view                  |
| 🏷️  | **Labels**             | Hierarchical, workspace-scoped, with soft delete and default labels on creation  |
| 👁️  | **Views**              | Saved views with personal/workspace/team scoping, drag-and-drop reorder          |
| 🔔  | **Notifications**      | Inbox with snooze, read status, and archive                                      |
| 📄  | **Docs**               | Notion/Obsidian-style wiki: page tree, real-time collaborative editing with live cursors, `[[wiki links]]` with backlinks and a graph view, tags, full-text search, favorites, templates, daily notes, version history, comments, Markdown import/export, and read-only public share links |
| 📁  | **File Management**    | Workspace file browser with uploads, authenticated links, and expiring public share links for single files |
| 🔗  | **Webhooks**           | Plug into external services and integrations                                     |
| ⚡  | **Real-time**          | Workspace WebSocket events for issues, comments, cycles, views, GitHub, and presence |
| 🖥️  | **Dev Machines**       | Unreleased multi-container coding environments with agents, browser access, authenticated routing, and work tracking |
| 🔧  | **GitHub**             | Link repos, match issue IDs in development activity, react to CI checks, and apply status rules      |
| 🤖  | **AI assistance**      | Generate issue descriptions, improve descriptions and comments, and review suggestions without losing attachments or intervening edits |
| ▶️  | **GitHub Actions**     | Browse linked repository workflows, inspect jobs and bounded logs, rerun in GitHub, and cancel active runs |
| 📊  | **Analytics**          | Workspace/team overview, burn-up, and configurable insights                     |
| 🔗  | **Public Sharing**     | Token-based read-only links for issues and views                                 |
| 📦  | **Asset Management**   | File uploads, signed URLs for prompt images, S3-compatible storage               |
| 💾  | **Workspace Transfer** | Owner/admin export and new-instance import with assets and ID remapping           |
| ⌨️  | **Command Palette**    | Global search with highlighting, keyboard shortcuts, and quick actions           |
| 🎨  | **Rich Text Editor**   | Tiptap-based with code blocks, slash commands, mentions, task lists              |
| 🚀  | **Release Changelog**  | Multi-release changelog modal with markdown rendering from static manifest       |

Completed issues can be archived from an issue's actions and restored from **Archived issues** in the workspace sidebar. **Settings → General** enables automatic archiving 30 days after completion by default; workspace owners can turn it off. Restored issues are exempt from automatic archiving until their status changes.

The production contract requires automatic provider discovery and retrieval of vendor keys stored once in OCI Vault. The current UI still requires workspace owners to save an OpenAI-compatible endpoint, model, and API key in **Settings → AI**; that manual setup is migration debt, not automatic Vault pickup. **Test connection** makes a short request with the saved configuration; save connection changes before testing. Issue descriptions and comments expose **Improve with AI**, and new issues can generate a description from their title. If text changes while a request is running, or the selection contains images, attachments, or mentions, Ku presents a suggestion to review and insert below the existing content. Applied changes can be undone in the editor.

**Actions** in the workspace sidebar shows workflows from linked GitHub repositories. Filter runs by status and branch, select a run to inspect jobs, and open a job's logs. Workspace owners can cancel active runs in Ku. For completed runs, **Rerun in GitHub** opens the native run page: sign in with your normal GitHub account and use its rerun controls. Your GitHub repository permissions apply. **View workflow steps in GitHub** opens that page for individual step details. Job logs of completed runs expire after GitHub's retention period.

## 🤖 Dev Machines (Agentic Coding)

Dev Machines are an unreleased multi-container subsystem for self-hosted Ku. The production contract requires always-on availability; current deployment gates and manual provisioning remain migration debt. Machines can be created generically, then attach issue worktrees when work starts. Every machine has collector and egress services; it may also include a shared code-server/native-terminal developer container and a browser, while agent containers are created on demand for runs. PostgreSQL-backed operations provide durable reconciliation and authenticated wildcard routing.

> See [`TECHNICAL.md`](TECHNICAL.md) for the full specification, architecture diagrams, schema, and API reference.

### Architecture

```
You (browser)
  │
  ├─ 0123456789abcdef0123.ku-machines.example.net         → code-server
  ├─ 0123456789abcdef0123-terminal.ku-machines.example.net → native xterm over ttyd.v1
  └─ 0123456789abcdef0123-browser.ku-machines.example.net → Chrome via KasmVNC (in-browser web navigation)
        │
        └── All routed through Machine Gateway auth — no public ports, no port management
```

The implementation:

1. **Orchestrate cooperating containers** per machine. Collector and egress services are always represented. The optional developer service runs code-server on `8080` and ttyd on `7681` in one container (`/workspace`, `HOME`, tools, processes, and tmux); browser and on-demand agent services run separately on the isolated bridge network. The Ku UI renders `@xterm/xterm` natively and speaks `ttyd.v1`; it does not expose ttyd's web page.
2. **Support multiple agent providers** — Claude Code, OpenCode, Codex, or admin-configured generic CLIs. The shared developer image includes pinned OpenCode, Claude Code, and Codex CLIs for direct interactive terminal use. Ku's dashboard launches bounded autonomous runs in separately pinned provider images and normalizes their results.
3. **Assign random subdomains** through a separate registrable wildcard domain (`*.ku-machines.example.net`) with launch-ticket auth and host-restricted machine session cookies. The machine domain must be a completely separate registrable domain to prevent cookie leakage between the main application and machine workloads.
4. **Authenticate** through a dedicated unprivileged Machine Gateway; the privileged Machine Manager is the only Dev Machines runtime component with Docker socket access. The current system updater also mounts the socket and must retain the same host-administrator boundary.
5. **Prepare issue worktrees** from issue, project, team, then workspace repository/environment defaults. One machine has affinity to one repository and can hold several independent issue worktrees from that repository; use a separate machine for another repository/environment or concurrent conflicting work.
6. **Record best-effort activity** from filesystem, shell, root-checkout Git, browser, agent, gateway, and lifecycle telemetry and attach it to project work

### Modes

| Mode              | Who                             | What happens                                                              |
| ----------------- | ------------------------------- | ------------------------------------------------------------------------- |
| **Agent-only**    | A user starts a bounded run      | Agent works autonomously, records a normalized result, and optionally pushes a branch or opens a pull request with the workspace GitHub token |
| **Human + Agent** | Developer opens an issue workspace | Uses code-server or the dedicated persistent terminal while launching and monitoring bounded agent runs from Ku |

### Current machine inputs

The following describes existing APIs and workspace policy. Deployment switches,
provider enable fields and adjustable runtime values are migration inventory for
later COS work; TS-795 does not change them. Always-on availability preserves
user-requested machine creation, repository selection, authorization and safe
resource boundaries.


Machines are created without requiring a repository, branch, issue, project, or manual TTL. Friendly random names are case-insensitively unique per creator within a workspace and have an availability API. Opening an issue resolves its repository and environment using issue, project, team, then workspace defaults and creates an isolated Git worktree. Workspace policies control concurrency, maximum hard runtime, a default 240-minute idle pause, per-machine disk size, providers, repositories, and custom CLI access. A per-machine **Keep running** switch bypasses idle pause; no automatic deletion is performed. Owners and admins can create a writable Environment Builder, pause or stop it, and save selected tooling/home customization as an immutable local OCI Development Environment image.

| Size   | CPU     | Memory | PID basis | Disk (hard quota) |
| ------ | ------- | ------ | --------- | ----------------- |
| Small  | 2 cores | 4 GB   | 512       | 20 GB             |
| Medium | 4 cores | 8 GB   | 512       | 50 GB             |
| Large  | 8 cores | 16 GB  | 512       | 100 GB            |

Per-container PID limits are derived from the profile basis; 512 is not one aggregate machine-wide process limit. Per-machine workspace-volume limits use Docker local-volume project quotas. Self-hosted Dev Machines require Docker's data root on XFS mounted with `pquota` or `prjquota`; the manager fails its startup quota probe instead of running an unbounded workspace when the host does not support this boundary.

## 🛠️ Tech Stack

Here's what Ku runs on and what each piece does:

| Layer             | Tech                                 | Role                                                                  |
| ----------------- | ------------------------------------ | --------------------------------------------------------------------- |
| **API**           | Go + Echo                            | High-performance HTTP server with middleware, routing, JWT auth       |
| **Database**      | PostgreSQL 17                        | Primary data store, raw SQL via sqlx/pgx, no ORM                      |
| **Redis**         | Redis 7                              | Required configuration; reserved for future cache and job use         |
| **Frontend**      | SvelteKit + Svelte 5                 | SPA with TypeScript, runes-based reactivity, static adapter           |
| **UI**            | Tailwind CSS + shadcn-svelte         | Utility-first styling with accessible component primitives            |
| **Editor**        | Tiptap v3 + Yjs                      | Rich text with code blocks, mentions, slash commands, task lists      |
| **Real-time**     | WebSocket (nhooyr.io)                | Workspace events, issue presence, targeted notifications, and per-document collaborative editing |
| **Storage**       | Local FS or S3-compatible            | AWS S3, Cloudflare R2, MinIO, SeaweedFS                               |
| **Reverse Proxy** | Caddy                                | Production HTTPS and routing                                          |
| **Dev Machines**  | Docker + Caddy + Go gateway/manager + code-server + ttyd/xterm + Chrome/KasmVNC | Unreleased multi-container development environments with issue worktrees and provider abstraction |
| **Infra**         | Docker + Docker Compose              | Local development and reference self-hosting stack                    |

## 🚀 Quick Start

### Docker

```sh
cp .env.example .env
make docker-up
```

App at `http://localhost:5173` · API at `http://localhost:8080`

### Local Dev

```sh
cp .env.example .env
make reset-dev
make seed
make dev-start-services
make dev
```

`make dev` runs the Go backend on `localhost:8080` and Vite on
`localhost:5173` directly on the host for hot reload. The root Compose stack is
for supporting services only during local development: Postgres, Redis, the Dev
Machine gateway/manager, runtime image builds, and the Caddy wildcard TLS proxy
for `https://*.machines.localhost`. The dev proxy binds `127.0.0.1:80` and
`127.0.0.1:443`; stop the selfhosting stack first if it is using those ports.
The root gateway container is named `ku-dev-machine-gateway` to avoid the
selfhosting `ku-machine-gateway` name.

To run another local instance, set `PORT=8081`, `KU_DEV_API_URL=http://localhost:8081`,
`KU_DEV_UI_PORT=5174`, and `FRONTEND_URL=http://localhost:5174` in its root `.env`
before `make dev`. Vite uses the same API target for HTTP, WebSockets, and uploads.
The `KU_DEV_` settings can also be supplied through the process environment.

### 📖 Commands

| Command                | What it does                          |
| ---------------------- | ------------------------------------- |
| `make dev`             | Run backend + frontend (with migrate) |
| `make dev-start-services` | Start Postgres, Redis, and Dev Machine infrastructure |
| `make dev-reset`       | Reset, seed, start infrastructure, then run host dev |
| `make dev-backend`     | Backend only                          |
| `make dev-frontend`    | Frontend only                         |
| `make dev-full`        | Backend + frontend + smee proxy       |
| `make dev-smee`        | Start webhook proxy (smee.io)         |
| `make migrate-up`      | Apply migrations                      |
| `make migrate-down`    | Roll back migrations                  |
| `make seed`            | Seed the database                     |
| `make reset-dev`       | Reset dev database                    |
| `make test`            | Both test suites (fast, then slow)    |
| `make test-fast`       | Fast suite: critical paths; gates every deploy |
| `make test-slow`       | Slow suite: everything else; never blocks a deploy |
| `make check-backend`   | gofmt and go vet                      |
| `make lint`            | Lint everything                       |
| `make docker-up`       | Start all (Docker)                    |
| `make docker-down`     | Stop all (Docker)                     |
| `make scan`            | Security scan backend + frontend      |

## 🏠 Self-Hosting

Ku is designed to be self-hosted. The reference stack in [`selfhosting/`](selfhosting/) includes Caddy, PostgreSQL, Redis, the backend, the frontend, and an update script. Review secrets, backups, monitoring, and host security before production use.

### Portable workspace transfer

Workspace owners and admins can download a versioned `.ku.zip` archive from **Settings → General → Workspace transfer**. An authenticated user with no workspace can choose **Import workspace** during workspace setup; an owner or admin can also start an import from the transfer settings. Import always creates a separate workspace and never overwrites an existing one.

The format 2 archive contains a logical `manifest.json`, `data.json`, and the workspace's uploaded asset bytes. It preserves teams, statuses, labels, projects, cycles, issues and their relationships/history, templates, views, favorites, shared links, workspace notifications, integration metadata, AI prompts, and safe Dev Machine workspace policy/scope settings. Historical issue identifiers retain their exact text and ownership so GitHub references continue to resolve after team renames and workspace transfer. Database and asset IDs are regenerated and references—including protected asset URLs—are remapped during import. Format 1 archives are no longer supported; export the workspace again with the current version before importing.

Workspace transfer deliberately excludes password hashes, refresh tokens, personal preferences, webhook secrets, GitHub credentials and access tokens, AI API keys, Dev Machine credentials, active machines, Docker volumes/networks, environment images, logs, sessions, and runtime artifacts. Imported webhooks, shared links, GitHub repositories/automation, and Dev Machine policy are disabled or rotated where appropriate; GitHub, AI, webhooks, and Development Environments must be reconfigured on the target instance.

Users are matched to existing target-instance accounts by normalized email. The importing user becomes the new owner; all other referenced users must register on the target instance before import. The preview reports any missing emails without creating partial data.

This feature transfers one logical workspace. It is not a replacement for an operator backup: disaster recovery and whole-instance migration still require PostgreSQL backups, object-storage backups, deployment configuration/secrets, and any required local Docker volumes or Development Environment OCI images.

### Prerequisites

- A Linux server with Docker + Docker Compose v2
- A domain pointing to your server (for HTTPS via Let's Encrypt HTTP-01)
- Dev Machines require a second registrable domain with a wildcard DNS record and either a custom Caddy DNS-01 build or an imported wildcard TLS certificate

### 1. Prepare the current reference stack

```sh
git clone https://github.com/carbogninalberto/kuayle.git
cd ku/selfhosting
cp .env.example .env
```

The current stack still consumes environment inputs and does not provision its
production secrets automatically. Domain ownership, valid HTTPS and protected
credentials remain prerequisites. Under the operating contract, OCI Vault must
create and deliver first-party secrets and retrieve vendor keys stored once;
this branch's delivery gap must be resolved before calling the stack a
zero-configuration production deployment. Never deploy example placeholders or
commit generated credentials.

`FRONTEND_URL` must be the exact public browser origin, including scheme and any non-default port. Dev Machine native terminal WebSockets use this value for strict `Origin` validation.

### 2. Launch

```sh
docker compose up --build -d
```

This starts 5 containers: Caddy (auto TLS), PostgreSQL, Redis, backend API, and frontend.

### 3. Run migrations and seed

```sh
docker compose exec backend /app/server migrate up
docker compose exec backend /app/server seed
```

Caddy handles HTTPS termination and proxies `/api/*` to the backend and all other requests to the frontend, so one public origin is enough.

### Dev Machines readiness

Dev Machines must be available without a production enable step. The current
reference Compose stack still has profile and flag gates; removing those gates
and implementing automatic provisioning belongs to later COS work. TS-795 does
not change their values or claim that a five-service launch starts the complete
control plane.

Deployment must satisfy these safety prerequisites:

- A separate registrable machine domain, wildcard DNS and valid production
  wildcard TLS; Caddy's local `tls internal` configuration is unsuitable for a
  public production domain, and stock Caddy cannot issue wildcard certificates
  through HTTP-01.
- An exact public `FRONTEND_URL` for terminal WebSocket origin checks and a
  reachable HTTPS collector ingestion URL.
- Independent machine-encryption and restricted gateway credentials, created
  and delivered by OCI Vault under the contract; the current stack still
  requires these inputs and does not implement that delivery.
- Trusted runtime images, completed migrations and restricted gateway grants
  before control-plane startup.
- Docker data-root hard quotas and correct socket ownership, discovered rather
  than exposed as production tuning; socket access remains host-privileged.

The API rejects a production machine domain that shares the main application's registrable domain. `machines.localhost` is supported only for local development. The gateway rejects parent-domain and reserved session cookies from workloads. Browser-cookie launches for code-server and browser require exact machine origins for mutations and service WebSockets; native terminal WebSockets use a separate one-use ticket bound to exact `FRONTEND_URL`, host, user/service, tmux session, and working directory.

For DNS-01, replace the stock `caddy:2-alpine` image with a trusted custom Caddy build containing your provider module, replace `tls internal` with a `tls` block using that DNS provider, and have OCI Vault deliver its scoped vendor credential; the current stack still requires a credential delivery integration. For operator certificates, bind-mount a host directory read-only at `/etc/caddy/certs` through a Compose override and use `tls /etc/caddy/certs/machines.pem /etc/caddy/certs/machines.key` in the wildcard block. Keep private keys outside the repository and container image.

Environment snapshot bytes are local OCI images stored in the host Docker image store, while their image references and lifecycle metadata are PostgreSQL rows. Snapshots exclude the repository workspace named volume and tmpfs secret mounts; plan backup, migration, pruning, and host disk monitoring accordingly.

The manager process runs as UID/GID 1000 with only the Docker socket's host group added. Access to that socket remains host-root-equivalent despite the non-root container UID, so never expose it to machine workloads or general application services.

### Updating

```sh
cd ku
bash selfhosting/update.sh
```

The update script pulls the latest code, rebuilds images, applies pending migrations, and recreates containers. If Dev Machines services are running, it rebuilds their control-plane and runtime images first, stops the existing gateway and manager only while migrations and restricted gateway-role grants are applied, then starts the updated control plane. A failure after the stop clears the upgrade marker, preserves logs, restarts the same previous containers, and returns the original nonzero status. The current script preserves the existing profile selection; that behavior remains migration debt relative to the always-on contract.

Instance updates require an authorized sysadmin action. The production contract
requires automatic provisioning of the updater's first-party credentials and
availability without a deployment profile toggle. The current sidecar still
requires an authorized sysadmin identity, protected credentials and an internal
endpoint; its profile-based setup remains migration debt. It must remain
internal-only and run the same reviewed update flow. Back up application data,
assets and required environment images before updating.

### Storage contract and current prerequisites

Production storage uses the fixed service setting and automatically delivered
credentials. Vendor-issued storage keys are stored once in OCI Vault. The
current reference stack still selects local or S3-compatible storage through
environment inputs; those backend-selection and tuning inputs are inventory
for later COS work. This documentation change does not migrate uploaded data
or implement automatic discovery. Verify access permissions, public URL
behavior and signed downloads before accepting a storage deployment.

### GitHub integration

Ku's current repository integration is GitHub (github.com and GitHub
Enterprise Server) over personal access tokens. See the
[GitHub integration contract](BE/docs/github-integration.md) for current APIs,
permissions and signed webhook handling.

The production contract retrieves a vendor token stored once in OCI Vault;
automatic pickup is not implemented by the current per-workspace/global
connection paths. Repository linking still requires owner authorization and
appropriate GitHub permissions. Automatic feature availability does not grant
access to repositories or authorize pushes, pull requests or status changes.

## 📂 Project Structure

```
ku/
├── BE/                     # Backend (Go)
│   ├── cmd/server/         # Entrypoint (server, migrate, seed)
│   └── internal/
│       ├── config/         # Configuration
│       ├── domain/         # Domain models
│       ├── dto/            # Data transfer objects
│       ├── handler/        # HTTP handlers
│       ├── service/        # Business logic
│       ├── repository/     # Data access (raw SQL)
│       ├── middleware/     # Auth and request middleware
│       └── realtime/       # WebSocket support
├── UI/                     # Frontend (SvelteKit)
│   └── src/
│       ├── routes/         # Pages
│       └── lib/
│           ├── api/        # API client
│           ├── components/ # UI components
│           ├── features/   # Feature modules
│           ├── types/      # TypeScript types
│           └── utils/      # Utilities
├── WEB/                    # Public marketing site (SvelteKit, statically generated)
├── selfhosting/            # Production Docker Compose + Caddy config
│   ├── docker-compose.yml
│   ├── Caddyfile
│   ├── update.sh
│   └── .env.example
├── scripts/                # Dev and maintenance scripts
├── docker-compose.yml      # Dev Docker Compose
├── Makefile
├── TECHNICAL.md            # Dev Machines specification
└── .env.example
```

## 🤝 Contributing

Pull requests are welcome. Keep changes focused, explain the behavior being changed, and include relevant validation.

1. Fork the repo
2. Create your branch (`git checkout -b feature/cool-thing`)
3. Commit your changes
4. Push and open a PR

### CI and deployment

Every push to `main` runs CI: the zero-configuration guard, svelte-check for UI and WEB, the fast backend suite (PostgreSQL and Redis as service containers), and the fast browser suite in three shards. The build job pushes the commit's image to Harbor as `registry.mariogk.com/homelab/ku:<sha12>`. The homelab-deployer daemon on alpha (a separate repository) rolls the alpha instance itself once every check run on the commit is green, health-gates the new instance with rollback, and posts the `deploy/alpha` commit status. The Release job waits for that status, then bumps the version (`UI/package.json` and the OpenAPI `info.version`) in a `[skip ci]` commit, tags `vX.Y.Z`, creates the GitHub release, and publishes the release-commit image to Harbor as `homelab/ku:<release-sha12>` and `homelab/ku:vX.Y.Z` so the deployer rolls alpha to the release commit. Pull requests run the checks and tests only; versions are never edited by hand.

Pull requests run only the CI jobs their changes touch (a `changes` job uses `dorny/paths-filter`): `BE/`, `devmachine/` and the `Makefile` run the backend suite; `UI/` and `WEB/` run svelte-check; `UI/` and the `Makefile` run the browser shards. The guard always runs. The `test` job aggregates them and is the check to wait for: it passes when every job passed or was skipped as unaffected, and fails when any failed or was cancelled. Pushes to `main` and manual runs always run everything. The security scan runs on pull requests that touch `BE/` or the UI npm dependencies; image scans run when an image's inputs change; both also run nightly.

The slow suite (`slow.yml`) runs after pushes to `main` (docs-only pushes excepted), nightly, on demand, and on pull requests labelled `full-ci`. Add the label to run it on a pull request; it re-runs on every new push while the label stays.

## 📄 License

Apache 2.0, see [`LICENSE`](LICENSE).

## 📬 Contact

Alberto Carbognin, [@carbogninalberto](https://github.com/carbogninalberto)

---

> **Project note:** Ku has been developed with AI-assisted tooling under human direction and review. Published releases remain MVPs; evaluate and test them against your requirements before production use.

<!-- MARKDOWN LINKS & IMAGES -->

[contributors-shield]: https://img.shields.io/github/contributors/carbogninalberto/kuayle.svg?style=for-the-badge
[forks-shield]: https://img.shields.io/github/forks/carbogninalberto/kuayle.svg?style=for-the-badge
[forks-url]: https://github.com/carbogninalberto/kuayle/network/members
[stars-shield]: https://img.shields.io/github/stars/carbogninalberto/kuayle.svg?style=for-the-badge
[stars-url]: https://github.com/carbogninalberto/kuayle/stargazers
[issues-shield]: https://img.shields.io/github/issues/carbogninalberto/kuayle.svg?style=for-the-badge
[issues-url]: https://github.com/carbogninalberto/kuayle/issues
[license-shield]: https://img.shields.io/github/license/carbogninalberto/kuayle.svg?style=for-the-badge
[license-url]: https://github.com/carbogninalberto/kuayle/blob/main/LICENSE
[product-screenshot]: assets/screenshot.png
