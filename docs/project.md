# Project

## Goal

One VPS. One server repo. GitHub pushes make it live.

Herald turns a single VPS into a GitHub-driven Docker Compose deploy target. App repo pushes deploy stacks. Server repo pushes update deployment wiring. Herald handles the glue between GitHub, Docker Compose, Caddy TLS, encrypted secrets, preview environments, and operational status.

## Outcome

A single-binary daemon that runs on one VPS and reacts to GitHub events. Push app code, and the matching stack deploys. Push server config, and Herald pulls the server repo, reloads config, keeps webhooks and Caddy wiring current, and deploys path-sourced stacks that opted into auto-deploy. Secrets are encrypted. TLS is automatic. The server is reproducible from one repo.

Everything herald manages is a **stack** — a compose project with a domain, secrets, and a source. Whether it's your app from GitHub or Nextcloud from your IaC repo, it's the same concept, same config, same deploy command.

## Value Proposition

Herald's value is the wiring you'd never want to do by hand:

1. **Git-to-production pipeline** — Webhook receives push, pulls code, builds, deploys. No CI/CD service needed. No SSH-based deploy scripts.

2. **Server repo automation** — The server repo is also webhook-driven. Changing `config.yml` should not require SSHing into the box to refresh deployment wiring.

3. **Config-as-code without Terraform semantics** — One `config.yml` per server, versioned in git. No database, no web UI state, no provider graph, no remote state. The repo describes what should run on this VPS; Herald operates it.

4. **Secrets management** — Age-encrypted at rest, auto-generated on first deploy, resolved at deploy time into env files and Docker secrets. No external vault needed for single-server setups.

5. **Automatic TLS** — Caddy provisions certificates for every domain declared in config. Adding a domain is one line in YAML.

6. **Preview environments** — Push a branch, get an isolated deployment with its own subdomain. Merge or close the PR, it tears down. Zero config beyond enabling it.

7. **Private operations, public availability** — Detailed status and diagnosis are for the admin. Public badges/status pages should expose only whether opted-in services are up, degraded, or down.

8. **Same repo, N deploys** — One GitHub repo can be deployed multiple times under different names, branches, tags, or domains. No other self-hosted tool does this cleanly.

Herald is **not** a Docker Compose wrapper. If the user is running `herald` commands that they could run with `docker compose` directly, herald is failing at its job. Every command should do something the user can't trivially do themselves.

## Constraints

### Single binary, no database
Herald stores state in files and git repos. No PostgreSQL, no Redis, no SQLite. Config lives in a git repo. Secrets live in an age-encrypted file. Runtime state is the filesystem and Docker itself.

### One VPS
Herald manages one VPS. Multi-server orchestration is out of scope. If you need that, use Kubernetes or Nomad.

### GitHub-native
GitHub is the event source. Webhook integration is GitHub-specific (HMAC-SHA256 signatures, GitHub event payloads, GitHub OAuth device flow). Supporting other forges (Gitea, GitLab) is possible later, but not until the GitHub-native product is excellent.

### Docker Compose as runtime
Herald generates compose overrides and runs `docker compose up`. It does not manage containers directly. This means herald inherits compose's capabilities and limitations.

### Caddy as reverse proxy
TLS and routing are delegated to caddy-docker-proxy via container labels. Herald generates the labels; Caddy does the work.

## Non-Goals

- **Web UI for configuration.** Config is YAML in git. That's the interface.
- **App marketplace or templates.** Herald deploys your compose files, not curated images.
- **Multi-server coordination.** One herald per server.
- **Replacing CI/CD.** Herald builds via `docker compose build`. If you need test pipelines, use GitHub Actions and deploy on success.
- **Wrapping Docker Compose commands.** Commands like `logs` and `exec` that add no herald-specific logic should not exist. Teach the naming convention (`herald-<name>`) instead.
- **Terraform/OpenTofu-style planning or state.** Herald operates one server from a repo. It does not provide providers, resource graphs, imports, or remote state.
- **Generic monitoring platform.** Herald may expose simple public availability for opted-in stacks, but detailed monitoring, alerting, and incident management are out of scope.

## Design Principles

### One entity type: the stack
Everything herald manages is a stack. A stack has a domain, secrets, and a source — either a GitHub repo (`repo:`) or a directory in the IaC repo (`path:`). That's the only fork. One config section (`stacks:`), one deploy command (`herald deploy`), one code path. No "apps vs services" distinction.

### Herald owns the wiring, not the runtime
Herald's job ends after `docker compose up` succeeds. It does not monitor containers, restart crashed services, or collect logs. Docker and compose own the runtime. Herald owns the path from "config changed" to "containers running."

### Events over polling
GitHub webhooks are the primary automation path. App repo pushes deploy apps. Server repo pushes update deployment wiring. Polling is a fallback for recovery or future non-GitHub setups, not the product model.

### Public availability, private operations
Operational detail belongs behind authentication: repos, branches, deployed refs, webhook state, paths, and secret key names are admin data. Public availability should be minimal and opt-in: up, degraded, down, or unknown.

### Minimal command surface
Every command must do something only herald can do. If `docker compose` can do it with a project name, herald shouldn't wrap it. The test: "would removing this command force the user to write a script?" If no, cut it.

### Config is the source of truth
The IaC repo's `config.yml` is the single source of truth for what runs on the server. Herald derives everything else: compose overrides, env files, caddy labels, webhook registrations. Nothing is configured imperatively.
