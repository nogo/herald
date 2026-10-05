package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nogo/herald/internal/secrets"
	"github.com/spf13/cobra"
)

func TestLoadConfigWithStoredTokenRequiresDeployDomain(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yml")
	if err := os.WriteFile(cfgPath, []byte("server:\n  name: pi\n  services_dir: /srv\n  acme_email: ops@example.com\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadConfigWithToken(cfgPath, dir); err != nil {
		t.Fatalf("without a stored token: %v", err)
	}

	store := secrets.NewStore(dir)
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	if err := store.Set("herald/github_token", "ghp_dummy"); err != nil {
		t.Fatal(err)
	}
	_, err := LoadConfigWithToken(cfgPath, dir)
	if err == nil || !strings.Contains(err.Error(), "server.deploy_domain") {
		t.Fatalf("with a stored token: err = %v, want one naming server.deploy_domain", err)
	}
}

// A command without an Args rule accepts any extra words and ignores them, so
// `herald deploy a b` would deploy a. Every runnable subcommand declares one.
func TestEveryCommandDeclaresItsArguments(t *testing.T) {
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if c.HasParent() && c.Runnable() && c.Args == nil && c.Name() != "help" && !strings.HasPrefix(c.CommandPath(), "herald completion") {
			t.Errorf("%s has no Args rule", c.CommandPath())
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(rootCmd)
}
