package github

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func TestSetCommitStatus(t *testing.T) {
	var got CommitStatus
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/repos/nogo/srv-moria/statuses/abc123" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") == "" {
			t.Error("request carries no token")
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{}`))
	}))
	want := CommitStatus{State: "failure", Description: `stack "shop": set service:`, Context: "herald/moria"}
	if err := client.SetCommitStatus(context.Background(), "nogo/srv-moria", "abc123", want); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("posted %+v, want %+v", got, want)
	}

	failing := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	if err := failing.SetCommitStatus(context.Background(), "nogo/srv-moria", "abc123", want); err == nil {
		t.Error("404 from GitHub: err = nil")
	}
}
