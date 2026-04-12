package preview

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/nogo/herald/internal/compose"
	"github.com/nogo/herald/internal/deployer"
)

func TestSubdomainFromBranch(t *testing.T) {
	tests := []struct {
		branch string
		domain string
		want   string
	}{
		{
			"feature/checkout-redesign",
			"*.preview.basalt.solutions",
			"feature-checkout-redesign.preview.basalt.solutions",
		},
		{
			"fix/BUG-123",
			"*.preview.basalt.solutions",
			"fix-bug-123.preview.basalt.solutions",
		},
		{
			"refs/heads/main",
			"*.preview.basalt.solutions",
			"main.preview.basalt.solutions",
		},
		{
			"main",
			"*.preview.basalt.solutions",
			"main.preview.basalt.solutions",
		},
		{
			"feature/my_special.branch",
			"*.preview.basalt.solutions",
			"feature-my-special-branch.preview.basalt.solutions",
		},
	}

	for _, tt := range tests {
		got := SubdomainFromBranch(tt.branch, tt.domain)
		if got != tt.want {
			t.Errorf("SubdomainFromBranch(%q, %q) = %q, want %q",
				tt.branch, tt.domain, got, tt.want)
		}
	}
}

func TestBranchSlugTruncation(t *testing.T) {
	long := "very-long-branch-name-that-exceeds-sixty-three-characters-limit-here"
	slug := branchSlug(long)
	if len(slug) > 63 {
		t.Errorf("slug len = %d, want <= 63", len(slug))
	}
}

func TestBranchSlugLowercase(t *testing.T) {
	slug := branchSlug("Fix/BUG-123")
	if slug != "fix-bug-123" {
		t.Errorf("got %q, want %q", slug, "fix-bug-123")
	}
}

func TestBranchSlugNoLeadingTrailingHyphens(t *testing.T) {
	slug := branchSlug("///feature///")
	if len(slug) > 0 && (slug[0] == '-' || slug[len(slug)-1] == '-') {
		t.Errorf("slug has leading/trailing hyphen: %q", slug)
	}
}

func TestMakeID(t *testing.T) {
	id := makeID("basalt-app", "feature/checkout")
	if id != "basalt-app-feature-checkout" {
		t.Errorf("got %q, want %q", id, "basalt-app-feature-checkout")
	}
}

func TestStateRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "previews.json")

	// Load empty state (file does not exist).
	state, err := loadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Previews) != 0 {
		t.Errorf("expected empty state, got %d previews", len(state.Previews))
	}

	// Save a preview entry and reload.
	state.Previews = append(state.Previews, PreviewInfo{
		ID:        "test-id",
		AppName:   "test-app",
		Branch:    "feature/test",
		Domain:    "feature-test.preview.example.com",
		CreatedAt: time.Now().UTC().Truncate(time.Second),
	})
	if err := saveState(path, state); err != nil {
		t.Fatal(err)
	}

	state2, err := loadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(state2.Previews) != 1 {
		t.Fatalf("expected 1 preview, got %d", len(state2.Previews))
	}
	if state2.Previews[0].ID != "test-id" {
		t.Errorf("expected ID 'test-id', got %q", state2.Previews[0].ID)
	}
	if state2.Previews[0].Domain != "feature-test.preview.example.com" {
		t.Errorf("unexpected domain: %q", state2.Previews[0].Domain)
	}
}

func TestStateAtomicWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "previews.json")

	state := &previewState{
		Previews: []PreviewInfo{{ID: "a"}, {ID: "b"}},
	}
	if err := saveState(path, state); err != nil {
		t.Fatal(err)
	}

	// Overwrite with a different state.
	state2 := &previewState{
		Previews: []PreviewInfo{{ID: "c"}},
	}
	if err := saveState(path, state2); err != nil {
		t.Fatal(err)
	}

	loaded, err := loadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Previews) != 1 || loaded.Previews[0].ID != "c" {
		t.Errorf("unexpected state after overwrite: %+v", loaded.Previews)
	}
}

func makeTestComposeFile(t *testing.T, dir string) string {
	t.Helper()
	composeContent := "services:\n  app:\n    image: myapp:latest\n"
	path := filepath.Join(dir, "compose.yml")
	if err := os.WriteFile(path, []byte(composeContent), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func makeTestOverrideData(t *testing.T, dir, composeFile, inlineOverride string) []byte {
	t.Helper()
	data, err := deployer.GenerateOverride(deployer.OverrideParams{
		DeployDir:      dir,
		StackName:      "myapp",
		Domain:         "feature-test.preview.example.com",
		ComposeFile:    composeFile,
		EnvFilePaths:   []string{filepath.Join(dir, ".env")},
		DefaultPort:    "3000",
		InternalNet:    "herald-preview-myapp-feature-test-internal",
		InlineOverride: inlineOverride,
	})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// applyPreviewLabel replicates the label post-processing in Deploy().
func applyPreviewLabel(overrideData []byte, id string) []byte {
	var parsedSvcs struct {
		Services map[string]any `yaml:"services"`
	}
	if parseErr := yaml.Unmarshal(overrideData, &parsedSvcs); parseErr == nil {
		for svcName := range parsedSvcs.Services {
			fragment := fmt.Sprintf("services:\n  %s:\n    labels:\n      com.herald.preview: %s\n", svcName, id)
			if merged, mergeErr := compose.DeepMergeYAML(overrideData, []byte(fragment)); mergeErr == nil {
				return merged
			}
			break
		}
	}
	return overrideData
}

func TestPreviewOverrideContainsLabel(t *testing.T) {
	dir := t.TempDir()
	composeFile := makeTestComposeFile(t, dir)
	id := "myapp-feature-test"

	overrideData := makeTestOverrideData(t, dir, composeFile, "")
	overrideData = applyPreviewLabel(overrideData, id)

	if !strings.Contains(string(overrideData), "com.herald.preview: "+id) {
		t.Errorf("override missing com.herald.preview label:\n%s", overrideData)
	}
}

func TestPreviewOverridePreservesYAMLTags(t *testing.T) {
	// Regression: the old DeepMerge (map[string]any) discarded YAML tags like !override.
	// deployer.GenerateOverride uses DeepMergeYAML which preserves them.
	dir := t.TempDir()
	composeFile := makeTestComposeFile(t, dir)

	inlineOverride := "services:\n  app:\n    environment: !override\n      - FOO=bar\n"
	overrideData := makeTestOverrideData(t, dir, composeFile, inlineOverride)

	if !strings.Contains(string(overrideData), "!override") {
		t.Errorf("YAML !override tag was lost in merge:\n%s", overrideData)
	}
}
