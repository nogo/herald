package preview

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// PreviewInfo describes a single active preview deployment.
type PreviewInfo struct {
	ID             string    `json:"id"`
	AppName        string    `json:"app_name"`
	Branch         string    `json:"branch"`
	Domain         string    `json:"domain"`
	Directory      string    `json:"directory"`
	ComposeProject string    `json:"compose_project"`
	ComposeFile    string    `json:"compose_file"`
	CreatedAt      time.Time `json:"created_at"`
	Commit         string    `json:"commit"`
}

type previewState struct {
	Previews []PreviewInfo `json:"previews"`
}

func loadState(path string) (*previewState, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &previewState{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading preview state: %w", err)
	}
	var s previewState
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parsing preview state: %w", err)
	}
	return &s, nil
}

// saveState writes the state atomically via a temp file + rename.
func saveState(path string, s *previewState) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling preview state: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return fmt.Errorf("writing preview state: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp) //nolint:errcheck
		return fmt.Errorf("renaming preview state: %w", err)
	}
	return nil
}

func statePath(dataDir string) string {
	return filepath.Join(dataDir, "previews.json")
}
