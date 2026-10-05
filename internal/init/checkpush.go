package bootstrap

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/nogo/herald/internal/config"
	"github.com/nogo/herald/internal/deployer"
)

// CheckPush is the bare repo's pre-receive check. updates is the hook's stdin,
// one "<old> <new> <ref>" line per pushed ref. For the branch the daemon deploys
// (the bare repo's HEAD), it checks out the pushed commit and validates its
// config.yml and every path stack's route the way the daemon will. A non-nil
// error rejects the whole push; git shows its text to the pusher. Pushes to
// other branches and branch deletions pass unchecked.
func CheckPush(ctx context.Context, dataDir string, updates io.Reader) error {
	bare := config.DataDir(dataDir).BareRepo()
	head, err := gitOutput(ctx, bare, "symbolic-ref", "HEAD")
	if err != nil {
		return fmt.Errorf("reading the deployed branch: %w", err)
	}

	lines := bufio.NewScanner(updates)
	for lines.Scan() {
		fields := strings.Fields(lines.Text())
		if len(fields) != 3 || fields[2] != head || strings.Trim(fields[1], "0") == "" {
			continue
		}
		if err := checkCommit(ctx, bare, fields[1]); err != nil {
			return fmt.Errorf("push rejected, nothing deployed:\n%w", err)
		}
	}
	return lines.Err()
}

// checkCommit validates the tree of commit in a throwaway checkout. The private
// index keeps the bare repo untouched; inside pre-receive, git's quarantine
// environment makes the not-yet-accepted objects readable.
func checkCommit(ctx context.Context, bare, commit string) error {
	tree, err := os.MkdirTemp("", "herald-push-")
	if err != nil {
		return fmt.Errorf("creating checkout dir: %w", err)
	}
	defer os.RemoveAll(tree)

	cmd := exec.CommandContext(ctx, "git", "-c", "safe.directory=*", "--git-dir", bare, "--work-tree", tree, "checkout", "-f", commit, "--", ".")
	cmd.Env = append(os.Environ(), "GIT_INDEX_FILE="+filepath.Join(tree, ".herald-index"))
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("checking out %s: %w: %s", commit, err, strings.TrimSpace(string(out)))
	}

	cfg, err := config.Load(filepath.Join(tree, "config.yml"))
	if err == nil {
		err = deployer.CheckPathRoutes(cfg, tree)
	}
	if err != nil {
		// Name files as the pusher knows them, relative to the repo root.
		return errors.New(strings.ReplaceAll(err.Error(), tree+string(filepath.Separator), ""))
	}
	return nil
}

func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", append([]string{"-c", "safe.directory=*", "-C", dir}, args...)...).Output()
	return strings.TrimSpace(string(out)), err
}
