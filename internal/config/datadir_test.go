package config_test

import (
	"testing"

	"github.com/nogo/herald/internal/config"
)

// The paths are on-disk state of every installed server: changing one strands
// existing clones, bare repos and hooks.
func TestDataDir(t *testing.T) {
	d := config.DataDir("/etc/herald/")
	cases := map[string][2]string{
		"Repo":            {d.Repo(), "/etc/herald/repo"},
		"ConfigFile":      {d.ConfigFile(), "/etc/herald/repo/config.yml"},
		"BareRepo":        {d.BareRepo(), "/etc/herald/server.git"},
		"PostReceiveHook": {d.PostReceiveHook(), "/etc/herald/server.git/hooks/post-receive"},
	}
	for name, c := range cases {
		if c[0] != c[1] {
			t.Errorf("%s = %q, want %q", name, c[0], c[1])
		}
	}
}
