# Herald

Deploy daemon for self-hosted infrastructure. Config-driven, no database, single binary.

Herald manages Docker Compose deployments across servers. It receives GitHub webhooks, builds and deploys your apps, manages Caddy reverse proxy with automatic TLS, and stores secrets with age encryption. Everything is defined in a YAML config file per server.

## Quick Start

```sh
# Build
make

# Authenticate with GitHub (interactive device flow)
herald auth login --client-id <your-oauth-app-client-id>

# Bootstrap a server from its IaC repo
herald init nogo/srv2

# Deploy an app
herald deploy budget

# Start the daemon (webhook listener + status page)
herald serve

# Install as systemd service
sudo herald install
```

## GitHub Authentication

Herald supports three ways to authenticate with GitHub:

**1. OAuth Device Flow (recommended for interactive use)**

```sh
herald auth login --client-id <client-id>
# Or: HERALD_GITHUB_CLIENT_ID=<client-id> herald auth login
```

Prints a URL and code. Open the URL on any device (phone, laptop), enter the code, authorize. The token is stored encrypted in the secrets store. Works over SSH.

Requires a one-time GitHub OAuth App registration:
1. Go to https://github.com/settings/applications/new
2. Application name: `Herald`
3. Homepage URL: `https://github.com/nogo/herald`
4. Authorization callback URL: `http://localhost`
5. Check **Enable Device Flow**
6. Copy the Client ID

**2. Personal Access Token (for automation/CI)**

```sh
herald init nogo/srv2 --github-token ghp_xxxx
# Or: GITHUB_TOKEN=ghp_xxxx herald init nogo/srv2
```

**3. Stored token (automatic after first auth)**

After `herald auth login` or `herald init --github-token`, the token is stored encrypted. All subsequent commands use it automatically -- no env var needed.

Token resolution order: `--github-token` flag > `GITHUB_TOKEN` env > secrets store > device flow.

```sh
herald auth status     # check who you're authenticated as
herald auth logout     # remove stored token
```

## How It Works

```
GitHub (push to main)
  |
  v
herald (webhook listener on your server)
  |-- validates HMAC signature
  |-- matches repo to config
  |-- git pull + docker compose build + up
  |-- Caddy auto-provisions TLS for the domain
  v
App is live at https://your.domain.com
```

## Config

Each server has its own IaC repo with a `config.yml`:

```yaml
server:
  name: srv2
  deploy_domain: deploy.example.com
  stacks_dir: /opt/deploy

apps:
  budget:
    repo: nogo/budget-app
    branch: main
    domain: budget.example.com
    env_file: .env.budget

  tracker:
    repo: nogo/budget-app          # same repo, different config
    branch: main
    domain: tracker.example.com
    env_file: .env.tracker

stacks:
  nextcloud:
    path: stacks/nextcloud
    domain: cloud.example.com
    auto_deploy: false
    update_script: stacks/nextcloud/update.sh
```

## Features

- **Webhook-driven deploys** -- push to GitHub, app deploys automatically
- **Same repo, multiple deployments** -- one repo can serve different domains with different env/mounts
- **Preview environments** -- feature branches get dynamic subdomains (`feature-x.preview.domain.com`)
- **Managed stacks** -- scripted update runbooks for Nextcloud, Ghost, etc. (`herald update nextcloud`)
- **Age-encrypted secrets** -- `herald secret set db/password "value"`, no plaintext on disk
- **Caddy management** -- auto-provisions TLS, reverse proxy via Docker labels
- **GitHub webhook auto-registration** -- `herald webhooks sync` sets up hooks via API
- **Status page** -- web dashboard at the deploy domain showing live Docker state
- **Systemd integration** -- `herald install` creates a hardened service unit
- **No database** -- config file + encrypted secrets file + Docker runtime = all state

## Commands

```
herald auth login           Authenticate with GitHub (device flow)
herald auth status          Show current GitHub auth status
herald auth logout          Remove stored GitHub token
herald init <repo>          Bootstrap server from IaC repo
herald serve                Start webhook listener + status page
herald status               Show all services, domains, health
herald deploy <app>         Force re-deploy an app
herald deploy --all         Deploy all configured apps
herald update <stack>       Run update script for managed stack
herald secret set <k> <v>   Add/update encrypted secret
herald secret get <k>       Read a secret
herald secret list          List secret keys
herald preview list         Show active preview deployments
herald preview cleanup      Remove stale previews
herald sync                 Reconcile config with running state
herald webhooks sync        Register/deregister GitHub webhooks
herald caddy start|stop     Manage Caddy reverse proxy
herald install              Install as systemd service
herald version              Print version
```

