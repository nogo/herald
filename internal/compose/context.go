package compose

import (
	"fmt"
	"os"
	"path/filepath"
)

// Context holds the resolved paths needed to run docker compose commands
// for a stack or preview.
type Context struct {
	ProjectName  string
	ComposeFile  string
	OverrideFile string // empty if file doesn't exist
	EnvFile      string // empty if file doesn't exist
	WorkDir      string
}

// BaseArgs returns the common docker compose arguments:
// [compose, --project-name, X, --env-file, X, -f, X, -f, X]
// Optional flags (--env-file, second -f) are omitted when their paths are empty.
func (c Context) BaseArgs() []string {
	args := []string{
		"compose",
		"--project-name", c.ProjectName,
	}
	if c.EnvFile != "" {
		args = append(args, "--env-file", c.EnvFile)
	}
	args = append(args, "-f", c.ComposeFile)
	if c.OverrideFile != "" {
		args = append(args, "-f", c.OverrideFile)
	}
	return args
}

// FindComposeFile returns the first compose filename found in dir.
func FindComposeFile(dir string) (string, error) {
	for _, name := range []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"} {
		if fileExists(filepath.Join(dir, name)) {
			return name, nil
		}
	}
	return "", fmt.Errorf("no compose file found in %s", dir)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
