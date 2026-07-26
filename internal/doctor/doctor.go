// Package doctor runs Herald's operator-facing diagnosis. It answers one question
// — "why is this server not deploying itself correctly?" — with actionable output
// grouped by category, plus the operational inventory that the public status page
// must not expose.
//
// Unlike the maintenance pass, doctor gathers everything live (including live
// GitHub token/webhook verification). It is run rarely and deliberately, so the
// cost of live checks is acceptable; it does not lean on last-sync.json for
// freshness. It is read-only: it never pulls, deploys, creates the Caddy network,
// or generates secrets.
package doctor

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	bootstrap "github.com/nogo/herald/internal/init"

	"github.com/nogo/herald/internal/caddy"
	"github.com/nogo/herald/internal/config"
	"github.com/nogo/herald/internal/deployer"
	githelper "github.com/nogo/herald/internal/git"
	"github.com/nogo/herald/internal/github"
	"github.com/nogo/herald/internal/maintenance"
	"github.com/nogo/herald/internal/secrets"
)

// Check categories, in display order.
const (
	catSystem   = "System"
	catRepo     = "Repo & config"
	catGitHub   = "GitHub"
	catWebhooks = "Webhooks"
	catCaddy    = "Caddy"
	catStacks   = "Stacks"
)

var categoryOrder = []string{catSystem, catRepo, catGitHub, catWebhooks, catCaddy, catStacks}

// Severity orders checks from healthy to broken.
type Severity int

const (
	SeverityOK        Severity = iota // healthy
	SeverityAttention                 // broken; needs an admin action with a known fix
	SeverityWarning                   // suspicious but not necessarily wrong
)

// Check is a single diagnosis result. Label is a short name used both in the
// grouped healthy view and the problem list; Detail/Fix appear only for problems.
type Check struct {
	Category string
	Label    string
	Severity Severity
	Detail   string
	Fix      string
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
	Server    string
	StatusURL string // public status page URL, or "" if no deploy domain
	Checks    []Check
	Stacks    []StackInventory
	Webhooks  []WebhookInventory
	LastPass  *maintenance.Report
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
	di.checkConfigAndRepo(ctx, d)
	di.checkGitHub(ctx, d)
	di.checkCaddy(ctx, d)
	di.checkStacks(ctx, d)
	di.checkOrphans(ctx, d)

	di.buildInventory(d)
	di.LastPass, _ = maintenance.LoadReport(d.DataDir)
	return di
}

func (di *Diagnosis) pass(category, label string) {
	di.Checks = append(di.Checks, Check{Category: category, Label: label, Severity: SeverityOK})
}

func (di *Diagnosis) fail(category, label, detail, fix string) {
	di.Checks = append(di.Checks, Check{Category: category, Label: label, Severity: SeverityAttention, Detail: detail, Fix: fix})
}

func (di *Diagnosis) warn(category, label, detail, fix string) {
	di.Checks = append(di.Checks, Check{Category: category, Label: label, Severity: SeverityWarning, Detail: detail, Fix: fix})
}

func (di *Diagnosis) checkEnvironment(ctx context.Context, d Deps) {
	if v, err := bootstrap.CheckDocker(ctx); err != nil {
		di.fail(catSystem, "docker", "docker info failed",
			"install Docker and ensure your user is in the 'docker' group")
	} else {
		// CheckDocker returns "Docker 29.5.2"; trim the redundant prefix.
		di.pass(catSystem, "docker "+strings.TrimPrefix(v, "Docker "))
	}

	if v, err := bootstrap.CheckDockerCompose(ctx); err != nil {
		di.fail(catSystem, "compose", "plugin not found", "install the Docker Compose plugin")
	} else {
		di.pass(catSystem, "compose "+v)
	}

	if v, err := bootstrap.CheckGit(ctx); err != nil {
		di.fail(catSystem, "git", "git not found", "install git")
	} else {
		di.pass(catSystem, "git "+v)
	}

	if err := bootstrap.CheckDataDir(d.DataDir); err != nil {
		di.fail(catSystem, "data dir", err.Error(), "")
	} else {
		di.pass(catSystem, "data dir")
	}

	if err := d.Secrets.HealthCheck(); err != nil {
		di.fail(catSystem, "secrets store", err.Error(),
			"run herald init to create the age key, or restore the key that encrypted secrets.age")
	} else {
		di.pass(catSystem, "secrets store")
	}
}

