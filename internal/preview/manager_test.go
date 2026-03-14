package preview

import (
	"path/filepath"
	"testing"
	"time"
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
