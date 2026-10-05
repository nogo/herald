# Coding standards

Base: [Effective Go](https://go.dev/doc/effective_go) and [Go Code Review Comments](https://go.dev/wiki/CodeReviewComments). Where this file is silent, they apply.

What herald is, its package map and its invariants live in [docs/architecture.md](docs/architecture.md) and [docs/project.md](docs/project.md). This file covers how the Go is written.

## Check

`mise run check` — vet, gofmt check, `go mod tidy` diff, `go test -race -count=1 ./...`, staticcheck and the package boundary check. CI runs the same command before a release. Must pass before a change is done.

## Measure

Tools run through `go run` at a pinned version, so nothing needs installing.

- Cognitive complexity: `go run github.com/uudashr/gocognit/cmd/gocognit@v1.2.2 -top 20 ./internal/<pkg>/`
- Duplication: `go run github.com/mibk/dupl@v1.1.0 -t 100 ./internal ./cmd`
- Mutation score for one package: `go run github.com/go-gremlins/gremlins/cmd/gremlins@v0.6.0 unleash ./internal/<pkg>`
- Flakiness: `go test -race -count=20 -shuffle=on ./internal/<pkg>/`
- Hotspots: `git log --since=3.months --name-only --format= -- '*.go' | sort | uniq -c | sort -rn | head -20`

## Go

### Structure
- A package belongs to one layer in docs/architecture.md: core, supporting or generic. Supporting packages (`compose`, `caddy`, `secrets`, `github`) never import each other, except `github` → `secrets`. Generic packages (`runner`, `git`, `ui`) import nothing from `internal/`.
- The deploy pipeline lives once, in `deployer`. Preview, webhook and maintenance call into it; they never repeat a stage.
- A deployment's paths, compose project name and stamps come from `deployer.Instance`. Nothing else joins `"repo"`, `".env"` or `"herald-"`.
- The data dir's paths (server repo clone, its `config.yml`, bare repo, post-receive hook) come from `config.DataDir`. Nothing else joins `"repo"` or `"server.git"` onto the data dir.
- `cmd/` parses flags, wires dependencies and prints output. Rules about stacks, previews or secrets go in `internal/`.
- Concrete types by default. An interface is declared by its consumer, holds only the methods that consumer calls, and exists only once a second implementation exists. A test fake counts (`maintenance.stackDeployer`).
- Dependencies arrive as exported struct fields or constructor arguments. A field that may be nil is documented on the field and falls back in one accessor (`Deployer.ui()` returns `ui.Nop()`).
- Logic reads no environment variables. The config loader resolves env references; everything else gets its values from `config.Config`.

### Naming
- Packages: one short lowercase word, named for what they provide (`secrets`, `preview`), never `util` or `common`.
- Name the domain, not the mechanism: a stack is a stack in every package. Herald dropped app/service once; don't bring back synonyms.
- Test names: `TestThing` or `TestThing_Condition` for one behaviour (`TestRemove_ComposeDownFailurePreservesState`). The name says what must hold.

### Errors
- Wrap with `%w` and a lowercase gerund for the step that failed: `fmt.Errorf("loading config: %w", err)`. Never `%v` on an error.
- Errors the operator sees say what to do next (`"docker is not installed or not accessible; install Docker and …"`). Config validation errors name the key.
- A failure that would leave state inconsistent is returned, not logged. A step that may fail without harm is logged at Warn, with a comment saying why it is best-effort (see `preview.Remove`).
- An async API reports "submitted", not "succeeded". `DeployAsync` returning true doesn't mean the deploy worked; callers don't report it as success.
- Sentinel errors (`ErrLimiterClosed`) and error types (`GitHubError`) only when a caller branches on them. Compare with `errors.Is` / `errors.As`.
- No `panic` except for programmer errors at init: a broken embedded template, a bad `regexp.MustCompile`.

### Concurrency and state
- A long-running component takes its config as a `*config.Live` and calls `Load()` on every use. Never keep a `*config.Config` that outlives one request or deploy: a reload must reach every component without a restart.
- Per-key serialisation uses one `sync.Map` of `*sync.Mutex` and `LoadOrStore` (stack locks, preview op locks). A mutex field carries a comment saying what it guards.
- State files are written to a temp file in the same directory, then `os.Rename`d. Never write the target in place.
- Every external command gets a context: `exec.CommandContext`. Use `runner.RunCmd` / `RunCmdStream` when output goes to logs or the UI.
- An `io.Writer` from `ui.UI` may be nil (`ui.Nop`). Code that writes to one checks for nil first.

### Tests
- Tests check behaviour through the package's interface. `package x` internal tests are fine for unexported pure helpers; don't assert on struct internals a caller can't see.
- Table-driven with `t.Run` once there are several input cases; one test function per behaviour otherwise.
- Filesystem state uses `t.TempDir()`; env uses `t.Setenv`. No writes outside the temp dir, no network.
- External binaries (`docker`, `git`) are faked by putting a script on `PATH` (`installFakeDocker`), not by putting a Go interface in front of `exec`.
- Fixtures live in `testdata/` next to the test (`internal/config/testdata/`).
- stdlib `testing` only: `t.Errorf` / `t.Fatalf` with got/want messages. No testify, no mock generators.
- A bug fix starts with a test that fails for that bug's reason: the recent preview, limiter and sync fixes each landed with one.
- Never a fixed `time.Sleep` as the wait. Wait on a channel or `WaitGroup`, or poll a condition until a deadline and fail with a message (`waitForFile`).

### Dependencies
- The stdlib first. Current direct dependencies: cobra, age, x/term, yaml.v3. A new one needs a reason the stdlib can't meet, and `go mod tidy` must leave go.mod clean.
- Pure Go, no CGO: herald ships as one static binary.
- Logging is `log/slog`. CLI progress goes through `ui.UI`, never `fmt.Println` from `internal/`.

## Overrides

- **Comments carry the why, not just the what.** This repo comments more than Go usually does: exported fields and non-obvious branches explain the operational reason (daemon vs CLI, why best-effort). Keep that density, and update the comment in the same edit as its code. Herald runs unattended on real servers, and the next reader is usually debugging from a log line.
- **Two-adapter test, applied to `exec`:** a fake binary on `PATH` counts as the second adapter for `docker`/`git`. Don't add a Go interface around command execution to make it testable.
