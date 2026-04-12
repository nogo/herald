# Herald — Architectural Invariants

These invariants are prepended to every work unit prompt. They are non-negotiable constraints.

## What herald is

Herald is a git-to-production bridge for self-hosters. One YAML config per server. Push code, it deploys. Secrets are age-encrypted. Caddy handles TLS. No database, single binary.

## Migration approach

Herald is in production but config files will be updated by hand. No backwards compatibility required for `apps:` or `services:` config keys. Clean break: old config keys, old deploy paths, old Docker project names are all replaced. The operator will tear down old containers and redeploy after the refactor.

## Target architecture

See `docs/architecture.md` for the full target architecture and `docs/project.md` for the project goal.

Summary: Everything herald manages is a **stack**. A stack has a domain, secrets, and a source — either a GitHub repo (`repo:`) or a directory in the IaC repo (`path:`). One config section (`stacks:`), one deploy command, one code path. No "apps vs services" distinction.

- Config key: `stacks:` (replaces `apps:` and `services:`)
- Deploy dir: `/opt/deploy/<name>/` (flat, no `apps/` or `services/` subdirectories)
- Docker project name: `herald-<name>` (uniform for all stacks)
- CLI: `herald deploy <stack>`. No `herald update` command.

## Package boundaries

- **Core** (`config`, `deployer`, `webhook`, `preview`): The domain. May import supporting and generic packages.
- **Supporting** (`compose`, `caddy`, `secrets`, `github`): Enables core. May import `config` for types. Must not import each other (exception: `github` -> `secrets` for OAuth token storage).
- **Generic** (`runner`, `git`, `ui`, `systemd`): Zero herald logic. No internal imports except stdlib.

## Deploy pipeline

The deploy pipeline exists **once** in `deployer`. The stages are:

```
resolve source -> preflight -> secrets -> env + override -> compose up -> post-deploy hook
```

- `repo:` stacks: git clone/fetch from GitHub
- `path:` stacks: symlink to IaC repo directory
- Post-deploy hook: optional `update:` script

Preview environments use the same pipeline with ephemeral lifecycle.

## Build and verification

- **Always run `gofmt -w .` before finishing.** The fmt:check gate rejects unformatted code.
- `mise run check` must pass after every change (vet, fmt, tidy, test, lint).
- `.forge/verify` is the authoritative gate (runs `mise run check` + boundary import checks).
- Tests use `go test -race -count=1 ./...`.
- No CGO. Pure Go.

## Code style

- Go 1.26. Use stdlib where possible.
- Cobra for CLI commands.
- `slog` for structured logging.
- `age` for encryption.
- No unnecessary interfaces. Concrete types until a second consumer needs polymorphism.
- Prefer editing existing files over creating new ones.
