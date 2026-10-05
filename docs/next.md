# Next Direction

## Principle

An admin should not SSH into the server for normal deployment wiring. After setup, the workflow is: edit `config.yml` or app code, push, Herald reacts and reports problems clearly. SSH is for recovery, not routine work.

The maintenance pass behind `herald sync`, daemon startup and server-repo pushes, `herald doctor`, and the public availability page carry this today. What follows is what is still open, the rules any new automation must keep, and what was decided against.

## Open

- **Availability badges.** An SVG badge per server and per public stack (`/badge.svg`, `/badge/<stack>.svg`) on top of the existing availability page and its JSONL history. Same public data rules as the page: aggregate or opted-in stack state and the check time, nothing else.
- **`auto_deploy` on a repo stack** is accepted and silently ignored. Either reject it in config validation, naming the stack, or honor it (see the decided-against gate below before honoring it).
- **`last-sync.json` is a pass record, not a state cache.** It still carries live state (stack state, orphans). Those fields are advisory "as seen during the last pass"; consumers that need "is it up now" query Docker. Shrink the file to pass history when it is next touched.
- **`herald doctor --fix`.** Safe, Herald-owned repairs only: start Caddy, create the Caddy network, sync webhooks, generate declared `generate:` secrets, clean stale preview metadata. Never deploy, remove stacks, delete volumes or edit config.
- **Custom `services_dir`** needs a hand edit of `ReadWritePaths` in the systemd unit. The installer could take the directory as an option.
- **`herald destroy <stack>`** (down plus removing the deploy dir, volumes opt-in) is the fix command an orphan report wants. Manual only, never run by the daemon. Deferred until teardown semantics are designed.

## Rules for automated maintenance

Automation may do Herald-owned wiring:

- pull the server repo, fast-forward only
- apply config only when it validates, routes included; a broken push keeps the previous config and deploys nothing
- ensure the Caddy container and network exist
- reconcile GitHub webhooks: create missing, prune stale
- write the report files
- redeploy an `auto_deploy` path stack whose directory changed since its last successful deploy

Automation must not:

- first-deploy a stack (that needs secret setup the daemon cannot guess)
- deploy a repo stack because its remote moved; app-repo webhooks do that
- remove unknown containers, delete volumes, or remove deploy directories
- edit config files or rotate secrets
- hide a failed check

## Decided against

- **An `auto_deploy` gate for repo stacks.** Release gating is `tag_pattern:`; "deploy when ready" is already covered because preflight fails a deploy with a missing secret and changes nothing. Revisit only for a real need to freeze a branch-tracked stack, and then as a command (`herald freeze <stack>`), not a config field.
- **Polling as the automation model.** GitHub webhooks drive the GitHub mode; `herald signal` from a post-receive hook drives the bare-repo mode. Polling stays out unless webhook delivery proves unreliable.
- **`herald init --install` and `--deploy`.** systemd belongs to `install.sh`, which already runs as root; first deploy stays a manual `herald deploy`.
- **Terraform vocabulary and shape.** No `plan`/`apply`/`state`/`drift`, providers, plugins or dependency graph. Herald operates one provisioned server; it does not provision it.
- **Wrappers.** No `herald logs` or `herald exec`. If a command only saves typing `docker compose -p herald-<name> …`, it should not exist.
