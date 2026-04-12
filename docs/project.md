# Project

## Goal

Make self-hosting as simple as a PaaS without the PaaS. You declare what runs on your server in one YAML file. Herald handles everything between a git push and a live site with TLS.

## Outcome

A single-binary daemon that turns a VPS into a deploy target. Push code to GitHub, it's live. Add a stack to your config, deploy it with one command. Secrets are encrypted. TLS is automatic. The server is reproducible from one repo.

Everything herald manages is a **stack** — a compose project with a domain, secrets, and a source. Whether it's your app from GitHub or Nextcloud from your IaC repo, it's the same concept, same config, same deploy command.

## Value Proposition

Herald's value is the wiring you'd never want to do by hand:

1. **Git-to-production pipeline** — Webhook receives push, pulls code, builds, deploys. No CI/CD service needed. No SSH-based deploy scripts.

2. **Config-as-code** — One `config.yml` per server, versioned in git. No database, no web UI state. A new server is `herald init` + `herald deploy --all`.

3. **Secrets management** — Age-encrypted at rest, auto-generated on first deploy, resolved at deploy time into env files and Docker secrets. No external vault needed for single-server setups.

4. **Automatic TLS** — Caddy provisions certificates for every domain declared in config. Adding a domain is one line in YAML.

5. **Preview environments** — Push a branch, get an isolated deployment with its own subdomain. Merge or close the PR, it tears down. Zero config beyond enabling it.

6. **Same repo, N deploys** — One GitHub repo can be deployed multiple times under different names, branches, tags, or domains. No other self-hosted tool does this cleanly.

Herald is **not** a Docker Compose wrapper. If the user is running `herald` commands that they could run with `docker compose` directly, herald is failing at its job. Every command should do something the user can't trivially do themselves.

## Constraints

### Single binary, no database
Herald stores state in files and git repos. No PostgreSQL, no Redis, no SQLite. Config lives in a git repo. Secrets live in an age-encrypted file. Runtime state is the filesystem and Docker itself.

### Single server
Herald manages one server. Multi-server orchestration is out of scope. If you need that, use Kubernetes or Nomad.

### GitHub only
Webhook integration is GitHub-specific (HMAC-SHA256 signatures, GitHub event payloads, GitHub OAuth device flow). Supporting other forges (Gitea, GitLab) is possible but not a current goal.

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

## Design Principles

### One entity type: the stack
Everything herald manages is a stack. A stack has a domain, secrets, and a source — either a GitHub repo (`repo:`) or a directory in the IaC repo (`path:`). That's the only fork. One config section (`stacks:`), one deploy command (`herald deploy`), one code path. No "apps vs services" distinction.

### Herald owns the wiring, not the runtime
Herald's job ends after `docker compose up` succeeds. It does not monitor containers, restart crashed services, or collect logs. Docker and compose own the runtime. Herald owns the path from "config changed" to "containers running."

### Minimal command surface
Every command must do something only herald can do. If `docker compose` can do it with a project name, herald shouldn't wrap it. The test: "would removing this command force the user to write a script?" If no, cut it.

### Config is the source of truth
The IaC repo's `config.yml` is the single source of truth for what runs on the server. Herald derives everything else: compose overrides, env files, caddy labels, webhook registrations. Nothing is configured imperatively.
