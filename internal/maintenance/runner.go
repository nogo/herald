// Package maintenance runs Herald's reconcile pass: pull the IaC repo, reload and
// validate config, ensure Caddy, reconcile webhooks (create/repair/prune), survey
// stacks, redeploy changed auto_deploy path stacks, and report drift. The same
// pass backs `herald sync`, daemon startup, and the IaC push handler, so behavior
// cannot drift between them. It does not depend on Cobra or stdout; callers render
// the returned Report.
package maintenance

import (
	"context"
	"encoding/json"
	"log/slog"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nogo/herald/internal/caddy"
	"github.com/nogo/herald/internal/config"
	"github.com/nogo/herald/internal/deployer"
	githelper "github.com/nogo/herald/internal/git"
	"github.com/nogo/herald/internal/github"
	"github.com/nogo/herald/internal/secrets"
	"github.com/nogo/herald/internal/status"
)

// Reconcile controls webhook reconciliation behavior for a pass.
type Reconcile int

const (
	ReconcileOff   Reconcile = iota // skip webhook sync
	ReconcileDelta                  // reconcile only when the desired repo set changed
	ReconcileFull                   // always reconcile (create, repair, prune)
)

// Options carry per-context behavior. The same Runner serves manual sync, daemon
// startup, and the IaC push handler; only the Options differ.
type Options struct {
	Pull            bool      // pull the IaC repo before reloading config
	Webhooks        Reconcile // webhook reconciliation mode
	RedeployChanged bool      // redeploy changed auto_deploy path stacks
	BlockOnDeploys  bool      // run deploys synchronously (CLI) instead of async (daemon)
}

// Runner performs a maintenance pass. Run is single-flight: overlapping triggers
// (e.g. back-to-back IaC pushes) coalesce so the config swap and deploy dispatch
// never race.
type Runner struct {
	DataDir    string
	Logger     *slog.Logger
	Secrets    *secrets.Store
	Deployer   *deployer.Deployer
	Live       *atomic.Pointer[config.Config] // authoritative config, published on reload
	Reload     func() (*config.Config, error) // reload + validate config from disk
	IaCRepo    string                         // GitHub full name of the server IaC repo, or ""
	HeraldPort int

	mu sync.Mutex
}

// Run executes the maintenance pass and returns a Report. It never returns an
// error: every step records its outcome in the Report so a single failure does
// not abort the rest. The only hard gate is an invalid config, which blocks
// deploys while leaving existing wiring untouched.
func (r *Runner) Run(ctx context.Context, opts Options) *Report {
	r.mu.Lock()
	defer r.mu.Unlock()

	rep := &Report{StartedAt: time.Now().UTC()}
	defer func() {
		rep.FinishedAt = time.Now().UTC()
		if err := rep.Write(r.DataDir); err != nil {
			r.Logger.Warn("writing maintenance report", "error", err)
		}
	}()

	repoDir := filepath.Join(r.DataDir, "repo")

	// Phase A1: pull the IaC repo (recovery — the daemon may have missed pushes).
	rep.IaC.OldHEAD = gitHEAD(ctx, repoDir)
	if opts.Pull {
		if _, err := os.Stat(filepath.Join(repoDir, ".git")); err == nil {
			if out, err := githelper.PullFFOnly(ctx, r.token(), repoDir); err != nil {
				rep.IaC.Error = strings.TrimSpace(out)
				if rep.IaC.Error == "" {
					rep.IaC.Error = err.Error()
				}
			} else {
				rep.IaC.Pulled = true
			}
		}
	}
	rep.IaC.NewHEAD = gitHEAD(ctx, repoDir)

	// Phase A2: reload + validate config. Apply only if valid; a broken push must
	// not take down wiring or trigger deploys.
	cfg := r.Live.Load()
	configOK := true
	if newCfg, err := r.Reload(); err != nil {
		rep.Config.Error = err.Error()
		configOK = false
	} else {
		cfg = newCfg
		r.Live.Store(newCfg)
		r.Deployer.Config = newCfg // keep the static field consistent for any non-live read
		rep.Config.Loaded = true
	}

	// Phase B1: ensure Caddy is running, protect its ACME account, survey TLS.
	rep.Caddy = r.ensureCaddy(ctx, cfg)
	if strings.HasPrefix(rep.Caddy, "running") || strings.HasPrefix(rep.Caddy, "started") {
		r.syncACMEAccount(ctx, rep)
		rep.Certificates = surveyCertificates(ctx)
	}

	// Phase B2: reconcile webhooks (create/repair/prune).
	r.reconcileWebhooks(ctx, cfg, opts, rep)

	// Phase C: survey and act per stack.
	r.surveyStacks(ctx, cfg, opts, configOK, rep)

	// Phase D: orphan detection (report-only).
	rep.Orphans = DetectOrphans(ctx, cfg)

	return rep
}

