// Package doctor runs Herald's operator-facing diagnosis. It answers one question
// — "why is this server not deploying itself correctly?" — with actionable,
// severity-grouped output plus the operational inventory that the public status
// page must not expose.
//
// Unlike the maintenance pass, doctor gathers everything live (including live
// GitHub token/webhook verification). It is run rarely and deliberately, so the
// cost of live checks is acceptable; it does not lean on last-sync.json for
// freshness. It is read-only: it never pulls, deploys, creates the Caddy network,
// or generates secrets.
package doctor

import (
	"context"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/nogo/herald/internal/caddy"
	bootstrap "github.com/nogo/herald/internal/init"

	"github.com/nogo/herald/internal/config"
	githelper "github.com/nogo/herald/internal/git"
	"github.com/nogo/herald/internal/github"
	"github.com/nogo/herald/internal/maintenance"
	"github.com/nogo/herald/internal/secrets"
)

// Severity orders checks from healthy to broken.
type Severity int

const (
	SeverityOK        Severity = iota // healthy
	SeverityAttention                 // broken; needs an admin action with a known fix
	SeverityWarning                   // suspicious but not necessarily wrong
)

// Check is a single diagnosis result.
type Check struct {
	Name     string
	Severity Severity
	Detail   string // shown under the name; empty for OK checks
	Fix      string // exact command / file / explanation; empty for OK checks
}

// StackInventory is the operational record for one stack (admin-only data).
type StackInventory struct {
	Name       string
	Source     string // "owner/repo@ref" or "path:<dir>"
	Domain     string
	DeployRef  string   // raw deployed_ref stamp, or "" if never deployed
	Secrets    []string // "key → type:target" (names only, never values)
	AutoDeploy bool
	Update     string
}

// WebhookInventory is the live webhook state for one repo.
type WebhookInventory struct {
	Repo  string
	State string // "active", "inactive", "missing", or "error: ..."
}

// Diagnosis is the full doctor result: checks plus operational inventory.
type Diagnosis struct {
	Server   string
	Checks   []Check
	Stacks   []StackInventory
	Webhooks []WebhookInventory
	LastPass *maintenance.Report
}

// Deps are the inputs doctor needs. Config may be nil when the config failed to
// load; ConfigErr then carries the reason and config-dependent checks are skipped.
type Deps struct {
	DataDir    string
	Config     *config.Config
	ConfigErr  error
	Secrets    *secrets.Store
	Token      string
	IaCRepo    string
	HeraldPort int
	Logger     *slog.Logger
}

// Run executes every check and assembles the inventory. It never returns an error:
// each check records its own outcome so one failure does not abort the rest.
func Run(ctx context.Context, d Deps) *Diagnosis {
	di := &Diagnosis{}
	if d.Config != nil {
		di.Server = d.Config.Server.Name
	}

	di.checkEnvironment(ctx, d)
	di.checkSecrets(d)
	di.checkConfig(d)
	di.checkServerRepo(ctx, d)
	di.checkGitHub(ctx, d)
	di.checkCaddy(ctx, d)
	di.checkStacks(ctx, d)
	di.checkOrphans(ctx, d)

	di.buildInventory(d)
	di.LastPass, _ = maintenance.LoadReport(d.DataDir)
	return di
}

func (di *Diagnosis) add(name string, sev Severity, detail, fix string) {
	di.Checks = append(di.Checks, Check{Name: name, Severity: sev, Detail: detail, Fix: fix})
}

func (di *Diagnosis) ok(name string) { di.add(name, SeverityOK, "", "") }

func (di *Diagnosis) checkEnvironment(ctx context.Context, d Deps) {
	if v, err := bootstrap.CheckDocker(ctx); err != nil {
		di.add("docker accessible", SeverityAttention,
			"docker info failed", "install Docker and ensure your user is in the 'docker' group")
	} else {
		di.ok("docker accessible (" + v + ")")
	}

	if v, err := bootstrap.CheckDockerCompose(ctx); err != nil {
		di.add("docker compose plugin", SeverityAttention,
			"plugin not found", "install the Docker Compose plugin")
	} else {
		di.ok("docker compose plugin (" + v + ")")
	}

	if v, err := bootstrap.CheckGit(ctx); err != nil {
		di.add("git installed", SeverityAttention, "git not found", "install git")
	} else {
		di.ok("git installed (" + v + ")")
	}

	if err := bootstrap.CheckDataDir(d.DataDir); err != nil {
		di.add("data dir writable", SeverityAttention, err.Error(), "")
	} else {
		di.ok("data dir writable (" + d.DataDir + ")")
	}
}

func (di *Diagnosis) checkSecrets(d Deps) {
	if err := d.Secrets.HealthCheck(); err != nil {
		di.add("secrets store", SeverityAttention, err.Error(),
			"run herald init to create the age key, or restore the key that encrypted secrets.age")
	} else {
		di.ok("secrets store decrypts")
	}
}

func (di *Diagnosis) checkConfig(d Deps) {
	if d.ConfigErr != nil {
		di.add("config valid", SeverityAttention, d.ConfigErr.Error(),
			"fix config.yml in the server repo and push")
		return
	}
	di.ok("config valid")
}

func (di *Diagnosis) checkServerRepo(ctx context.Context, d Deps) {
	repoDir := filepath.Join(d.DataDir, "repo")
	if _, err := os.Stat(filepath.Join(repoDir, ".git")); err != nil {
		di.add("server repo present", SeverityAttention,
			"no git clone at "+repoDir, "herald init <server-repo>")
		return
	}
	if err := githelper.CmdWithAuth(ctx, d.Token, repoDir, "ls-remote", "--quiet", "origin", "HEAD").Run(); err != nil {
		di.add("server repo reachable", SeverityWarning,
			"git ls-remote failed: "+err.Error(), "check network and credentials for the server repo")
		return
	}
	di.ok("server repo present and reachable")
}

