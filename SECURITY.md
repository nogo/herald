# Security Model

## Trust Boundaries

Herald operates with three trust levels:

### 1. The herald binary (highest trust)

Herald runs as a dedicated system user with Docker socket access. **Docker socket access is equivalent to root** on the host — any process that can talk to the Docker socket can run privileged containers, mount the host filesystem, and escalate to full root.

Herald mitigates this with systemd hardening (`NoNewPrivileges`, `ProtectSystem=strict`, `PrivateTmp`), but the Docker socket itself remains the primary attack surface.

**Future mitigation**: Podman rootless mode eliminates this risk entirely. A compromised Podman user can only affect their own containers, not the host. Herald's architecture is designed to support Podman as a drop-in replacement.

### 2. The server IaC repo (high trust)

The server repo (`config.yml`, compose files, deploy scripts) has the same effective trust level as the herald binary. A compromised IaC repo can:

- Execute arbitrary shell scripts via `update_script`
- Mount arbitrary host paths via compose `override`
- Run privileged containers via compose override
- Access all secrets in the store

**Treat the IaC repo with the same security as root access to the server.**

### 3. App repos (moderate trust)

App repos contain Dockerfiles and application code. Herald clones and builds them. Mitigations:

- **Git hooks are disabled** (`core.hooksPath=/dev/null`) on all clone/fetch operations, preventing code execution during git operations
- Apps run as unprivileged containers (unless the IaC repo compose override grants privileges)
- Apps cannot access other apps' data or secrets unless explicitly configured

A compromised app repo can run arbitrary code **inside its container** but cannot escape to the host (assuming no Docker vulnerabilities and no privileged compose overrides).

## Credential Handling

### GitHub tokens

- **Never embedded in git URLs** — passed via git credential helper environment variables, invisible in `ps`, `/proc`, and `.git/config`
- **Never in systemd unit files** — stored in `/etc/herald/environment` (0600) or the encrypted secrets store
- **Never logged** — log messages contain key names only, never values
- **Stored encrypted at rest** with age encryption in `secrets.age`

### Secrets store

- Encrypted with [age](https://age-encryption.org/) (X25519 + ChaCha20-Poly1305)
- Master key at `/etc/herald/age.key` (0600, owned by herald user)
- Secrets file at `/etc/herald/secrets.age` (0600)
- Atomic writes with file locking prevent corruption
- **Back up `age.key`** — if lost, all secrets are unrecoverable

### File permissions

| File | Mode | Contains |
|------|------|----------|
| `/etc/herald/age.key` | 0600 | Master encryption key |
| `/etc/herald/secrets.age` | 0600 | Encrypted secrets |
| `/etc/herald/environment` | 0600 | Systemd env vars |
| `.env` files per app | 0600 | Resolved secrets for containers |
| Docker secret files | 0600 | Individual secret values |
| Compose overrides | 0644 | Caddy labels, network config (no secrets) |

## Network Security

### Webhook endpoint

- **HMAC-SHA256 signature verification** on every request (constant-time comparison via `hmac.Equal`)
- **10 MB body size limit** prevents memory exhaustion
- **Rate limited**: 30 requests/minute with burst of 10
- Payload repo names are matched against config — unknown repos are ignored
- Webhook payloads cannot trigger arbitrary commands; only configured apps/stacks are deployable

### Status page

- **HTTP Basic Auth** with constant-time password comparison (`crypto/subtle`)
- **Rate limited**: 6 auth attempts/minute with burst of 5
- Error responses never expose Docker internals
- Behind Caddy TLS — credentials encrypted in transit
- **Recommendation**: Use a strong, random password (`openssl rand -base64 32`)

### Health endpoint

- `/health` returns only `{"status":"ok"}` — no version, no internal state
- No authentication required (used for monitoring)

## Recommendations

1. **Run herald as a dedicated user**, not root. The `herald` user should only be in the `docker` group.
2. **Back up `/etc/herald/age.key`** to a password manager or offline storage.
3. **Use `herald auth login`** instead of `--github-token` flags (CLI args are visible in process listings).
4. **Use stdin for secrets**: `echo "value" | herald secret set key` instead of `herald secret set key value`.
5. **Review IaC repo changes** — they have root-equivalent trust. Use branch protection and PR reviews.
6. **Keep the herald binary updated** — security fixes are applied via new releases.
7. **Consider Podman** for rootless container runtime in the future.