// token resolves the GitHub token from the live config, falling back to the
// secrets store.
func (r *Runner) token() string {
	if cfg := r.Live.Load(); cfg != nil && cfg.Server.GithubToken != "" {
		return cfg.Server.GithubToken
	}
	if token, err := r.Secrets.Get("herald/github_token"); err == nil {
		return token
	}
	return ""
}

func (r *Runner) ensureCaddy(ctx context.Context, cfg *config.Config) string {
	mgr := &caddy.CaddyManager{Config: cfg, Logger: r.Logger, HeraldPort: r.HeraldPort}
	running, err := mgr.IsRunning(ctx)
	if err != nil {
		return "error checking: " + err.Error()
	}
	if running {
		return "running"
	}
	if err := mgr.Start(ctx); err != nil {
		return "failed to start: " + err.Error()
	}
	return "started"
}

// acmeAccountKey is where the backup of Caddy's ACME account lives in the
// secrets store.
const acmeAccountKey = "herald/caddy_acme_account"

// syncACMEAccount keeps a copy of Caddy's ACME account outside the caddy_data
// volume, and restores it when the volume comes up empty. The account key exists
// nowhere else: lose the volume and Caddy silently registers a new account, which
// invalidates any CAA accounturi pin and re-issues every certificate at once —
// straight into Let's Encrypt's per-domain weekly limit.
//
// This protects against volume loss, not host loss: the backup lands in the
// secrets store under the data dir, so a full host rebuild still needs that
// restored from wherever you keep it.
func (r *Runner) syncACMEAccount(ctx context.Context, rep *Report) {
	live, err := caddy.ExportACMEAccount(ctx)
	if err != nil {
		rep.addErr("reading Caddy ACME account: %v", err)
		return
	}
	backup, _ := r.Secrets.Get(acmeAccountKey)

	if len(live) == 0 {
		if backup == "" {
			return // nothing issued yet; nothing to protect
		}
		r.Logger.Warn("caddy has no ACME account but a backup exists; restoring",
			"hint", "the caddy_data volume was recreated")
		if err := caddy.ImportACMEAccount(ctx, backup); err != nil {
			rep.addErr("restoring Caddy ACME account: %v", err)
			return
		}
		// Not an error, but the operator must see it: the volume was recreated.
		rep.Caddy += " (ACME account restored from backup — caddy_data had been recreated)"
		return
	}

	if live != backup {
		if err := r.Secrets.Set(acmeAccountKey, live); err != nil {
			rep.addErr("backing up Caddy ACME account: %v", err)
			return
		}
		r.Logger.Info("backed up Caddy ACME account to the secrets store")
	}
}

// surveyCertificates records TLS health so a failing renewal shows up in the
// pass — and therefore in last-sync.json — instead of only when someone thinks
// to run doctor.
func surveyCertificates(ctx context.Context) CertResult {
	var res CertResult

	certs, err := caddy.ListCertificates(ctx)
	if err != nil {
		res.Error = err.Error()
	}
	res.Total = len(certs)
	for _, c := range certs {
		if res.NextExpiry.IsZero() || c.NotAfter.Before(res.NextExpiry) {
			res.NextExpiry = c.NotAfter
		}
		switch {
		case c.Expired():
			res.Expired = append(res.Expired, c.Name())
		case c.Remaining() < caddy.ExpiryThreshold:
			res.Expiring = append(res.Expiring, c.Name())
		}
	}

	if rerr, err := caddy.RecentRenewalError(ctx, caddy.RenewalErrorWindow); err == nil && rerr != nil {
		res.RenewalError = rerr.Detail
		if rerr.Identifier != "" {
			res.RenewalError = rerr.Identifier + ": " + rerr.Detail
		}
	}
	return res
}

