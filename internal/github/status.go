package github

import (
	"context"
	"fmt"
	"net/http"
)

// CommitStatus is a status GitHub shows next to a commit: State is "success",
// "failure", "error" or "pending"; Context names who reports it, so a later
// status with the same Context replaces the earlier one.
type CommitStatus struct {
	State       string `json:"state"`
	Description string `json:"description"`
	Context     string `json:"context"`
}

// SetCommitStatus attaches status to commit sha in fullRepo ("owner/repo").
// Description must fit GitHub's 140-character limit.
func (c *GitHubClient) SetCommitStatus(ctx context.Context, fullRepo, sha string, status CommitStatus) error {
	owner, repo, err := splitRepo(fullRepo)
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%s/repos/%s/%s/statuses/%s", c.BaseURL, owner, repo, sha)
	_, _, err = c.doRequestJSON(ctx, http.MethodPost, url, status, http.StatusCreated)
	return err
}
