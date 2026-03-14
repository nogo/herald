package webhook

// Repository is embedded in push and pull_request payloads.
type Repository struct {
	FullName string `json:"full_name"` // "nogo/budget-app"
	CloneURL string `json:"clone_url"`
	SSHURL   string `json:"ssh_url"`
}

// Pusher is the user who pushed the commits.
type Pusher struct {
	Name string `json:"name"`
}

// PushPayload represents a GitHub push event.
type PushPayload struct {
	Ref        string     `json:"ref"`        // "refs/heads/main"
	Before     string     `json:"before"`
	After      string     `json:"after"` // commit SHA
	Repository Repository `json:"repository"`
	Pusher     Pusher     `json:"pusher"`
}

// PullRequestPayload represents a GitHub pull_request event.
type PullRequestPayload struct {
	Action      string      `json:"action"` // "opened", "closed", "synchronize"
	Number      int         `json:"number"`
	PullRequest PullRequest `json:"pull_request"`
	Repository  Repository  `json:"repository"`
}

// PullRequest holds the PR head/base refs.
type PullRequest struct {
	Head   PRRef `json:"head"`
	Base   PRRef `json:"base"`
	Merged bool  `json:"merged"`
}

// PRRef is a branch ref within a pull request.
type PRRef struct {
	Ref string `json:"ref"` // branch name
	SHA string `json:"sha"`
}