func (r *Runner) reconcileWebhooks(ctx context.Context, cfg *config.Config, opts Options, rep *Report) {
	if opts.Webhooks == ReconcileOff || cfg.Server.GithubToken == "" {
		rep.Webhooks.Skipped = true
		return
	}

	statePath := status.WebhookStatePath(r.DataDir)
	prev, err := status.LoadWebhookState(statePath)
	if err != nil {
		rep.Webhooks.Error = err.Error()
		return
	}
	known := make(map[string]int64, len(prev.Repos))
	for repo, e := range prev.Repos {
		known[repo] = e.ID
	}

	desired := desiredRepoSet(cfg, r.IaCRepo)
	if opts.Webhooks == ReconcileDelta && sameRepoSet(known, desired) {
		rep.Webhooks.Skipped = true
		return
	}

	client := github.NewGitHubClient(cfg.Server.GithubToken, r.Logger)
	results, current, err := github.ReconcileWebhooks(ctx, cfg, r.Secrets, client, false, r.IaCRepo, known)
	if err != nil {
		rep.Webhooks.Error = err.Error()
		return
	}

	for _, res := range results {
		switch res.Action {
		case "exists":
			rep.Webhooks.Synced++
		case "created":
			rep.Webhooks.Created++
		case "pruned":
			rep.Webhooks.Pruned++
		case "error":
			rep.Webhooks.Errors++
			rep.addErr("webhook %s: %v", res.Repo, res.Error)
		}
	}

	ws := &status.WebhookState{SyncedAt: time.Now().UTC(), Repos: make(map[string]status.WebhookEntry, len(current))}
	for repo, id := range current {
		ws.Repos[repo] = status.WebhookEntry{ID: id, Registered: true}
	}
	if err := status.SaveWebhookState(statePath, ws); err != nil {
		rep.addErr("saving webhook state: %v", err)
	}
}

func (r *Runner) surveyStacks(ctx context.Context, cfg *config.Config, opts Options, configOK bool, rep *Report) {
	repoDir := filepath.Join(r.DataDir, "repo")
	missingByStack := map[string][]string{}

	for _, name := range slices.Sorted(maps.Keys(cfg.Stacks)) {
		stack := cfg.Stacks[name]
		sr := StackReport{Name: name, Source: "path", Action: "none"}
		if stack.Repo != "" {
			sr.Source = "repo"
		}

		deployDir := filepath.Join(cfg.Server.ServicesDir, name)
		if _, err := os.Stat(deployDir); os.IsNotExist(err) {
			sr.State = "not deployed"
			rep.Stacks = append(rep.Stacks, sr)
			continue
		}

		if StackRunning(ctx, name) {
			sr.State = "running"
		} else {
			sr.State = "stopped"
		}

		missing, merr := r.Secrets.MissingRequired(stack.Secrets)
		if merr != nil {
			rep.addErr("stack %q: checking secrets: %v", name, merr)
		} else if len(missing) > 0 {
			sr.MissingSecrets = missing
			missingByStack[name] = missing
		}

		// config.yml can change a stack without its source moving — a `domain:` edit
		// being the case that silently breaks TLS, since the running container keeps
		// its old caddy label and the new domain never gets a certificate.
		sr.ConfigDrift = deployer.ConfigDrifted(deployDir, stack)

		// The only automated deploy: a changed auto_deploy path stack with secrets
		// satisfied and a valid config. Everything else is report-only.
		if configOK && opts.RedeployChanged && stack.Path != "" && stack.AutoDeploy {
			if len(missing) > 0 {
				sr.Action = "blocked"
			} else {
				recorded := deployer.ReadDeployedIaCCommit(deployDir)
				changed, cerr := pathStackChanged(ctx, repoDir, recorded, stack.Path)
				if cerr != nil {
					r.Logger.Warn("path change detection failed; redeploying to be safe",
						"stack", name, "error", cerr)
					changed = true
				}
				if changed || sr.ConfigDrift {
					r.Logger.Info("maintenance: redeploying changed path stack",
						"stack", name, "config_drift", sr.ConfigDrift)
					r.deploy(ctx, name, opts.BlockOnDeploys)
					sr.Action = "redeployed"
					sr.ConfigDrift = false
				}
			}
		} else if len(missing) > 0 {
			sr.Action = "blocked"
		} else if sr.ConfigDrift {
			// Repo stacks and manual path stacks are never auto-deployed, so drift is
			// reported rather than acted on — same policy as before, minus the silence.
			sr.Action = "config drift"
		}

		rep.Stacks = append(rep.Stacks, sr)
	}

	if len(missingByStack) > 0 {
		rep.MissingSecrets = missingByStack
	}
}