## Architecture

```
/etc/herald/
  age.key              # master encryption key (back this up)
  secrets.age          # encrypted secrets store
  repo/                # cloned server IaC repo
    config.yml
    stacks/nextcloud/
    stacks/ghost/

/opt/deploy/
  apps/
    budget/repo/       # git clone of nogo/budget-app
    tracker/repo/       # separate clone, same repo
  stacks/
    nextcloud/ -> /etc/herald/repo/stacks/nextcloud/
  caddy/
    compose.yml        # caddy-docker-proxy
```

## Why Herald?

Herald was built because every existing self-hosted deploy tool either requires a database, is overscoped, or doesn't support config-as-code.

### Comparison

| | Herald | Coolify | Dokploy | Arcane | Temps | Dokku | Kamal | CapRover | Piku |
|---|---|---|---|---|---|---|---|---|---|
| **What it is** | Deploy daemon | Full PaaS | Full PaaS | Container mgmt UI | Full PaaS | Mini-Heroku | Deploy tool | PaaS | Mini-PaaS |
| **Database** | None | PostgreSQL + Redis | PostgreSQL + Redis | SQLite (embedded) | PostgreSQL + Redis | None (filesystem) | None (stateless) | MongoDB | None |
| **Config model** | YAML in git | Web UI (DB) | Web UI (DB) | Web UI (DB) | Web UI (DB) | CLI + filesystem | YAML (workstation) | Web UI | Procfile |
| **Deploy trigger** | GitHub webhook | GitHub webhook | GitHub webhook | Manual | GitHub webhook | `git push` | CLI push | `git push` / webhook | `git push` |
| **Build location** | On server | On server | On server | N/A | On server | On server | CI/registry | On server | On server |
| **Reverse proxy** | Caddy (managed) | Traefik | Traefik | None | Pingora | Nginx/Caddy | kamal-proxy | Nginx | Nginx |
| **Auto TLS** | Yes | Yes | Yes | No | Yes | Yes (plugin) | Yes | Yes | No |
| **Preview envs** | Yes | Yes | No | No | No | No | No | No | No |
| **Same repo, N deploys** | Yes | No | No | No | No | No | Partial | No | No |
| **Managed stacks** | Yes (runbooks) | Yes (UI) | Yes (UI) | No | Yes (UI) | No | No | No | No |
| **Secrets** | Age-encrypted | DB-encrypted | DB-encrypted | Encrypted env | DB-encrypted | Env files | .env files | DB-encrypted | Env files |
| **Multi-server** | Per-server binary | Agent-based | Agent-based | Agent-based | No | Single only | Native | Single only | Single only |
| **Status page** | Built-in web | Full web UI | Full web UI | Full web UI | Full web UI | CLI | CLI | Web UI | CLI |
| **Docker Compose** | Native | Yes | Yes | Yes | Yes | No | No | No | No |
| **Podman support** | Planned | No | No | Yes | No | No | No | No | No |
| **Language** | Go | PHP/Laravel | TypeScript | Go + Svelte | Rust | Bash + Go | Ruby | Node.js | Python |
| **RAM overhead** | ~10 MB | ~500 MB | ~400 MB | ~50-100 MB | ~200 MB+ | ~20 MB | 0 (workstation) | ~200 MB | ~10 MB |
| **Maturity** | New | Beta (2+ yrs) | v1.x | v1.16 (active) | v0.0.5 | 10+ years | Stable | Mature | Stable |
| **License** | BSD-3 | AGPL-3 | Proprietary(core) | BSD-3 | AGPL-3 | MIT | MIT | Apache-2 | MIT |

### Why not the others?

#### Coolify

Self-hosted PaaS written in PHP/Laravel. Deploys via GitHub webhooks, manages Traefik for reverse proxy, handles Let's Encrypt. Closest feature set to what Herald does.

**Why not:** Requires PostgreSQL + Redis (~500 MB RAM before a single app runs). All configuration lives in the database -- not version-controlled, not reviewable in a PR, not recoverable from a git repo. Known stability issues: CPU spikes, failed deployments leaving stale state. Still considered beta after 2+ years of development. Requires root SSH access to managed servers. No config-as-code -- everything is UI-driven.

**What it does well:** Polished web UI, one-click app marketplace, built-in monitoring. Good if you want a Heroku-like experience and accept the database dependency.

#### Dokploy

TypeScript/Next.js PaaS. Similar scope to Coolify -- GitHub deploys, Traefik, Let's Encrypt.

