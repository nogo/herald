# Herald Configuration Reference

Herald is configured by a single `config.yml` file, typically at `/etc/herald/repo/config.yml` (inside the cloned IaC repo). All paths below are relative to the IaC repo root unless stated otherwise.

---

## Top-level structure

```yaml
server:    # required — server identity and global settings
  ...

stacks:    # optional — everything herald deploys
  name: ...
```

---

## `server`

| Field | Required | Default | Description |
|---|---|---|---|
| `name` | yes | — | Short identifier for this server. Used in logs and status output. |
| `deploy_domain` | yes | — | Domain where herald's webhook listener is reachable (e.g. `deploy.example.com`). |
| `services_dir` | yes | — | Base directory for all deployments (e.g. `/opt/deploy`). All stacks land directly under `<name>/` (flat, no subdirectories). |
| `port` | no | `9483` | Port herald listens on. |
| `bind` | no | all interfaces | Listen address. Empty binds all interfaces (`:port`). Set `127.0.0.1` to bind loopback-only when Caddy runs on the same host. |
| `acme_email` | no | `webmaster@<deploy_domain>` | Email for ACME/Let's Encrypt TLS certificate registration via Caddy. |
| `github_token` | no | — | GitHub personal access token. Supports `${ENV_VAR}` expansion. Prefer `herald auth login` instead. |

```yaml
server:
  name: srv1
  deploy_domain: deploy.example.com
  services_dir: /opt/deploy
  port: 9483
  acme_email: ops@example.com
```

---

## `stacks`

Each stack has a domain, secrets, and a source — either a GitHub repo (`repo:`) or a directory in the IaC repo (`path:`). The source field is the only fork in the deploy pipeline; everything else is shared.

Each key under `stacks:` is the stack name used in CLI commands (`herald deploy <name>`).

