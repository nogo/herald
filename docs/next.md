# Next Direction

## Problem

Herald already has the right model: one server repo, one daemon, GitHub webhooks, Docker Compose, Caddy, and encrypted secrets. The weak point is automation after setup.

Today, changing the server repo or an app repo can still require SSHing into the server and running `herald sync`. That breaks the core promise. If Herald is a daemon, the daemon should keep the deployment wiring current.

The next phase should make Herald feel less like a CLI tool an admin operates and more like a small deployment operator that runs on the server.

## Source Analysis Snapshot

Several pieces of the desired direction already exist. The next phase is mostly consolidation and daemon integration, not a greenfield rewrite.

Already implemented:

- `herald init <server-repo>` registers a webhook for the server IaC repo.
- `herald webhooks sync` and `herald sync` include the IaC repo by deriving it from the cloned server repo remote.
- `herald serve` detects pushes to the IaC repo and calls an IaC push handler.
- The IaC push handler already pulls the server repo, reloads config, and auto-deploys path stacks with `auto_deploy: true`.
- Manual `herald sync` already performs many maintenance checks: pull IaC, reload config, ensure Caddy, sync webhooks, detect undeployed/stopped/orphaned stacks, check pending commits, and warn about missing required secrets.
- `herald status` and the status web UI already report Caddy, stack, preview, and webhook state.
- `webhooks.json` already persists webhook sync status for the status read side.
- `herald preview cleanup` already removes previews for branches that no longer exist.
- `herald init` already checks prerequisites, initializes secrets, starts Caddy, registers webhooks, and clones stack repos.
- `herald install` already writes/enables/starts the systemd service by default.
- `herald deploy --all` already exists.

Important gaps:

- The fuller `herald sync` behavior lives directly in `cmd/sync.go`, so the daemon cannot reuse it cleanly.
- The IaC push handler runs only a subset of manual `herald sync`.
- No startup maintenance pass runs when `herald serve` starts.
- No `herald doctor` command exists yet.
- No broad operational report exists beyond `webhooks.json`.
- The status read side does not currently include the IaC repo in its webhook list, even though webhook sync manages it.
- The app status page assumes every stack is repo-sourced and renders repo/branch fields poorly for path-sourced stacks.
- The current status page is operational inventory, not a public-safe availability badge.

## Product Positioning

Herald should not position itself as Terraform/OpenTofu for Docker Compose. Terraform and OpenTofu provision infrastructure. Herald operates deployments on one already-provisioned server.

Herald should also not become Dokku, Coolify, or a generic Docker control panel. Those tools are platforms an admin operates. Herald should be a git-driven deployment daemon with a small, sharp command surface.

The useful distinction:

```text
Terraform/OpenTofu: create the VPS and cloud resources
Herald: keep the VPS deploying the right stacks

Dokku/Coolify: operate an app platform
Herald: make a server repo and GitHub pushes drive Docker Compose deployments
```

The strongest USP:

> Herald is a single-binary GitHub-to-Docker-Compose deployment daemon for one VPS. Install it once; after that, git pushes, webhooks, secrets, and TLS wiring are handled on the server.

Shorter:

> Push to GitHub. Herald makes the right Compose stack live with secrets and TLS.

## Core Principle

An admin should not SSH into the server for normal deployment wiring.

After setup, the normal workflow should be:

```text
edit config.yml or app code
push to GitHub
Herald reacts
Herald reports problems clearly
```

SSH should be for exceptional recovery, not routine sync.

## Priority 1: Event-Driven Sync In The Daemon

`herald sync` should not remain primarily a manual command. Its useful behavior should move into the daemon, but the default trigger should be GitHub webhooks, not polling.

Herald is GitHub-specific by design. That is a strength: the daemon can react immediately when the server repo changes instead of periodically pulling to discover changes.

Implemented daemon behavior:

- handles app repo webhooks
- handles preview deploy/teardown
- handles IaC repo push by pulling config, reloading it, and auto-deploying path stacks with `auto_deploy: true`

Implemented manual `herald sync` behavior:

- pulls the IaC repo
- reloads config
- ensures Caddy is running
- syncs GitHub webhooks
- detects undeployed/stopped/orphaned stacks
- checks pending commits
- warns about missing required secrets

The daemon should run the safe parts of `sync` automatically when relevant events happen.

Primary triggers:

- app repo push/tag webhook
- preview branch/PR webhook
- server IaC repo push webhook
- daemon startup
- explicit `herald sync`