**Why not:** PostgreSQL + Redis required (~400 MB). Forces Docker Swarm mode even on single-node setups, adding unnecessary complexity. No Caddy support. No deploy hooks or scripted runbooks. Config is DB-stored. The Swarm requirement is the dealbreaker -- it changes Docker networking behavior and adds an orchestration layer you don't need for self-hosted apps.

**What it does well:** Clean UI, good compose project support, active development (v1.x).

#### Temps

Rust-based self-hosted PaaS. Markets itself as a "single binary" but that's misleading.

**Why not:** Requires PostgreSQL + TimescaleDB + Redis (3 containers). The "single binary" claim ignores the database infrastructure. v0.0.5 -- pre-release with 268 stars. Massively overscoped: tries to be Vercel + PostHog + Sentry + Resend in one project (60+ crates including analytics, error tracking, email with DKIM, vulnerability scanning, AI gateway). Only tested on Ubuntu, not Debian. Single-node only. Uses Cloudflare's Pingora for proxy -- technically interesting but no ecosystem for standalone config.

**What it does well:** Rust performance is genuinely fast. The architecture of separating concerns into crates is clean. If it matures, it could be compelling -- but it's too early and too broad.

#### Arcane

Docker management UI written in Go + SvelteKit. Formerly "Arcane Docker Management."

**Why not:** It's a container management dashboard, not a deploy tool. No CI/CD pipeline, no GitHub webhook integration, no git-based deploys, no reverse proxy management, no TLS provisioning. You can view containers, start/stop them, inspect logs, manage compose projects, and build images -- but you can't push code to GitHub and have it land on your server. Uses an embedded SQLite database. Different problem space.

**What it does well:** Clean container management UI. Podman support. Remote environments via agent architecture (direct + edge mode with outbound tunnels). Vulnerability scanning. Volume backups. Good as a Portainer replacement for container visibility and multi-host Docker management. BSD-3-Clause license. Active development (v1.16, 5k stars, 45 contributors).

#### Dokku

The original "mini-Heroku." Bash + Go, 10+ years old. The most battle-tested database-free option.

**Why not:** Single-server only. Bash-heavy internals are opaque to debug when things break. The `git push dokku main` deploy model doesn't fit GitHub webhook workflows natively (you can add plugins, but it's bolted on). No support for same-repo-multiple-deployment (one app = one repo). No config-as-code in the Herald sense -- config is imperative CLI commands (`dokku config:set`), not a declarative YAML file.