func (di *Diagnosis) checkConfigAndRepo(ctx context.Context, d Deps) {
	repoDir := filepath.Join(d.DataDir, "repo")
	if _, err := os.Stat(filepath.Join(repoDir, ".git")); err != nil {
		di.fail(catRepo, "server repo", "no git clone at "+repoDir, "herald init <server-repo>")
	} else if err := githelper.CmdWithAuth(ctx, d.Token, repoDir, "ls-remote", "--quiet", "origin", "HEAD").Run(); err != nil {
		di.warn(catRepo, "server repo", "git ls-remote failed: "+err.Error(),
			"check network and credentials for the server repo")
	} else {
		di.pass(catRepo, "server repo")
	}

	if d.ConfigErr != nil {
		di.fail(catRepo, "config", d.ConfigErr.Error(), "fix config.yml in the server repo and push")
	} else {
		di.pass(catRepo, "config")
	}
}

func (di *Diagnosis) checkGitHub(ctx context.Context, d Deps) {
	if d.Token == "" {
		di.fail(catGitHub, "token", "no token in config or secrets store", "herald auth login")
		return
	}
	if login, err := github.GetUser(ctx, d.Token); err != nil {
		di.fail(catGitHub, "token", err.Error(), "herald auth login")
	} else {
		di.pass(catGitHub, "token ("+login+")")
	}

	if _, err := d.Secrets.Get("herald/webhook_secret"); err != nil {
		di.fail(catGitHub, "webhook secret", "missing from secrets store", "herald webhooks sync regenerates it")
	} else {
		di.pass(catGitHub, "webhook secret")
	}

	// Live webhook verification, reused to fill the webhook inventory in one pass.
	if d.Config == nil {
		return
	}
	client := github.NewGitHubClient(d.Token, d.Logger)
	for _, ws := range github.ListWebhookStatuses(ctx, d.Config, client, d.IaCRepo) {
		inv := WebhookInventory{Repo: ws.Repo}
		switch {
		case ws.Error != nil:
			inv.State = "error: " + ws.Error.Error()
			di.warn(catWebhooks, ws.Repo, ws.Error.Error(), "")
		case !ws.Found:
			inv.State = "missing"
			di.fail(catWebhooks, ws.Repo, "no Herald webhook registered on GitHub", "herald webhooks sync")
		case !ws.Active:
			inv.State = "inactive"
			di.warn(catWebhooks, ws.Repo, "webhook present but inactive", "herald webhooks sync")
		default:
			inv.State = "active"
			di.pass(catWebhooks, ws.Repo)
		}
		di.Webhooks = append(di.Webhooks, inv)
	}
}

// acmeLogHint is the one command that explains any certificate problem here.
const acmeLogHint = "docker logs herald-caddy 2>&1 | grep -iE 'challenge|caa|renew'"

func (di *Diagnosis) checkCaddy(ctx context.Context, d Deps) {
	mgr := &caddy.CaddyManager{Config: d.Config, Logger: d.Logger, HeraldPort: d.HeraldPort}
	running, err := mgr.IsRunning(ctx)
	switch {
	case err != nil:
		di.warn(catCaddy, "running", "could not query Docker: "+err.Error(), "")
	case !running:
		di.fail(catCaddy, "running", "herald-caddy container is not running",
			"herald caddy start (or restart herald.service)")
	default:
		di.pass(catCaddy, "running")
	}

	if !caddy.NetworkExists(ctx) {
		di.fail(catCaddy, "network", "docker network 'caddy' does not exist", "herald caddy start creates it")
	} else {
		di.pass(catCaddy, "network")
	}

	// Both read out of the running container, so they are meaningless when it is down.
	if running {
		di.checkCertificates(ctx, d)
		di.checkRenewals(ctx)
	}
}

// deployedStackCount counts stacks with a deploy directory, i.e. stacks that
// should already have a certificate.
func deployedStackCount(d Deps) int {
	if d.Config == nil {
		return 0
	}
	n := 0
	for name := range d.Config.Stacks {
		if _, err := os.Stat(filepath.Join(d.Config.Server.ServicesDir, name)); err == nil {
			n++
		}
	}
	return n
}