| Field | Applies to | Required | Default | Description |
|---|---|---|---|---|
| `repo` | repo stacks | yes* | — | GitHub repo in `owner/name` format. Mutually exclusive with `path`. |
| `path` | path stacks | yes* | — | Path to directory within the IaC repo (e.g. `stacks/nextcloud`). Must contain a `compose.yml`. Mutually exclusive with `repo`. |
| `domain` | both | yes | — | Primary domain. Herald configures Caddy to route traffic here. |
| `branch` | repo stacks | no | `main` | Branch to track. Mutually exclusive with `tag`. |
| `tag` | repo stacks | no | — | Deploy a specific tag instead of tracking a branch. Mutually exclusive with `branch`. |
| `tag_pattern` | repo stacks | no | — | Glob pattern (e.g. `v[0-9]*`). When a matching tag is pushed to GitHub, herald deploys it automatically. Requires `branch`. |
| `compose` | repo stacks | no | `compose.yml` | Path to the compose file. Relative paths resolve from the IaC repo root; absolute paths are used as-is. |
| `config` | both | no | — | Path to a non-secret env file, relative to the IaC repo root. Values are loaded as a base layer; secrets overlay on top. |
| `env_file` | both | no | — | Additional env file to include in the compose override, relative to the stack deploy directory. |
| `override` | both | no | — | Inline YAML merged into the compose override. Use to add labels, extra env_file entries, or any compose key for services that herald doesn't manage directly. |
| `auto_deploy` | path stacks | no | `false` | Whether to redeploy automatically when the IaC repo is updated via `herald sync`. |
| `update` | path stacks | no | — | Path to a shell script run as a post-deploy hook, relative to the IaC repo root. Receives `STACK_NAME` and `STACK_DIR` as environment variables. |
| `secrets` | both | no | — | List of secrets to inject. See [Secrets](#secrets). |
| `preview` | repo stacks | no | — | Preview deployment config. See [Preview deployments](#preview-deployments). |

### Minimal examples

```yaml
stacks:
  myapp:                         # repo-sourced stack
    repo: myorg/myapp
    branch: main
    domain: myapp.example.com

  nextcloud:                     # path-sourced stack
    path: stacks/nextcloud
    domain: cloud.example.com
```

### Full example

```yaml
stacks:
  myapp:
    repo: myorg/myapp
    branch: main
    domain: myapp.example.com
    compose: /etc/herald/repo/stacks/myapp/compose.yml
    config: stacks/myapp/config.prod.env
    secrets:
      - key: myapp/db_password
        target: db_password
        type: docker-secret
        generate: alphanumeric
        length: 32
      - key: myapp/auth_secret
        target: AUTH_SECRET
        type: env
        generate: base64
    override: |
      services:
        worker:
          env_file:
            - /opt/deploy/myapp/.env
```

### `config` — non-secret base layer

Points to a committed env file in your IaC repo containing non-secret configuration: hostnames, feature flags, log levels, etc.

```
# stacks/myapp/config.prod.env
DATABASE_HOST=database
LOG_LEVEL=info
SMTP_HOST=smtp.example.com
```

At deploy time herald reads this file, then overlays all `type: env` secrets on top. The merged result is written to `<services_dir>/<name>/.env`. **Secrets always win on key collision.**

### `override` — inline compose YAML

Raw YAML merged into the generated compose override. Use this to reach services that herald doesn't inject automatically (workers, migration runners, etc.).

```yaml
override: |
  services:
    worker:
      env_file:
        - /opt/deploy/myapp/.env
    migrate:
      env_file:
        - /opt/deploy/myapp/.env
```

### `!override` — replacing lists in compose

Docker Compose merges lists by appending. Herald uses the `!override` YAML tag on `env_file` lists so they **replace** the base compose file's value instead of appending to it. This ensures the generated `.env` file is the single source of environment variables.

The tag is applied automatically by herald on the main service's `env_file`. If you need the same behavior in your `override:` YAML (e.g., to replace a list rather than append), use the tag explicitly:

```yaml
override: |
  services:
    worker:
      env_file: !override
        - /opt/deploy/myapp/.env
```

Without `!override`, the worker's `env_file` list would be appended to whatever the base compose file defines. With it, the list is replaced entirely.

---

## Secrets

All stacks share the same `secrets:` list format.

| Field | Required | Description |
|---|---|---|
| `key` | yes | Identifier in the herald secret store. Namespaced by convention: `<stack>/secret_name`. |
| `type` | yes | `env` or `docker-secret`. |
| `target` | yes | For `type: env`: the environment variable name. For `type: docker-secret`: the Docker secret name. |
| `generate` | no | Auto-generate if absent: `base64`, `alphanumeric`, or `hex`. Uses `SetIfAbsent` semantics — generated once, stable across deploys. |
| `length` | no | Length for generated secrets. For `base64`/`hex`: number of random bytes before encoding. For `alphanumeric`: number of characters. Range: 16–512. Defaults to 32. |

### `type: env`

The secret value is written into `<services_dir>/<name>/.env` as `TARGET=value`. Herald injects this file via the compose override's `env_file`.

```yaml
- key: myapp/smtp_password
  target: SMTP_PASSWORD
  type: env
```

### `type: docker-secret`

The secret value is written to a file at `<services_dir>/<name>/secrets/<target>`. Herald adds the secret definition to the compose override:

```yaml
secrets:
  <target>:
    file: /opt/deploy/<name>/secrets/<target>
```

Your compose file must reference the secret by name. Give it a placeholder `file:` so Docker Compose validates the base file; herald's override replaces the path at deploy time:

```yaml
services:
  app:
    secrets:
      - db_password

secrets:
  db_password:
    file: ./secrets/db_password  # placeholder — herald overrides with the real path
```

### Auto-generation

Secrets with `generate` set are created the first time they're needed and never overwritten:

```yaml
secrets:
  - key: myapp/db_password
    target: db_password
    type: docker-secret
    generate: alphanumeric   # [a-zA-Z0-9], safe for most DB password fields
    length: 32

  - key: myapp/session_secret
    target: SESSION_SECRET
    type: env
    generate: base64         # url-safe base64, good for HMAC/signing keys
    length: 48               # 48 random bytes → ~64 char base64 string

  - key: myapp/api_key
    target: API_KEY
    type: env
    generate: hex            # lowercase hex, good for fixed-length keys
```

Required secrets (no `generate`) that are missing cause `herald deploy` to fail with a list of all missing keys before any side effects.

---

## Tag deployments

### Pinned tag

Deploy a specific release tag. No auto-deploy — `herald deploy` only. `branch` must not be set.

```yaml
stacks:
  myapp:
    repo: myorg/myapp
    tag: v1.2.3
    domain: myapp.example.com
```

To upgrade, change `tag:` in config, run `herald sync` then `herald deploy myapp`.

### Auto-deploy on tag pattern

Continue tracking a branch for normal auto-deploy, and also deploy automatically when a matching tag is pushed.

```yaml
stacks:
  myapp:
    repo: myorg/myapp
    branch: main
    tag_pattern: "v[0-9]*"   # deploy when a tag like v1.2.3 is pushed
    domain: myapp.example.com
```

Pattern syntax is stdlib glob (`path.Match`): `v*`, `v[0-9]*`, `release-*`. A matching tag push triggers an immediate deploy of that exact tag. The next branch push deploys the branch tip again — no lasting pin.

**Rules:**
- `tag` and `branch` are mutually exclusive
- `tag_pattern` requires `branch` (not compatible with `tag`)

---

## Preview deployments

Preview deployments create an ephemeral environment for every feature branch or pull request, each at its own subdomain. No config change required per branch.

### Setup

```yaml
stacks:
  myapp:
    repo: myorg/myapp
    branch: main
    domain: myapp.example.com
    preview:
      enabled: true
      domain: "*.preview.myapp.example.com"  # wildcard — must contain *
```

**DNS requirement:** add a wildcard record pointing to the server IP:
```
*.preview.myapp.example.com → <server-ip>
```
Caddy provisions TLS per subdomain automatically.

### Triggers

| Event | Action |
|---|---|
| Push to any branch other than `branch` | Deploy preview for that branch |
| Pull request `opened` or `synchronize` (same-repo only) | Deploy preview for PR head branch |
| Push with branch deletion | Tear down preview |
| Pull request `closed` | Tear down preview |

Pull requests from **forks do not trigger previews** — only same-repo branches and PRs do.
Preview environments also receive **no secrets**: only the non-secret `config`/`env_file`
values are injected, never resolved `secrets`. An app that requires secrets at runtime may
not come up fully in a preview.

Branch names are slugified into DNS labels with a short hash suffix for uniqueness:
`feature/auth-v2` → `feature-auth-v2-1a2b3c4d.preview.myapp.example.com`.

### Managing previews

```sh
herald preview list           # show active previews (ID, domain, branch, age)
herald preview remove <id>    # tear down a specific preview
herald preview cleanup        # remove previews whose branches no longer exist on remote
```

`herald preview cleanup` checks each branch via `git ls-remote`. Run it periodically or after `herald sync`.

### Limits and caveats

- **Max 10 previews per stack.** Pushing to an existing preview branch updates it (no new slot used).
- **Secrets are shared with production.** Previews resolve secrets from the same store using the same `secrets:` list. There is no per-preview secret isolation.
- **No per-preview database.** Unless your compose file or `override:` provisions a separate database per environment, previews share state with whatever the compose file points to. Previews work best for stateless frontends or stacks that tolerate shared data.
- **Volumes are wiped on teardown.** Preview `compose down` always passes `--volumes`.

### `preview` fields

| Field | Required | Default | Description |
|---|---|---|---|
| `enabled` | yes | — | Must be `true` to activate previews. |
| `domain` | yes | — | Wildcard domain pattern. Must contain `*`. The `*` is replaced with the branch slug. |

---

## Multiple environments on one server

Run dev and demo side-by-side by defining separate stack entries pointing to the same repo with different branches and config files:

```yaml
stacks:
  myapp-dev:
    repo: myorg/myapp
    branch: main
    domain: dev.example.com
    config: stacks/myapp/config.dev.env
    secrets:
      - key: myapp-dev/db_password
        target: db_password
        type: docker-secret
        generate: alphanumeric

  myapp-demo:
    repo: myorg/myapp
    branch: demo
    domain: demo.example.com
    config: stacks/myapp/config.demo.env
    secrets:
      - key: myapp-demo/db_password
        target: db_password
        type: docker-secret
        generate: alphanumeric
```

Each entry gets its own deploy directory, Docker Compose project (`herald-<name>`), secret namespace, and isolated Docker network.

---

## Networking

Every stack and preview gets its own isolated Docker network. The main service (the one Caddy routes to) joins both the shared `caddy` network and the stack-specific internal network. All other services in the compose file (databases, caches, workers) only see the internal network.

| Type | Network name | Cleaned up by |
|---|---|---|
| Stack | `herald-<name>-internal` | `herald down <name>` |
| Preview | `herald-preview-<id>-internal` | `herald preview remove` / `herald preview cleanup` |

The `caddy` network is a shared external network created once by herald. Stack-internal networks are project-scoped and removed automatically by `docker compose down`.

This means:
- Containers from different stacks cannot reach each other directly
- A database in `myapp`'s compose file is unreachable from `otherapp`
- Only the front service is exposed to Caddy for reverse proxying

If your compose file defines additional services (workers, migrations), use the `override` field to attach them to the internal network or pass the generated `.env` file. Herald only injects the detected main service automatically.

---

## Filesystem layout

After deployment the directory structure under `services_dir` looks like:

```
/opt/deploy/
  myapp/
    repo/                  # git clone of the stack repo (repo-sourced stacks)
    .env                   # generated: merged config + type:env secrets
    deployed_ref           # last deployed ref, e.g. "main@abc1234" (repo-sourced stacks only)
    secrets/
      db_password          # written by herald for type:docker-secret entries
    compose.override.yml   # generated by herald (Caddy labels, env_file, secret defs)
  nextcloud/               # path-sourced stack: deploy dir with symlink to IaC path
    .env                   # generated (if secrets defined)
    secrets/
      admin_password
    compose.override.yml
  previews/
    myapp-feature-auth/    # one directory per active preview (id = stackname-branchslug)
      repo/
      .env
      compose.override.yml
```
