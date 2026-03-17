# Herald Configuration Reference

Herald is configured by a single `config.yml` file, typically at `/etc/herald/repo/config.yml` (inside the cloned IaC repo). All paths below are relative to the IaC repo root unless stated otherwise.

---

## Top-level structure

```yaml
server:    # required — server identity and global settings
  ...

apps:      # optional — apps you own (source in GitHub)
  name: ...

services:  # optional — software you operate (compose files in IaC repo)
  name: ...
```

---

## `server`

| Field | Required | Default | Description |
|---|---|---|---|
| `name` | yes | — | Short identifier for this server. Used in logs and status output. |
| `deploy_domain` | yes | — | Domain where herald's webhook listener is reachable (e.g. `deploy.example.com`). |
| `services_dir` | yes | — | Base directory for all deployments (e.g. `/opt/deploy`). Apps land under `apps/<name>/`, services under `services/<name>/`. |
| `port` | no | `9483` | Port herald listens on. |
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

## `apps`

Apps are services whose source code you own. Herald clones the repo, builds with Docker Compose, and redeploys on every push to the configured branch.

Each key under `apps:` is the app name used in CLI commands (`herald deploy <name>`).

| Field | Required | Default | Description |
|---|---|---|---|
| `repo` | yes | — | GitHub repo in `owner/name` format. |
| `branch` | no | `main` | Branch to track. |
| `domain` | yes | — | Primary domain. Herald configures Caddy to route traffic here. |
| `compose` | no | `compose.yml` | Path to the compose file. Relative paths resolve from the IaC repo root; absolute paths are used as-is. |
| `config` | no | — | Path to a non-secret env file, relative to the IaC repo root. Values are loaded as a base layer; secrets overlay on top. |
| `env_file` | no | — | Additional env file to include in the compose override, relative to the app deploy directory. |
| `override` | no | — | Inline YAML merged into the compose override. Use to add labels, extra env_file entries, or any compose key for services that herald doesn't manage directly. |
| `secrets` | no | — | List of secrets to inject. See [Secrets](#secrets). |
| `preview` | no | — | Preview deployment config. See [Preview deployments](#preview-deployments). |

### Minimal example

```yaml
apps:
  myapp:
    repo: myorg/myapp
    branch: main
    domain: myapp.example.com
```

### Full example

```yaml
apps:
  myapp:
    repo: myorg/myapp
    branch: main
    domain: myapp.example.com
    compose: /etc/herald/repo/apps/myapp/compose.yml
    config: apps/myapp/config.prod.env
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
            - /opt/deploy/apps/myapp/.env
```

### `config` — non-secret base layer

Points to a committed env file in your IaC repo containing non-secret configuration: hostnames, feature flags, log levels, etc.

```
# apps/myapp/config.prod.env
DATABASE_HOST=database
LOG_LEVEL=info
SMTP_HOST=smtp.example.com
```

At deploy time herald reads this file, then overlays all `type: env` secrets on top. The merged result is written to `<services_dir>/apps/<name>/.env`. **Secrets always win on key collision.**

### `override` — inline compose YAML

Raw YAML merged into the generated compose override. Use this to reach services that herald doesn't inject automatically (workers, migration runners, etc.).

```yaml
override: |
  services:
    worker:
      env_file:
        - /opt/deploy/apps/myapp/.env
    migrate:
      env_file:
        - /opt/deploy/apps/myapp/.env
```

---

## `services`

Services are software you operate but don't develop — Nextcloud, Ghost, databases. Compose files live in the IaC repo. Herald manages Caddy routing, secret injection, and optional update scripts.

Each key under `services:` is the service name used in CLI commands (`herald update <name>`).

| Field | Required | Default | Description |
|---|---|---|---|
| `path` | yes | — | Path to the service directory within the IaC repo (e.g. `services/nextcloud`). Must contain a `compose.yml`. |
| `domain` | yes | — | Primary domain for Caddy routing. |
| `auto_deploy` | no | `false` | Whether to redeploy automatically when the IaC repo is updated via `herald sync`. |
| `update_script` | no | — | Path to a shell script run by `herald update <name>`, relative to the IaC repo root. Receives `SERVICE_NAME` and `SERVICE_DIR` as environment variables. |
| `config` | no | — | Path to a non-secret env file, relative to the IaC repo root. Same semantics as apps. |
| `env_file` | no | — | Additional env file to include in the compose override. |
| `secrets` | no | — | List of secrets to inject. See [Secrets](#secrets). |

```yaml
services:
  nextcloud:
    path: services/nextcloud
    domain: cloud.example.com
    auto_deploy: false
    update_script: services/nextcloud/update.sh
    secrets:
      - key: nextcloud/admin_password
        target: NEXTCLOUD_ADMIN_PASSWORD
        type: env
        generate: alphanumeric
        length: 24
```

---

## Secrets

Both apps and services share the same `secrets:` list format.

| Field | Required | Description |
|---|---|---|
| `key` | yes | Identifier in the herald secret store. Namespaced by convention: `<app>/secret_name`. |
| `type` | yes | `env` or `docker-secret`. |
| `target` | yes | For `type: env`: the environment variable name. For `type: docker-secret`: the Docker secret name. |
| `generate` | no | Auto-generate if absent: `base64`, `alphanumeric`, or `hex`. Uses `SetIfAbsent` semantics — generated once, stable across deploys. |
| `length` | no | Length for generated secrets. For `base64`/`hex`: number of random bytes before encoding. For `alphanumeric`: number of characters. Range: 16–512. Defaults to 32. |

### `type: env`

The secret value is written into `<services_dir>/apps/<name>/.env` (or `services/<name>/.env` for services) as `TARGET=value`. Herald injects this file via the compose override's `env_file`.

```yaml
- key: myapp/smtp_password
  target: SMTP_PASSWORD
  type: env
```

### `type: docker-secret`

The secret value is written to a file at `<services_dir>/apps/<name>/secrets/<target>`. Herald adds the secret definition to the compose override:

```yaml
secrets:
  <target>:
    file: /opt/deploy/apps/<name>/secrets/<target>
```

Your compose file must reference the secret by name:

```yaml
services:
  app:
    secrets:
      - db_password

secrets:
  db_password:  # file: path is provided by herald's override — leave empty here
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

## Preview deployments

Preview deployments create ephemeral environments per branch or pull request.

```yaml
apps:
  myapp:
    repo: myorg/myapp
    domain: myapp.example.com
    preview:
      enabled: true
      domain: "*.preview.example.com"   # wildcard — must contain *
```

When enabled, pushing to a non-default branch creates a deployment at `<branch>.preview.example.com`. Use `herald preview list` to see active previews.

---

## Multiple environments on one server

Run dev and demo side-by-side by defining separate app entries pointing to the same repo with different branches and config files:

```yaml
apps:
  myapp-dev:
    repo: myorg/myapp
    branch: main
    domain: dev.example.com
    config: apps/myapp/config.dev.env
    secrets:
      - key: myapp-dev/db_password
        target: db_password
        type: docker-secret
        generate: alphanumeric

  myapp-demo:
    repo: myorg/myapp
    branch: demo
    domain: demo.example.com
    config: apps/myapp/config.demo.env
    secrets:
      - key: myapp-demo/db_password
        target: db_password
        type: docker-secret
        generate: alphanumeric
```

Each entry gets its own deploy directory, Docker Compose project (`herald-<name>`), and secret namespace.

---

## Filesystem layout

After deployment the directory structure under `services_dir` looks like:

```
/opt/deploy/
  apps/
    myapp/
      repo/          # git clone of the app repo (managed by herald)
      .env           # generated: merged config + type:env secrets
      secrets/
        db_password  # written by herald for type:docker-secret entries
      compose.override.yml   # generated by herald (Caddy labels, env_file, secret defs)
  services/
    nextcloud/
      .env           # generated (if secrets defined)
      secrets/
        admin_password
      compose.override.yml
```
