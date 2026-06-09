# Herald

One VPS. One server repo. GitHub pushes make it live.

Herald is a single-binary deployment daemon for one Docker Compose server. It reacts to GitHub webhooks, deploys stacks, wires Caddy TLS, resolves encrypted secrets, and keeps the server reproducible from a git repo.

No database. No control panel. No SSH deploy scripts.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/nogo/herald/main/scripts/install.sh | sh
```

Or with wget:

```sh
wget -qO- https://raw.githubusercontent.com/nogo/herald/main/scripts/install.sh | sh
```

This creates a `herald` user, downloads the latest binary, and sets up directories. Run as root.

### From source

```sh
git clone https://github.com/nogo/herald.git && cd herald && make
```

## Setup

```sh
# 1. Authenticate with GitHub
herald auth login --client-id <your-oauth-client-id>

# 2. Bootstrap from your server's IaC repo
herald init myorg/my-server

# 3. Set secrets
herald secret set myapp/db_password

# 4. Deploy
herald deploy myapp

# 5. Start daemon + install as service
sudo herald install --user herald
```

After setup, the intended workflow is git-driven:

```text
push app repo      -> Herald deploys matching stack
push server repo   -> Herald reloads config and auto-deploys opted-in path stacks
push feature branch -> Herald creates/updates preview, if enabled
```

## Config

Each server has its own repo with a `config.yml`:

```yaml
server:
  name: srv1
  deploy_domain: deploy.example.com
  services_dir: /opt/deploy
  port: 9483

stacks:
  myapp:
    repo: myorg/myapp
    branch: main
    domain: myapp.example.com
    tag_pattern: "v[0-9]*"           # also deploy on matching tag push
    config: stacks/myapp/config.env  # non-secret env vars, committed to IaC repo
    secrets:
      - key: myapp/db_password
        target: db_password
        type: docker-secret
        generate: alphanumeric       # auto-generated on first deploy
    preview:
      enabled: true
      domain: "*.preview.myapp.example.com"

  myapp-pinned:
    repo: myorg/myapp
    tag: v2.1.0                      # deploy exact tag, no auto-deploy
    domain: stable.example.com

  nextcloud:
    path: stacks/nextcloud
    domain: cloud.example.com
    auto_deploy: false
    update: stacks/nextcloud/update.sh
```

Each stack has a domain, secrets, and a source — either a GitHub repo (`repo:`) or a directory in the IaC repo (`path:`).

See [docs/config.md](docs/config.md) for the full configuration reference.

## How it works

```
GitHub app push    → webhook → herald → git pull/build → docker compose up → live
GitHub config push → webhook → herald → reload config → path stacks update
```

Herald registers webhooks on your app repos and on the server repo via the GitHub API. When you push app code, Herald pulls, builds, and deploys the matching stack. When you push server config, Herald pulls the server repo, reloads config, and auto-deploys path-sourced stacks with `auto_deploy: true`. Caddy provisions TLS certificates automatically.

Everything Herald manages is a stack: a Docker Compose project with a domain, secrets, and a source.

## Commands

**Daemon**
```
herald install              Install as systemd service
herald serve                Start webhook listener
herald sync                 Pull IaC repo + reconcile config + sync webhooks
herald status               Show apps, services, domains, health
```

**Stacks**
```
herald deploy <stack>       Deploy a stack
herald deploy --all         Deploy all stacks
herald down <stack>         Stop and remove a stack's containers
```

**Previews**
```
herald preview list         List active preview deployments
herald preview remove <id>  Remove a preview deployment
herald preview cleanup      Remove previews for deleted branches
```

**Secrets**
```
herald secret set <key>     Set a secret (interactive prompt)
herald secret list          List secret keys
```

**Infrastructure**
```
herald caddy start|stop     Manage reverse proxy
herald webhooks sync        Register GitHub webhooks
```

**Auth**
```
herald auth login           Authenticate with GitHub
herald auth status          Show authentication status
herald version              Print version
```

## GitHub Auth

```sh
# Interactive device flow (recommended)
herald auth login --client-id <client-id>

# Personal access token
herald init myorg/srv --github-token ghp_xxxx

# Check status
herald auth status
```

Token is stored encrypted. All subsequent commands use it automatically.

To create a GitHub OAuth App: [github.com/settings/applications/new](https://github.com/settings/applications/new) -- set "Enable Device Flow", callback URL `http://localhost`.

## Security

See [SECURITY.md](SECURITY.md) for the full security model. Key points:

- Secrets encrypted at rest with [age](https://age-encryption.org/)
- GitHub tokens never in URLs, process args, or logs
- Git hooks disabled on all operations; webhook git refs validated
- Webhook HMAC-SHA256 + per-IP rate limiting
- Status page uses constant-time auth comparison
- Preview environments receive no secrets; fork PRs cannot trigger them
- Systemd hardening (NoNewPrivileges, ProtectSystem, kernel/namespace restrictions)
- Per-app Docker network isolation (only the front service joins Caddy)

The authenticated status page is for operational detail. Public availability badges/status pages are planned as a separate, minimal surface that exposes only opted-in service availability.

## Comparison

| | Herald | Coolify | Dokploy | Dokku | Kamal |
|---|---|---|---|---|---|
| Database | **None** | PostgreSQL+Redis | PostgreSQL+Redis | None | None |
| Config | YAML in git | Web UI | Web UI | CLI | YAML |
| Deploy trigger | Webhook | Webhook | Webhook | git push | CLI push |
| Reverse proxy | Caddy | Traefik | Traefik | Nginx/Caddy | kamal-proxy |
| Auto TLS | Yes | Yes | Yes | Plugin | Yes |
| Preview envs | Yes | Yes | No | No | No |
| Same repo, N deploys | Yes | No | No | No | Partial |
| RAM | ~10 MB | ~500 MB | ~400 MB | ~20 MB | 0 |

**Why Herald?** GitHub-native deploy automation for one VPS. One server repo drives wiring. One app repo can deploy N times. No PostgreSQL/Redis tax.

**Trade-offs:** GitHub only. One server only. No web UI for config. No app marketplace. New project.

## Roadmap

Near-term direction:

- make the daemon run the full `herald sync` maintenance path on server repo pushes and startup
- add `herald doctor` for actionable private diagnosis
- add public-safe availability badges/status pages backed by append-only JSONL files, no database

The public availability surface should show only `up`, `degraded`, `down`, or `unknown` for opted-in services. Detailed status stays private.

## Architecture

```
/etc/herald/                  herald data
  age.key                     encryption key (BACK THIS UP)
  secrets.age                 encrypted secrets
  repo/                       cloned server IaC repo
    config.yml
    stacks/<name>/            compose files, config, update scripts

/opt/deploy/                  deployments
  <name>/                     per-stack runtime state
    repo/                     cloned stack repo or symlink to IaC path
    .env                      generated: config base + env secrets
    deployed_ref              last deployed ref (repo-sourced stacks only)
    secrets/<name>            docker secret files
    compose.override.yml      generated by herald
  previews/<stack>-<branch>/  ephemeral preview environments
    repo/
    .env
    compose.override.yml
  caddy/                      caddy-docker-proxy
```

## Why port 9483?

```
Herald:      H≈9  E≈4  R≈8  A≈3  →  9483
```

Derived from letter shapes. Unregistered, above 1024.

## License

BSD-3-Clause