**What it does well:** Zero database. Plugin ecosystem (Caddy, PostgreSQL, Redis, Let's Encrypt). Zero-downtime deploys. Buildpack support. Proven in production for a decade. If you're deploying single-purpose apps from individual repos and want a `git push` workflow, Dokku is excellent.

#### Kamal

Basecamp's deploy tool. Ruby CLI that pushes Docker images to servers via SSH.

**Why not:** Requires Ruby on your workstation (not the server). Mandates a Docker registry -- images are built locally or in CI, pushed to a registry, then pulled on the server. No build-on-server. Designed for push-from-workstation workflow, not server-side webhook-triggered deploys. The YAML config (deploy.yml) is good but lives on your workstation, not on the server.

**What it does well:** Stateless, clean architecture. kamal-proxy is an excellent Go binary for zero-downtime switching. Native multi-server support (roles). No database. If your workflow is "build in CI, push to registry, deploy from workstation/CI," Kamal is the best option in this space.

#### CapRover

Node.js PaaS with web UI.

**Why not:** Forces Docker Swarm. Nginx-only (no Caddy). Web UI adds attack surface. Database for state. Less actively maintained than Coolify/Dokploy.

#### Piku

Python-based, Dokku-inspired. `git push` deploys on bare metal.

**Why not:** No Docker support. No zero-downtime deploys. Single Python file -- too minimal for 10+ containerized services. No compose support.

### Herald's trade-offs

What you give up:
- No web UI for configuration (you edit YAML files)
- No drag-and-drop deployments
- No marketplace/templates for one-click apps
- New project -- less battle-tested than Dokku or Kamal

What you get:
- Zero database dependency
- Config is a YAML file in a git repo -- reviewable, diff-able, recoverable
- One binary, ~10 MB RAM, systemd service
- Same repo can power multiple independent deployments
- Secrets encrypted at rest without external infrastructure
- Full server bootstrap with one command (`herald init`)

## Installation

### Build from source

```sh
git clone https://github.com/nogo/herald.git
cd herald
make
# Binary at bin/herald
```

### From GitHub Releases

Download the latest binary for your platform from [Releases](https://github.com/nogo/herald/releases):

```sh
# Linux amd64
curl -fsSL https://github.com/nogo/herald/releases/latest/download/herald-linux-amd64 -o herald
chmod +x herald

# Linux arm64 (Raspberry Pi, ARM servers)
curl -fsSL https://github.com/nogo/herald/releases/latest/download/herald-linux-arm64 -o herald
chmod +x herald
```

Verify:

```sh
./herald version
```

### Server Setup

#### 1. Prerequisites

On the target server (Debian/Ubuntu):

```sh
# Docker + Docker Compose must be installed
docker --version        # Docker 24+
docker compose version  # Compose v2+
git --version           # Git 2+
```

#### 2. Create a dedicated herald user

Herald should not run as root. Create a system user with Docker access:

```sh
# As root:
useradd -r -m -d /home/herald -s /bin/bash -G docker herald

# Create directories
mkdir -p /etc/herald
chown herald:herald /etc/herald

mkdir -p /opt/deploy
chown herald:herald /opt/deploy

# Install the binary
cp herald /usr/local/bin/herald
chmod 755 /usr/local/bin/herald

# Verify Docker access
su - herald -c "docker ps"
su - herald -c "herald version"
```

#### 3. Bootstrap (as herald user)

```sh
su - herald

# Authenticate with GitHub (pick one):
herald auth login --client-id <your-oauth-client-id>   # interactive
# or: export GITHUB_TOKEN=ghp_xxxx                     # token

# Bootstrap from your server's IaC repo
herald init myorg/my-server-repo
```

`herald init` will:
- Clone the server repo to `/etc/herald/repo/`
- Generate the age encryption key
- Start Caddy reverse proxy
- Register GitHub webhooks on all configured repos
- Clone app repositories

#### 4. Configure secrets

```sh
# Set secrets (stored age-encrypted)
herald secret set myapp/db_password "secure-password"
herald secret list

# Secrets are persisted in /etc/herald/secrets.age
# Back up /etc/herald/age.key — it cannot be recovered if lost
```

#### 5. Deploy

```sh
# Deploy an app
herald deploy myapp

# Deploy a managed stack
herald update nextcloud

# Deploy everything
herald deploy --all
```

#### 6. Start the daemon

```sh
# Test in foreground
herald serve

# Install as systemd service (as root)
sudo herald install --user herald
```

The systemd service:
- Starts herald on boot (after Docker)
- Restarts on crash (10s backoff)
- Runs as the `herald` user
- Security-hardened (`NoNewPrivileges`, `ProtectSystem=strict`, `PrivateTmp`)

#### 7. Verify

```sh
systemctl status herald          # service running
journalctl -u herald -f          # follow logs
herald status                    # all services and domains
curl https://deploy.example.com/health  # webhook endpoint
```

### Server Repo Structure

Each server gets its own IaC repo defining what runs on it:

```
my-server-repo/
  config.yml              # apps, stacks, domains
  stacks/
    nextcloud/
      compose.yaml        # compose file for the stack
      update.sh           # scripted update runbook
    ghost/
      compose.yaml
      update.sh
```

See the [Config](#config) section for the config.yml format.

### Data directories

After setup, the server has:

```
/etc/herald/                  # herald data (owned by herald user)
  age.key                     # master encryption key (BACK THIS UP)
  secrets.age                 # encrypted secrets store
  repo/                       # cloned server IaC repo

/opt/deploy/                  # deployments (owned by herald user)
  apps/<name>/repo/           # cloned app source repos
  stacks/<name>/              # symlinks to IaC repo stacks
  caddy/compose.yml           # caddy-docker-proxy (managed by herald)
```

### Updating herald

```sh
# Download new binary
curl -fsSL https://github.com/nogo/herald/releases/latest/download/herald-linux-amd64 -o /tmp/herald
chmod +x /tmp/herald

# Replace and restart (as root)
cp /tmp/herald /usr/local/bin/herald
systemctl restart herald
```

## Why port 9483?

Port numbers are derived from letter shapes in the project name:

```
Herald:      H≈9  E≈4  R≈8  A≈3  →  9483
Knotenpunkt: K≈9  P≈4  U≈7  N≈0  →  9470
Forge:       F≈9  O≈4  R≈6  G≈3  →  9463
```

Unregistered, above 1024, consistent across the toolchain.

## Requirements

- Go 1.26+ (build only)
- Docker + Docker Compose (runtime)
- Debian/Ubuntu server (tested)
- GitHub account (token via device flow or PAT)

## License

BSD-3-Clause
