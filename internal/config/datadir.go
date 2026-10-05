package config

import "path/filepath"

// DataDir is the layout of Herald's data dir (--data-dir): the server repo
// clone and, on a bare-initialised server, the bare repo the operator pushes
// to. Ask it for these paths instead of joining "repo" or "server.git" onto
// the data dir. The age store's files belong to internal/secrets.
type DataDir string

// Repo is the server repo clone the daemon pulls and reads config.yml from.
func (d DataDir) Repo() string { return filepath.Join(string(d), "repo") }

// ConfigFile is config.yml inside the server repo clone.
func (d DataDir) ConfigFile() string { return filepath.Join(d.Repo(), "config.yml") }

// BareRepo is the bare repo `herald init` creates when the server repo lives
// on this server instead of GitHub.
func (d DataDir) BareRepo() string { return filepath.Join(string(d), "server.git") }

// PostReceiveHook is the hook in BareRepo that signals the daemon on a push.
func (d DataDir) PostReceiveHook() string {
	return filepath.Join(d.BareRepo(), "hooks", "post-receive")
}
