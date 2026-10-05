package deployer

import (
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"

	"github.com/nogo/herald/internal/compose"
	"github.com/nogo/herald/internal/config"
)

// selectStackRoute resolves a stack's route the way a deploy does: an
// upstream's port stands in for the explicit port, and the host part of the
// upstream is returned for the caller to wire. upstreamHost is "" without an
// upstream.
func selectStackRoute(composeFile, service, port, upstream string) (route compose.Route, upstreamHost string, err error) {
	if upstream != "" {
		if upstreamHost, port, err = compose.ParseUpstream(upstream); err != nil {
			return compose.Route{}, "", err
		}
	}
	route, err = compose.SelectRoute(composeFile, service, port)
	return route, upstreamHost, err
}

// CheckPathRoutes resolves the route of every path stack in cfg against the
// compose files in repoDir, a checkout of the server repo, exactly as a deploy
// would. It returns nil when every path stack would route, or one joined error
// naming each stack that would not. Repo stacks are skipped: their compose
// file lives in the app repo and is only known at deploy time.
func CheckPathRoutes(cfg *config.Config, repoDir string) error {
	var errs []error
	for _, name := range slices.Sorted(maps.Keys(cfg.Stacks)) {
		stack := cfg.Stacks[name]
		if stack.Path == "" {
			continue
		}
		dir := filepath.Join(repoDir, stack.Path)
		file, err := compose.FindComposeFile(dir)
		if err == nil {
			_, _, err = selectStackRoute(filepath.Join(dir, file), stack.Service, stack.Port, stack.Upstream)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("stack %q: %w", name, err))
		}
	}
	return errors.Join(errs...)
}
