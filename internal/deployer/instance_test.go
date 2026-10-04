package deployer

import (
	"testing"

	"github.com/nogo/herald/internal/config"
)

func TestStackNameOf(t *testing.T) {
	cases := []struct {
		project string
		name    string
		ok      bool
	}{
		{"herald-blog", "blog", true},
		{"herald-preview-blog-feature-1a2b3c4d", "", false},
		{"herald-", "", false},
		{"nextcloud", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.project, func(t *testing.T) {
			name, ok := StackNameOf(tc.project)
			if name != tc.name || ok != tc.ok {
				t.Errorf("StackNameOf(%q) = %q, %v; want %q, %v", tc.project, name, ok, tc.name, tc.ok)
			}
		})
	}
}

func TestStackNameOfRoundTrips(t *testing.T) {
	cfg := &config.Config{Server: config.Server{ServicesDir: "/opt/deploy"}}
	if name, ok := StackNameOf(StackInstance(cfg, "blog").Project); !ok || name != "blog" {
		t.Errorf("StackNameOf(StackInstance(blog).Project) = %q, %v", name, ok)
	}
	if _, ok := StackNameOf(PreviewInstance(cfg, "blog-x").Project); ok {
		t.Error("a preview project was taken for a production stack")
	}
}