func (di *Diagnosis) checkGitHub(ctx context.Context, d Deps) {
	if d.Token == "" {
		di.add("github token", SeverityAttention,
			"no token in config or secrets store", "herald auth login")
		return
	}
	if login, err := github.GetUser(ctx, d.Token); err != nil {
		di.add("github token valid", SeverityAttention, err.Error(), "herald auth login")
	} else {
		di.ok("github token valid (" + login + ")")
	}

	if _, err := d.Secrets.Get("herald/webhook_secret"); err != nil {
		di.add("webhook secret", SeverityAttention,
			"missing from secrets store", "herald webhooks sync regenerates it")
	} else {
		di.ok("webhook secret present")
	}

	// Live webhook verification. Also fills the webhook inventory in one pass so we
	// don't call the GitHub API twice.
	if d.Config == nil {
		return
	}
	client := github.NewGitHubClient(d.Token, d.Logger)
	for _, ws := range github.ListWebhookStatuses(ctx, d.Config, client, d.IaCRepo) {
		inv := WebhookInventory{Repo: ws.Repo}
		switch {
		case ws.Error != nil:
			inv.State = "error: " + ws.Error.Error()
			di.add("webhook "+ws.Repo, SeverityWarning, ws.Error.Error(), "")
		case !ws.Found:
			inv.State = "missing"
			di.add("webhook "+ws.Repo, SeverityAttention,
				"no Herald webhook registered on GitHub", "herald webhooks sync")
		case !ws.Active:
			inv.State = "inactive"
			di.add("webhook "+ws.Repo, SeverityWarning,
				"webhook present but inactive", "herald webhooks sync")
		default:
			inv.State = "active"
			di.ok("webhook " + ws.Repo)
		}
		di.Webhooks = append(di.Webhooks, inv)
	}
}

func (di *Diagnosis) checkCaddy(ctx context.Context, d Deps) {
	mgr := &caddy.CaddyManager{Config: d.Config, Logger: d.Logger, HeraldPort: d.HeraldPort}
	if running, err := mgr.IsRunning(ctx); err != nil {
		di.add("caddy running", SeverityWarning, "could not query Docker: "+err.Error(), "")
	} else if !running {
		di.add("caddy running", SeverityAttention,
			"herald-caddy container is not running", "herald caddy start (or restart herald.service)")
	} else {
		di.ok("caddy running")
	}

	if !caddy.NetworkExists(ctx) {
		di.add("caddy network", SeverityAttention,
			"docker network 'caddy' does not exist", "herald caddy start creates it")
	} else {
		di.ok("caddy network exists")
	}
}

func (di *Diagnosis) checkStacks(ctx context.Context, d Deps) {
	if d.Config == nil {
		return
	}
	for _, name := range slices.Sorted(maps.Keys(d.Config.Stacks)) {
		stack := d.Config.Stacks[name]

		// Required secrets — checked even when undeployed, so a stack blocked on
		// secrets surfaces its fix before first deploy.
		if missing, err := d.Secrets.MissingRequired(stack.Secrets); err != nil {
			di.add(name+": secrets", SeverityWarning, "could not check: "+err.Error(), "")
		} else if len(missing) > 0 {
			di.add(name+": missing required secret", SeverityAttention,
				strings.Join(missing, ", "), "herald secret set "+missing[0])
		}

		deployDir := filepath.Join(d.Config.Server.ServicesDir, name)
		if _, err := os.Stat(deployDir); os.IsNotExist(err) {
			di.add(name+": not deployed", SeverityAttention,
				"no deploy directory — first deploy is manual", "herald deploy "+name)
			continue
		}
		if maintenance.StackRunning(ctx, name) {
			di.ok(name + ": running")
		} else {
			di.add(name+": stopped", SeverityWarning,
				"deploy directory exists but no containers are running",
				"docker compose -p herald-"+name+" ps")
		}
	}
}

func (di *Diagnosis) checkOrphans(ctx context.Context, d Deps) {
	if d.Config == nil {
		return
	}
	for _, project := range maintenance.DetectOrphans(ctx, d.Config) {
		name := strings.TrimPrefix(project, "herald-")
		di.add(project+": orphan", SeverityWarning,
			"running but not present in config",
			"inspect: docker compose -p "+project+" ps  ·  remove: herald down "+name)
	}
}

func (di *Diagnosis) buildInventory(d Deps) {
	if d.Config == nil {
		return
	}
	for _, name := range slices.Sorted(maps.Keys(d.Config.Stacks)) {
		stack := d.Config.Stacks[name]
		inv := StackInventory{Name: name, Domain: stack.Domain}
		if stack.Repo != "" {
			ref := stack.Branch
			if stack.Tag != "" {
				ref = "tag:" + stack.Tag
			} else if stack.TagPattern != "" {
				ref = "tag:" + stack.TagPattern
			}
			inv.Source = stack.Repo + "@" + ref
		} else {
			inv.Source = "path:" + stack.Path
			inv.AutoDeploy = stack.AutoDeploy
			inv.Update = stack.UpdateScript
		}
		inv.DeployRef = readDeployRef(filepath.Join(d.Config.Server.ServicesDir, name))
		for _, s := range stack.Secrets {
			inv.Secrets = append(inv.Secrets, s.Key+" → "+s.Type+":"+s.Target)
		}
		di.Stacks = append(di.Stacks, inv)
	}
}

// readDeployRef returns the raw deployed_ref stamp ("main@abc123" or
// "path@def456"), or "" if the stack was never deployed.
func readDeployRef(deployDir string) string {
	data, err := os.ReadFile(filepath.Join(deployDir, "deployed_ref"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
