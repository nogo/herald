# Architecture

## Core Idea

Herald is a git-to-production bridge for self-hosters. You declare what runs on your server in one YAML file. Herald handles everything between a config change and a live site with TLS.

The user thinks in terms of "things with domains," not Docker projects, compose files, or container orchestration.

## Domain Model

One entity: the **stack**. A stack is anything herald deploys — your app from GitHub, a Nextcloud instance from your IaC repo, a preview environment for a feature branch. The only difference is where the source lives and what triggers a deploy.

```yaml
stacks:
  myapp:
    repo: myorg/myapp            # source: GitHub
    branch: main
    domain: myapp.example.com

  nextcloud:
    path: stacks/nextcloud       # source: IaC repo directory
    domain: cloud.example.com
```

`repo:` and `path:` are mutually exclusive. That field is the only fork in the deploy pipeline. Everything downstream — secrets, env, override generation, compose up, caddy labels — is one code path.

## Deploy Pipeline

Every deploy, regardless of source, follows the same stages:

```
resolve source → preflight → secrets → env + override → compose up
```

1. **Resolve source** — Clone/fetch from GitHub (`repo:`) or symlink from IaC repo (`path:`). The only stage that differs by source type.
2. **Preflight** — Verify required secrets exist. Fail fast with actionable error.
3. **Secrets** — Resolve secret refs from the age-encrypted store. Auto-generate any with `generate:` set. Split into env vars and Docker secret files.
4. **Env + Override** — Merge config file base with resolved secrets into `.env`. Generate `compose.override.yml` with caddy labels, network isolation, secret mounts, and env file references.
5. **Compose up** — `docker compose up -d --build --remove-orphans`. Herald's job ends here.

Preview environments are the same pipeline with an ephemeral lifecycle: deploy on branch push, teardown on merge/delete.

Post-deploy hooks (`update:`) run after compose up for stacks that need custom migration or restart logic.

## Domain Map

### Core — what only herald knows

**`config`** — The domain model. Defines stacks, secrets, previews, server identity. Innermost package, no outward imports. This is the language of herald.

**`deployer`** — The single deploy orchestrator. Owns the pipeline above. Source resolution is a strategy fork within one `Deploy()` function, not separate packages. Handles deploy serialization (per-stack locking, drop excess).

**`webhook`** — Translates GitHub events into deploy intents. Matches repo+branch+tag to stacks, routes preview events, verifies HMAC signatures. Herald-specific matching logic that no external tool provides.

**`preview`** — Preview environment lifecycle management. State tracking, subdomain derivation, cleanup of stale environments. Uses deployer for the actual deploy — does not duplicate the pipeline.

### Supporting — enables core, swappable

**`compose`** — Override generation and Docker Compose types (`Override`, `ServiceOverride`, `SecretFileDef`). Knows the compose file format. Does not know about stacks or deploy policy. Swappable if herald ever supports another container runtime.

**`caddy`** — Reverse proxy lifecycle. Manages the caddy-docker-proxy container. Swappable for Traefik or nginx without changing the deploy model.

**`secrets`** — Age-encrypted key-value store with auto-generation. Handles encrypt/decrypt, atomic writes, file locking. Does not know about deploy pipelines.

**`github`** — GitHub API client for webhook CRUD and OAuth device flow. Isolated from core domain.

**`init`** — Bootstrap orchestration. Clones IaC repo, sets up secrets, starts caddy, registers webhooks. Procedural, runs once.

**`status`** / **`web`** — Read-side observability. Collects state from Docker, git, and config into a unified view. No write-side effects.

### Generic — could be libraries

**`runner`** — Command execution with stream capture.

**`git`** — Authenticated git operations. Credential helper injection. No herald concepts.

**`ui`** — Step-based progress output (TTY and nop implementations).

**`systemd`** — Unit file generation.

## Dependency Graph

```
                    ┌─────────┐
                    │ config  │
                    └────┬────┘
                         │
          ┌──────────────┼──────────────┐
          │              │              │
     ┌────▼────┐   ┌─────▼─────┐  ┌────▼────┐
     │ secrets │   │  compose  │  │   git   │
     └────┬────┘   └─────┬─────┘  └────┬────┘
          │              │              │
     ┌────▼──────────────▼──────────────▼────┐
     │             deployer                  │
     └────┬──────────────┬──────────────┬────┘
          │              │              │
     ┌────▼────┐   ┌─────▼─────┐  ┌────▼────┐
     │  caddy  │   │  runner   │  │   ui    │
     └─────────┘   └───────────┘  └─────────┘

     preview uses deployer (does not duplicate it)
     webhook dispatches to deployer and preview
```

One orchestrator. Supporting packages are leaves. No circular dependencies. Preview delegates to deployer instead of reimplementing the pipeline.

## Command Surface

Commands exist only when herald adds value beyond `docker compose`:

| Command | Value |
|---------|-------|
| `herald serve` | Webhook daemon — the core product |
| `herald deploy <stack>` | Full pipeline: source sync + secrets + override + compose up |
| `herald deploy --all` | Reproduce entire server from config |
| `herald down <stack>` | Resolve project name + teardown (convenience) |
| `herald status` | Cross-stack view impossible without herald's model |
| `herald secret set/list` | Encrypted store |
| `herald preview list/remove/cleanup` | Ephemeral lifecycle management |
| `herald init/install/auth` | One-time setup |
| `herald sync` | Pull IaC repo + reconcile config + sync webhooks |

Not included: `logs`, `exec`, or anything that's just `docker compose <cmd>` with a project name lookup. Herald's naming convention (`herald-<name>`) is documented in `status` output.

## File Layout

```
/etc/herald/                  herald data
  age.key                     master encryption key (back this up)
  secrets.age                 encrypted secrets store
  repo/                       cloned IaC repo
    config.yml                server configuration
    stacks/<name>/            compose files, update scripts, config

/opt/deploy/<name>/           per-stack runtime state
  repo/                       cloned GitHub repo, or symlink to IaC path
  .env                        generated: config base merged with secrets
  secrets/<name>              docker secret files
  compose.override.yml        generated: caddy labels, networks, secret mounts
  deployed_ref                last deployed ref (repo-sourced stacks only)

/opt/deploy/previews/
  <stack>-<branch>/           ephemeral preview environments (same structure)

/opt/deploy/caddy/            caddy-docker-proxy
```

Flat deploy directory. No `apps/` vs `services/` split — all stacks are peers.
