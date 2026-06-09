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
- 🔧 resolved by the maintenance-pass work (commit `security-hardening`)

> **Update:** the `internal/maintenance` pass now backs `herald sync`, daemon
> startup, and the IaC push handler. Items marked 🔧 below were closed by it. See
> the gap summary for what remains.

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
- Writes `<deployDir>/deployed_ref` = `path@<iacCommit>` recording the IaC commit
  it was deployed from, so a later pass can detect subtree changes. 🔧

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
| P2 | Version/config bump (edit files in subtree) — **the Nextcloud case** | Pass redeploys an `auto_deploy` stack only when its `path:` subtree changed since `deployed_ref` (`git diff <recorded>..HEAD -- <path>`) | 🔧 |
| P3 | `auto_deploy: false` stack, IaC push | Reported as updated, never auto-deploys | ✅ by design |
| P4 | `update:` migration hook | Runs after `compose up` | ✅ |
| P5 | Which IaC commit is this stack on? | `deployed_ref` now records `path@<iacCommit>` | 🔧 |
| P6 | Failed deploy, then unrelated IaC push | Detection is "since last *successful* deploy" (the recorded commit), so a failed stack stays flagged as changed until it deploys | 🔧 |

> Change detection is keyed off the per-stack `deployed_ref` (git as source of
> truth), not a central state file. A missing record means deploy-once to
> establish the stamp.

---

## 5. Mutating an existing stack ("change a stack completely")

| # | Change | Today | Status |
|---|--------|-------|--------|
| M1 | Domain changed | Redeploy regenerates Caddy labels; cert follows | ✅ on redeploy |
| M2 | Image version bump (path) | See P2 | 🔧 |
| M3 | Secret value changed | Secrets live outside git; no trigger. Takes effect only on next redeploy. Preflight blocks deploy if a **required** secret is missing (`deployer.go:132`) | ⚠️ no auto-redeploy |
| M4 | **Rename** stack key (`nextcloud` → `cloud`) | New `cloud` deploys fresh; old `herald-nextcloud` keeps running as an **orphan**; its **volumes are stranded** (apparent data loss). The maintenance pass reports it as an orphan; no auto-remove by design | ⚠️ footgun |
| M5 | **Flip source type** (repo ↔ path) | `symlinkSource` only handles a stale *symlink* (`deployer.go:359`); if `deployDir/repo` is a real git clone, `os.Symlink` fails. Reverse (clone into existing symlink) undefined | 🐛 dirty deploy dir |
| M6 | Add stack to config | IaC push now runs the maintenance pass, which reconciles webhooks (registers the new repo's hook) and refreshes the live config the webhook matcher reads. First deploy still manual | 🔧 |
| M7 | Remove stack from config | Containers keep running as an orphan; needs manual `herald down` | ⚠️ |

---

## 6. Removal / teardown scenarios

| # | Scenario | Today | Status |
|---|----------|-------|--------|
| D1 | `herald down <stack>` | Stops containers; preserves deploy dir + volumes | ✅ |
| D2 | `herald down --volumes` | Also removes named volumes (irreversible) | ✅ |
| D3 | Removed from config, still running | Orphan; no automatic action (next.md forbids auto-remove) | ⚠️ |
| D4 | Orphan reporting | `detectOrphans` now lives in the maintenance pass and is written to `last-sync.json` by `sync`, startup, and IaC push. Status web UI does not yet read it | ⚠️ partial |

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
| F2 | Daemon offline during pushes | `herald serve` runs a startup maintenance pass (pull, reconcile, redeploy changed path stacks) → self-healing restarts | 🔧 |
| F3 | IaC pull is not fast-forward (force-push) | `PullFFOnly` fails; pass records the error, keeps last-good config, continues reconciling | ⚠️ manual fix |
| F4 | Caddy not running on IaC push | IaC push runs the full pass, which ensures Caddy | 🔧 |
| F5 | Repo set changed but webhooks not reconciled | IaC push reconciles webhooks (create + prune); a stack whose `repo:` changed has its old hook deleted | 🔧 (= M6) |

---

## 9. Gap summary

### Resolved by the maintenance pass

`herald sync`, daemon startup, and the IaC push handler now share one
single-flight `internal/maintenance` pass (validate-before-apply, per-context
`Options`). The config swap is race-free via a shared `atomic.Pointer`.

1. **🔧 Webhook drift on IaC push** (M6/F5) — the pass reconciles webhooks:
   creates for new repos and **prunes** stale hooks when a stack is removed or its
   `repo:` changes.
2. **🔧 Path-stack over-deploy** (P2) — subtree change detection keyed off the
   per-stack `deployed_ref`; only changed `auto_deploy` path stacks redeploy.
3. **🔧 Path-stack deploy record** (P5) — `deployed_ref` now records `path@<commit>`.
4. **🔧 Startup maintenance pass** (F2) — restarts recover missed pushes.
5. **🔧 Failed deploys retried** (P6) — detection is "since last *successful*
   deploy", so a failed stack stays flagged until it deploys.

### Remaining

6. **🐛 Source-type flip leaves a dirty deploy dir** (M5) — untouched; separate
   deployer correctness bug.
7. **⚠️ Rename strands volumes** (M4) — needs clear `doctor` output, not
   auto-removal.
8. **⚠️ Orphans not in the status web UI** (D3/D4) — detected and written to
   `last-sync.json`, but the web UI does not yet read it.
9. **Repo-stack `auto_deploy` gate** — decided not to build; tags + preflight cover
   the cases. Revisit only if a deploy-*freeze* need appears (likely as `herald
   freeze`, not a config field). `auto_deploy` on a repo stack is currently a silent
   no-op.
10. **❓ Preview count limit** (PV6) — enforcement unverified.

Next up per `next.md`: read-side fixes (status reads `last-sync.json`, renders path
stacks), the public availability badge, and `herald doctor` built on the report.