Periodic polling may exist later as a fallback for non-GitHub setups or unreliable networks, but it should not be the default automation model.

### Startup Maintenance

This is not implemented yet.

When `herald serve` starts, it should perform a startup maintenance pass:

- pull the server IaC repo
- reload config
- ensure Caddy is running
- sync GitHub webhooks
- write webhook status for `herald status`
- check required secrets
- detect orphaned Herald compose projects
- optionally clean stale previews
- deploy path stacks with `auto_deploy: true` if their source changed

This makes reboots and service restarts self-healing.

Startup sync is allowed to pull the server repo because the daemon may have been offline while webhooks were delivered. This is recovery, not the normal change detection path.

### IaC Webhook Maintenance

This is partly implemented:

- `herald init <server-repo>` passes the server repo to webhook sync, so the IaC repo gets a GitHub webhook.
- `herald webhooks sync` and `herald sync` derive the IaC repo from the cloned server repo remote and include it in webhook reconciliation.
- `herald serve` detects pushes from `IaCRepo` and calls `OnIaCPush`.
- `OnIaCPush` currently pulls the IaC repo, reloads config, and auto-deploys path stacks with `auto_deploy: true`.

The next step is not adding the first IaC webhook. The next step is extracting the manual `herald sync` behavior into reusable code and making the IaC webhook path call it.

When the server IaC repo receives a push webhook, Herald should run the same maintenance path immediately:

- pull the server repo with fast-forward only
- reload config
- sync webhooks, because the set of repos may have changed
- ensure Caddy is running
- write an operational report
- auto-deploy changed path stacks with `auto_deploy: true`
- report stacks blocked by missing secrets

This is the core automation path. If the admin changes `config.yml` and pushes, Herald should react without SSH.

### Optional Polling Fallback

Periodic pulling should be optional and disabled by default.

Possible config:

```yaml
server:
  poll_interval: 0s  # disabled
```

If enabled, polling should be treated as a recovery mechanism, not the primary product flow.

Purpose:

- recover from missed GitHub webhooks
- support future non-GitHub forge integrations
- help servers behind unreliable network paths

If polling is enabled, it should be safe. It should not deploy every repo stack on every interval. Repo stacks should still deploy from matching GitHub push/tag events. Path stacks may deploy automatically only when `auto_deploy: true`.

### Unify Sync Logic

Avoid separate implementations of sync behavior.

Create an internal maintenance service used by:

- `herald sync`
- daemon startup
- IaC repo push handler
- optional polling fallback, if implemented

The CLI command should become a foreground way to run and print the same maintenance pass the daemon uses.

This avoids drift where `serve` handles only part of what `sync` does.

Suggested package shape:

```text
internal/maintenance
  Runner.Run(ctx, Options) Report
  Report contains caddy, webhooks, stacks, secrets, orphans, previews, errors
```

The package should not depend on Cobra or stdout. Commands and daemon handlers should render/log the returned report.

### Safe Automated Actions

Automated maintenance may do Herald-owned wiring:

- pull the server repo with fast-forward only on IaC webhook, startup recovery, manual sync, or optional polling
- reload valid config
- ensure Caddy container/network exists
- sync GitHub webhooks
- update Herald status/report files
- auto-deploy changed path stacks with `auto_deploy: true`
- clean previews when their branch is deleted, if enabled/configured

Automated maintenance should not:

- remove unknown containers automatically
- delete volumes
- deploy every repo stack just because a remote commit exists
- mutate config files
- rotate secrets
- hide failed checks

## Priority 2: `herald doctor`

Herald needs an operator-facing diagnosis command. Avoid Terraform-like vocabulary such as `plan`, `apply`, `state`, `resources`, and `drift`.

Much of the first `doctor` implementation can reuse existing checks from `init`, `sync`, `status`, `auth status`, `caddy`, and `preview cleanup`.

Use:

```sh
herald doctor
```

or:

```sh
herald check
```

`doctor` is clearer for troubleshooting. `check` is clearer for routine verification. If only one exists, prefer `doctor`.

The command should answer:

> Why is this server not deploying itself correctly?

It should produce actionable output, not a raw dump.

### Doctor Checks

Initial checks should include:

- Docker is installed and accessible
- Docker Compose plugin is installed
- Git is installed
- Herald data dir is readable/writable
- age key exists and permissions are acceptable
- secrets store can be decrypted
- server repo exists and can be pulled
- config loads and validates
- GitHub token exists and is valid
- webhook secret exists
- configured webhooks exist and point at the expected deploy domain
- Caddy is running
- Caddy network exists
- ports 80/443 are usable by Caddy
- configured stack domains are unique
- required secrets are present
- generated secrets can be created if absent
- compose files exist
- compose config validates for each stack
- deployed stacks have running compose projects
- orphaned `herald-*` compose projects are reported
- previews are within configured limits
- stale previews are reported

### Doctor Output

Output should be grouped by severity:

```text
Herald doctor: srv1

OK:
  docker accessible
  caddy running
  config valid
  4 webhooks registered

Needs attention:
  nextcloud missing required secret nextcloud/db_password
    fix: herald secret set nextcloud/db_password

  blog has not been deployed
    fix: herald deploy blog

Warnings:
  old-wiki is running but not present in config
    inspect: docker compose -p herald-old-wiki ps
    remove:  herald down old-wiki
```

Every failing check should include one of:

- exact fix command
- exact file to edit
- exact external action needed
- clear explanation why Herald cannot fix it automatically

### Doctor Scope

`doctor` should be read-only by default.

Optional future mode:

```sh
herald doctor --fix
```

`--fix` may only perform safe Herald-owned repairs:

- start Caddy
- create missing Caddy network
- sync webhooks
- generate declared auto-generated secrets
- clean stale preview metadata

It should not deploy stacks, remove stacks, delete volumes, or edit config.

### Read-Side Fixes For Trustworthy Diagnosis

Before or alongside `doctor`, fix the status/reporting inconsistencies that would make diagnosis confusing:

- Include the server IaC repo in status webhook reporting, matching webhook sync behavior.
- Render path-sourced stacks correctly in the web app detail page: show `path`, `auto_deploy`, and `update` instead of an empty GitHub repo/branch.
- Surface missing required secrets in status or the maintenance report without exposing secret values.
- Surface orphaned Herald compose projects in status or the maintenance report.

## Priority 3: Public Availability Badge

The existing status page should stay private. It exposes operational metadata that is useful to an admin but too detailed for a public README or website.

The public use case is different:

> Show whether Herald-managed services are up.

This should be a minimal, public-safe availability surface, not the full status UI.

### Public Badge Endpoint

Add a dedicated endpoint for simple availability:

```text
GET /badge
GET /badge/{stack}
GET /api/availability
```

Possible badge output:

```text
[herald|status: green]
[herald|status: degraded]
[herald|status: down]
```

For README usage, SVG badge output is useful:

```text
https://deploy.example.com/badge.svg
https://deploy.example.com/badge/myapp.svg
```

### Public Data Rules

The public endpoint should expose only:

- aggregate status: `green`, `degraded`, `down`, or `unknown`
- optional stack status for explicitly public stacks
- timestamp or age of last check

It should not expose:

- repo names
- branch names
- commit hashes
- secret key names
- compose override content
- internal paths
- container names
- preview branches
- webhook state
- Caddy/ACME details

### Availability Semantics

Use existing runtime status checks as the first implementation:

- `green`: all included stacks are running
- `degraded`: at least one included stack is degraded/stopped, but at least one is running
- `down`: no included stack is running
- `unknown`: Herald cannot determine status

Future versions may optionally support HTTP checks per stack:

```yaml
stacks:
  myapp:
    domain: app.example.com
    availability:
      public: true
      path: /health
      expected_status: 200
```

Default should be private. A stack must opt in before it appears by name in a public endpoint.

### No-Database Status Page History

A small statuspage.io-style history can stay file-backed.

Use append-only JSONL for check events:

```text
/etc/herald/availability/events-2026-06-09.jsonl
/etc/herald/availability/events-2026-06-10.jsonl
```

Each line is one check result:

```json
{"ts":"2026-06-09T14:00:00Z","service":"myapp","status":"up","latency_ms":83}
{"ts":"2026-06-09T14:05:00Z","service":"myapp","status":"down","error":"timeout"}
```

Rotate by day and keep a configured retention window:

```yaml
server:
  availability:
    interval: 5m
    retention: 30d
```

At a five-minute interval this is small:

```text
288 events/day/service
8640 events/30d/service
```

For fast public rendering, write a derived summary atomically:

```text
/etc/herald/availability/summary.json
```

Example:

```json
{
  "updated_at": "2026-06-09T14:05:00Z",
  "window": "24h",
  "services": {
    "myapp": {
      "status": "degraded",
      "uptime": 99.65,
      "last_check": "2026-06-09T14:05:00Z",
      "last_error": "timeout",
      "sparkline": ["up", "up", "up", "down", "up"]
    }
  }
}
```

The JSONL files are the source for availability history. `summary.json` is only a read model for the public page and badges. If it is lost, Herald can rebuild it from JSONL.

Incidents do not need a database either. They can be derived from consecutive failed checks within the selected time window:

- outage starts after N consecutive failed checks
- outage ends after a successful check
- current status becomes `degraded` when recent failures exist but the latest check is passing

Optional manually-curated incidents can come later as another file, but the first version should derive outages from the check log.

### Private Status Page

Keep the current status page behind authentication. It is useful for operational inventory:

- Caddy status
- stack names and domains
- deployed refs
- preview branches
- webhook state
- secret keys and targets
- config details

That page should not be the artifact embedded in public websites or READMEs.

## Priority 4: Better Setup Automation

`herald init` already does a lot:

- checks prerequisites
- clones the server repo
- loads config
- initializes the secrets store
- generates the webhook secret
- stores the GitHub token
- creates directories
- starts Caddy
- registers webhooks
- clones repo stacks

The next step is to reduce the remaining manual tail.

Proposed command:

```sh
herald init myorg/server --install --deploy
```

Behavior:

- bootstrap as today
- install and enable the systemd service
- start the daemon
- run startup sync
- deploy all stacks that are deployable
- skip stacks blocked by missing required secrets
- print the remaining blockers

Example completion:

```text
Herald initialized: srv1

Running:
  caddy
  herald.service

Deployed:
  blog
  plausible

Needs secrets:
  nextcloud/db_password
    fix: herald secret set nextcloud/db_password

Next:
  herald deploy nextcloud
```

This reinforces the product promise: after setup, the server maintains deployment wiring itself.

## Priority 5: Operational Report File

Avoid Terraform-style state. Herald does not need a remote state model.

But Herald should persist the last maintenance result for status, web UI, `doctor`, and support.

Possible file:

```text
/etc/herald/last-sync.json
```

Contents:

- last sync time
- server repo commit
- config path
- config load result
- Caddy result
- webhook sync result
- missing secrets by stack
- undeployed stacks
- stopped/degraded stacks
- orphaned Herald compose projects
- stale previews
- last maintenance error

This should be generated by the internal maintenance package. `webhooks.json` may remain a small specialized file, or webhook status can become part of the broader report. Avoid introducing two competing sources of read-side truth.

This file is an operational report, not the source of truth. The source of truth remains:

- `config.yml`
- encrypted secrets store
- Docker runtime
- GitHub webhook registrations

## What Not To Build

To keep the USP distinct, avoid:

- Terraform-like `plan/apply/state` language
- provider/plugin architecture
- dependency graph engine
- generic Docker dashboard
- web UI for editing config
- app marketplace
- `herald logs` and `herald exec` wrappers
- multi-server orchestration

If a command only saves typing `docker compose -p herald-<name> ...`, it should probably not exist.

## Recommended Implementation Order

1. Extract current `cmd/sync.go` behavior into an internal maintenance package.
2. Make `herald sync` call that package and print a human summary.
3. Update IaC push handling to call the same maintenance path.
4. Run maintenance once at `herald serve` startup.
5. Fix status read-side gaps: include the IaC repo webhook and render path stacks correctly.
6. Add a public-safe availability badge endpoint separate from the private status page.
7. Persist `last-sync.json` or an equivalent maintenance report.
8. Build `herald doctor` from the maintenance report plus deeper read-only checks.
9. Keep app repo webhooks, preview webhooks, and the existing IaC repo webhook reconciled during init/sync.
10. Add `herald init --install`.
11. Add `herald init --deploy` once doctor output can clearly explain blocked stacks.
12. Consider optional polling fallback only if webhook delivery proves insufficient or non-GitHub support becomes a goal.

## Success Criteria

Herald is moving in the right direction when these are true:

- pushing the server repo does not require SSH
- daemon restarts recover missed server repo changes
- Caddy/webhook wiring stays current
- status explains the last maintenance pass
- public badges show only availability, not operational inventory
- `doctor` explains what is wrong and how to fix it
- setup ends with a running daemon, not a checklist of manual server tasks
- normal admin work happens in git, not in an SSH session