func (r *Runner) deploy(ctx context.Context, name string, block bool) {
	if block {
		if err := r.Deployer.Deploy(ctx, name, ""); err != nil {
			r.Logger.Error("deploy failed", "stack", name, "error", err)
		}
		return
	}
	r.Deployer.DeployAsync(name, "")
}

// gitHEAD returns the short HEAD commit of repoDir, or "" if unavailable.
func gitHEAD(ctx context.Context, repoDir string) string {
	out, err := githelper.CmdWithAuth(ctx, "", repoDir, "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// pathStackChanged reports whether anything under subpath changed between
// fromCommit and HEAD in repoDir. An empty fromCommit (no deploy record) counts
// as changed so the stack deploys once and establishes its stamp.
func pathStackChanged(ctx context.Context, repoDir, fromCommit, subpath string) (bool, error) {
	if fromCommit == "" {
		return true, nil
	}
	cmd := githelper.CmdWithAuth(ctx, "", repoDir, "diff", "--quiet", fromCommit+"..HEAD", "--", subpath)
	err := cmd.Run()
	if err == nil {
		return false, nil
	}
	var exitErr *exec.ExitError
	if ok := asExitError(err, &exitErr); ok && exitErr.ExitCode() == 1 {
		return true, nil // exit 1 = differences found
	}
	return false, err // bad commit, unknown path, etc.
}

func asExitError(err error, target **exec.ExitError) bool {
	if e, ok := err.(*exec.ExitError); ok {
		*target = e
		return true
	}
	return false
}

// StackRunning reports whether the stack's compose project has running containers.
func StackRunning(ctx context.Context, name string) bool {
	out, err := exec.CommandContext(ctx, "docker", "compose",
		"-p", "herald-"+name, "ps", "--format", "json").Output()
	trimmed := strings.TrimSpace(string(out))
	return err == nil && trimmed != "" && trimmed != "[]"
}

// desiredRepoSet returns the set of repos that should have a Herald webhook:
// every repo-sourced stack plus the IaC repo.
func desiredRepoSet(cfg *config.Config, iacRepo string) map[string]bool {
	set := map[string]bool{}
	for _, stack := range cfg.Stacks {
		if stack.Repo != "" {
			set[stack.Repo] = true
		}
	}
	if iacRepo != "" {
		set[iacRepo] = true
	}
	return set
}

func sameRepoSet(known map[string]int64, desired map[string]bool) bool {
	if len(known) != len(desired) {
		return false
	}
	for repo := range desired {
		if _, ok := known[repo]; !ok {
			return false
		}
	}
	return true
}

// DetectOrphans lists Docker Compose projects with the "herald-" prefix that are
// not accounted for by the current config. Report-only: maintenance never removes
// containers.
func DetectOrphans(ctx context.Context, cfg *config.Config) []string {
	type projectInfo struct {
		Name string `json:"Name"`
	}
	out, err := exec.CommandContext(ctx, "docker", "compose", "ls", "--format", "json", "--all").Output()
	if err != nil {
		return nil
	}
	var projects []projectInfo
	if err := json.Unmarshal(out, &projects); err != nil {
		return nil
	}
	known := map[string]bool{"herald-caddy": true}
	for name := range cfg.Stacks {
		known["herald-"+name] = true
	}
	var orphans []string
	for _, p := range projects {
		if !strings.HasPrefix(p.Name, "herald-") || strings.HasPrefix(p.Name, "herald-preview-") {
			continue
		}
		if !known[p.Name] {
			orphans = append(orphans, p.Name)
		}
	}
	return orphans
}
