# Stack Scenarios

A map of every situation Herald can face with a stack: what triggers it, what
Herald does today, and where the gaps are.

**Source of truth is git + Docker + systemd.** Herald keeps no authoritative
state of its own. Any record it writes (`deployed_ref`, future report files) is a
derived cache that can be rebuilt or thrown away. This doc reflects that: every
"how do we know X" answer must resolve to git, Docker, or the config — never to a
Herald-owned database.

Legend:

- ✅ handled
- ⚠️ partial / by-design-but-rough / needs operator action
- 🐛 latent bug
- ❌ not implemented

---

## 1. The stack model

Every stack has exactly one source. The two are mutually exclusive
(`config.go:203`).

### repo stack (`repo:`)
- Source: a GitHub repo, deployed via `git clone` / `fetch+reset` into
  `<services_dir>/<name>/repo` (`deployer.go:339`).
- Ref: `branch:` **xor** `tag:` required (`config.go:231-234`).
- `tag_pattern:` — glob; requires `branch`, not compatible with fixed `tag`
  (`config.go:236`). Drives auto-deploy on matching tag pushes.
- `preview:` — repo stacks only; wildcard domain required (`config.go:249-254`).
- `compose:` / `override:`, `env_file`, `config`, `secrets`, `domain`.
- On deploy, writes `<deployDir>/deployed_ref` = `ref@commit` (`deployer.go:315`).

### path stack (`path:`)
- Source: a directory inside the IaC repo, **symlinked** into
  `<services_dir>/<name>/repo` (`deployer.go:348`).
- `auto_deploy:` — deploy automatically on IaC push.
- `update:` — script run as a **post-deploy hook** after `compose up`
  (`deployer.go:306`). This is the migration hook (e.g. Nextcloud upgrade steps).
- `override:`, `env_file`, `config`, `secrets`, `domain`.
- **Not allowed:** `branch`/`tag`/`tag_pattern`, `preview`, `compose`
  (`config.go:263-271`).
- **No `deployed_ref` is written** — path stacks leave no record of which IaC
  commit deployed them. ❌

---

## 2. Triggers

What can cause Herald to act on a stack:

| # | Trigger | Source | Where |
|---|---------|--------|-------|
| T1 | App repo push to tracked branch | webhook | `server.go:333` |
| T2 | App repo tag push matching `tag_pattern` | webhook | `server.go:354` |
| T3 | App repo non-default branch push / PR | webhook (preview) | `server.go:390` |
| T4 | IaC repo push | webhook | `serve.go:180` |
| T5 | `herald deploy <stack>` / `--all` | manual | `cmd/deploy.go` |
| T6 | `herald sync` | manual | `cmd/sync.go` |
| T7 | `herald down <stack>` [`--volumes`] | manual | `cmd/down.go` |
| T8 | `herald preview cleanup` | manual | `cmd/preview.go` |
| T9 | Daemon startup | — | **none yet** ❌ |

---

## 3. repo stack scenarios

| # | Scenario | Today | Status |
|---|----------|-------|--------|
| R1 | First deploy (not yet on disk) | T1/T5 → clone + compose up | ✅ |
| R2 | New commit on tracked branch | T1 → fetch+reset, compose up, write `deployed_ref` | ✅ |
| R3 | Tag release matching `tag_pattern` | T2 → deploy that tag | ✅ |
| R4 | Fixed `tag:` stack, new tag pushed | `handleTagPush` requires `tag_pattern`; fixed-tag stacks do **not** auto-deploy on tag push — manual only | ⚠️ by design, easy to misread |
| R5 | Pending commits exist (remote ahead) | T6 reports "has new commits"; daemon does not | ⚠️ sync-only |
| R6 | Branch / ref changed in config | Redeploy fetch+resets to new ref; but webhook only fires on push to the **new** branch | ⚠️ needs redeploy |
| R7 | Override/compose content changed | Regenerated on next redeploy | ✅ on redeploy |
| R8 | Container stopped, deploy dir intact | T6 reports "stopped"; daemon does **not** restart (Docker owns runtime) | ✅ by design |

---

## 4. path stack scenarios

| # | Scenario | Today | Status |
|---|----------|-------|--------|
| P1 | First deploy | T4 (if `auto_deploy`) or T5 → symlink + compose up | ✅ |
| P2 | Version/config bump (edit files in subtree) — **the Nextcloud case** | T4 redeploys **every** `auto_deploy` stack regardless of whether its subtree changed | 🐛 over-deploys |
| P3 | `auto_deploy: false` stack, IaC push | Logs "updated (no auto-deploy)", never deploys | ✅ by design |
| P4 | `update:` migration hook | Runs after `compose up` | ✅ |
| P5 | Which IaC commit is this stack on? | No record written for path stacks | ❌ |
| P6 | Failed deploy, then unrelated IaC push | Failed stack's subtree unchanged → never retried (forgotten) | ⚠️ |

