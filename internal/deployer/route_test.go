package deployer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nogo/herald/internal/config"
)

func TestCheckPathRoutes(t *testing.T) {
	repo := t.TempDir()
	write := func(dir, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(repo, dir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, dir, "compose.yml"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("wiki", "services:\n  wiki:\n    expose: [\"80\"]\n")
	write("shop", "services:\n  web:\n    expose: [\"3000\"]\n  db:\n    image: postgres\n")
	write("ha", "services:\n  homeassistant:\n    network_mode: host\n")

	routable := map[string]config.Stack{
		"wiki":   {Path: "wiki"},
		"shop":   {Path: "shop", Service: "web"},
		"ha":     {Path: "ha", Upstream: "host:8123"},
		"budget": {Repo: "nogo/budget"}, // compose lives in the app repo: not checked
	}
	if err := CheckPathRoutes(&config.Config{Stacks: routable}, repo); err != nil {
		t.Fatalf("routable stacks: %v", err)
	}

	broken := map[string]config.Stack{
		"wiki":    {Path: "wiki"},
		"shop":    {Path: "shop"},
		"ha":      {Path: "ha"},
		"missing": {Path: "missing"},
	}
	err := CheckPathRoutes(&config.Config{Stacks: broken}, repo)
	if err == nil {
		t.Fatal("broken stacks: err = nil")
	}
	for _, want := range []string{`stack "shop"`, "set `service:`", `stack "ha"`, "set `port:`", `stack "missing"`, "no compose file"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not contain %q:\n%v", want, err)
		}
	}
	if strings.Contains(err.Error(), `stack "wiki"`) {
		t.Errorf("routable stack reported:\n%v", err)
	}
}