// checkCertificates reports certificates that are expired or too close to expiry
// to still be renewing normally. A running Caddy serving a dead certificate is
// otherwise indistinguishable from a healthy one.
func (di *Diagnosis) checkCertificates(ctx context.Context, d Deps) {
	certs, err := caddy.ListCertificates(ctx)
	if err != nil {
		di.warn(catCaddy, "certificates", "could not read Caddy's certificate store: "+err.Error(), "")
		return
	}
	if len(certs) == 0 {
		// An empty store with stacks already deployed is the signature of a
		// recreated caddy_data volume: new ACME account, and every certificate
		// about to be re-issued at once against the weekly per-domain limit.
		if n := deployedStackCount(d); n > 0 {
			di.warn(catCaddy, "certificates",
				fmt.Sprintf("no certificates stored, but %d stack(s) are deployed — the caddy_data volume looks recreated", n),
				"expect re-issuance; watch for Let's Encrypt rate limits: "+acmeLogHint)
		}
		return
	}

	soonest := certs[0]
	problems := 0
	for _, c := range certs {
		if c.NotAfter.Before(soonest.NotAfter) {
			soonest = c
		}
		switch {
		case c.Expired():
			problems++
			di.fail(catCaddy, "certificate "+c.Name()+": expired",
				fmt.Sprintf("expired %s ago (issuer %s)", humanDays(-c.Remaining()), c.Issuer),
				acmeLogHint)
		case c.Remaining() < caddy.ExpiryThreshold:
			problems++
			di.fail(catCaddy, "certificate "+c.Name()+": renewal overdue",
				fmt.Sprintf("expires in %s; Caddy renews at 30 days, so renewal is failing", humanDays(c.Remaining())),
				acmeLogHint)
		}
	}
	if problems == 0 {
		di.pass(catCaddy, fmt.Sprintf("certificates (%d, next expiry %s)", len(certs), humanDays(soonest.Remaining())))
	}
}

// checkRenewals surfaces the newest ACME failure from Caddy's log. This catches a
// broken renewal weeks before checkCertificates can — the certificate is still
// valid while the renewals behind it are already failing.
func (di *Diagnosis) checkRenewals(ctx context.Context) {
	rerr, err := caddy.RecentRenewalError(ctx, caddy.RenewalErrorWindow)
	if err != nil {
		di.warn(catCaddy, "renewals", "could not read Caddy logs: "+err.Error(), "")
		return
	}
	if rerr == nil {
		di.pass(catCaddy, "renewals")
		return
	}
	label := "renewal failing"
	if rerr.Identifier != "" {
		label = "renewal failing: " + rerr.Identifier
	}
	di.fail(catCaddy, label,
		fmt.Sprintf("%s (last attempt %s)", rerr.Detail, rerr.At.Local().Format(time.RFC3339)),
		acmeLogHint)
}

// humanDays renders a duration at the granularity operators think in.
func humanDays(d time.Duration) string {
	if d < 24*time.Hour {
		return fmt.Sprintf("%d hours", int(d.Hours()))
	}
	if days := int(d.Hours() / 24); days != 1 {
		return fmt.Sprintf("%d days", days)
	}
	return "1 day"
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
			di.warn(catStacks, name+": secrets", "could not check: "+err.Error(), "")
		} else if len(missing) > 0 {
			di.fail(catStacks, name+": missing required secret",
				strings.Join(missing, ", "), "herald secret set "+missing[0])
		}

		deployDir := filepath.Join(d.Config.Server.ServicesDir, name)
		if _, err := os.Stat(deployDir); os.IsNotExist(err) {
			di.fail(catStacks, name+": not deployed",
				"no deploy directory — first deploy is manual", "herald deploy "+name)
			continue
		}
		// config.yml edited since the last deploy. A domain change is the case that
		// matters: the container keeps its old caddy label until it is redeployed,
		// so the new domain never gets a certificate.
		if deployer.ConfigDrifted(deployDir, stack) {
			di.fail(catStacks, name+": config drift",
				"config.yml changed this stack since its last deploy", "herald deploy "+name)
		}

		if maintenance.StackRunning(ctx, name) {
			di.pass(catStacks, name)
		} else {
			di.warn(catStacks, name+": stopped",
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
		di.warn(catStacks, project+": orphan", "running but not present in config",
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