> P2 + P5 together are the change-detection gap. The intended behavior: on IaC
> push, redeploy a path stack only if its `path:` subtree changed between the
> deployed commit and the new HEAD. With source-of-truth = git, "deployed commit"
> should come from a per-stack `deployed_ref` (extend the existing repo-stack
> stamp to path stacks) — not a central state file.

---

## 5. Mutating an existing stack ("change a stack completely")

| # | Change | Today | Status |
|---|--------|-------|--------|
| M1 | Domain changed | Redeploy regenerates Caddy labels; cert follows | ✅ on redeploy |
| M2 | Image version bump (path) | See P2 | 🐛 |
| M3 | Secret value changed | Secrets live outside git; no trigger. Takes effect only on next redeploy. Preflight blocks deploy if a **required** secret is missing (`deployer.go:132`) | ⚠️ no auto-redeploy |
| M4 | **Rename** stack key (`nextcloud` → `cloud`) | New `cloud` deploys fresh; old `herald-nextcloud` keeps running as an **orphan**; its **volumes are stranded** (apparent data loss). `detectOrphans` reports it (`sync.go:160`); no auto-remove by design | ⚠️ footgun |
| M5 | **Flip source type** (repo ↔ path) | `symlinkSource` only handles a stale *symlink* (`deployer.go:359`); if `deployDir/repo` is a real git clone, `os.Symlink` fails. Reverse (clone into existing symlink) undefined | 🐛 dirty deploy dir |
| M6 | Add stack to config | Deploys on its trigger — **but** IaC push does not re-sync webhooks, so a new repo stack's app webhook is never registered → its pushes are ignored until manual `herald sync` | 🐛 webhook drift |
| M7 | Remove stack from config | Containers keep running as an orphan; needs manual `herald down` | ⚠️ |

---

## 6. Removal / teardown scenarios

| # | Scenario | Today | Status |
|---|----------|-------|--------|
| D1 | `herald down <stack>` | Stops containers; preserves deploy dir + volumes | ✅ |
| D2 | `herald down --volumes` | Also removes named volumes (irreversible) | ✅ |
| D3 | Removed from config, still running | Orphan; no automatic action (next.md forbids auto-remove) | ⚠️ |
| D4 | Orphan reporting | `detectOrphans` in `sync.go`; not surfaced by the daemon or status | ⚠️ sync-only |

---

## 7. Preview scenarios (repo stacks only)

| # | Scenario | Today | Status |
|---|----------|-------|--------|
| PV1 | PR opened / synchronize | Deploy preview subdomain | ✅ |
| PV2 | PR closed | Teardown | ✅ |
| PV3 | Non-default branch deleted | Teardown | ✅ |
| PV4 | **Fork** PR | Skipped — head ref/commit is attacker-controlled (`server.go:294`) | ✅ security |
| PV5 | Stale preview (branch gone, teardown missed) | `herald preview cleanup` | ✅ manual |
| PV6 | Preview count vs configured limit | next.md references limits in doctor; enforcement not confirmed here | ❓ verify |

---

## 8. Failure & recovery scenarios

| # | Scenario | Today | Status |
|---|----------|-------|--------|
| F1 | Deploy fails on missing required secret | Preflight error, stack not deployed, logged | ✅ fails safe |
| F2 | Daemon offline during pushes | On restart, **no startup maintenance pass** → missed changes not reconciled until next event or manual sync | ❌ |
| F3 | IaC pull is not fast-forward (force-push) | `PullFFOnly` fails, logged, config **not** reloaded | ⚠️ manual fix |
| F4 | Caddy not running on IaC push | `herald sync` ensures Caddy; the IaC push handler does **not** | ⚠️ |
| F5 | Repo set changed but webhooks not reconciled | IaC push handler skips webhook sync → drift | 🐛 (= M6) |

---

## 9. Gap summary (priority order)

The daemon's IaC push handler (`serve.go:180`) does only 3 of the 8 steps
`herald sync` does. Everything below flows from that drift plus missing change
detection.

1. **🐛 Webhook drift on IaC push** (M6/F5) — new repo stacks get no webhook; the
   core "push config, it deploys" promise silently breaks.
2. **🐛 Path-stack over-deploy** (P2) — every IaC push rebuilds every
   `auto_deploy` stack. Fix with subtree change detection keyed off a per-stack
   `deployed_ref`.
3. **❌ No path-stack deploy record** (P5) — needed for change detection,
   failed-deploy retry, and restart recovery.
4. **❌ No startup maintenance pass** (F2/T9) — restarts are not self-healing.
5. **⚠️ Failed deploys are forgotten** (P6) — no retry until the subtree changes
   again.
6. **🐛 Source-type flip leaves a dirty deploy dir** (M5).
7. **⚠️ Rename strands volumes** (M4) — needs clear doctor output, not
   auto-removal.
8. **⚠️ Orphans only surfaced by manual sync** (D3/D4) — daemon and status should
   report them.

Items 1–4 are Priority 1 in `next.md`. The clean fix for 1–2 is the same move:
extract the `herald sync` body into a reusable maintenance path and route the IaC
push handler (and startup) through it, with change detection scoped per stack.
