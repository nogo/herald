# Herald

Config-driven deploy daemon for self-hosted infrastructure. No database, single binary.

Push to GitHub, your app deploys. Config is a YAML file per server. Secrets are age-encrypted. Caddy handles TLS automatically.

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
GitHub push → webhook → herald → git pull → docker compose up → Caddy TLS → live
```

Herald registers webhooks on your repos via the GitHub API. When you push, GitHub notifies herald, which pulls, builds, and deploys. Caddy provisions TLS certificates automatically.

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
- Git hooks disabled on all operations
- Webhook HMAC-SHA256 + rate limiting
- Status page uses constant-time auth comparison
- Systemd hardening (NoNewPrivileges, ProtectSystem, PrivateTmp)
- Per-app Docker network isolation (only the front service joins Caddy)

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

**Why Herald?** Config-as-code (not a database). One repo can deploy N times. No PostgreSQL/Redis tax. Full server bootstrap with one command.

**Trade-offs:** No web UI for config. No app marketplace. New project.

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
