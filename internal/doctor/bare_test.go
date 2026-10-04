package doctor

import (
	"bytes"
	"context"
	"strings"
	"testing"

	bootstrap "github.com/nogo/herald/internal/init"
)

func TestEmptyBareRepoIsHealthy(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	if err := bootstrap.InitBare(context.Background(), &out, bootstrap.BareOptions{DataDir: dir, HeraldBin: "/bin/true"}); err != nil {
		t.Fatal(err)
	}
	di := &Diagnosis{}
	di.checkConfigAndRepo(context.Background(), Deps{DataDir: dir})
	di.checkGitHub(context.Background(), Deps{DataDir: dir})
	for _, c := range di.Checks {
		if c.Severity != SeverityOK {
			t.Fatalf("check = %+v", c)
		}
	}
	out.Reset()
	di.Render(&out)
	for _, want := range []string{"nothing pushed yet", "git remote add", dir + "/server.git", "Healthy"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in %s", want, &out)
		}
	}
}
