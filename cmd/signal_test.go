package cmd

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestSignalSync(t *testing.T) {
	for _, code := range []int{http.StatusAccepted, http.StatusForbidden} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/sync" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				w.WriteHeader(code)
				w.Write([]byte("test diagnostic"))
			}))
			defer s.Close()
			t.Setenv("HTTP_PROXY", "http://192.0.2.1:1")
			p := s.Listener.Addr().(*net.TCPAddr).Port
			err := signalSync(context.Background(), p)
			if code == http.StatusAccepted && err != nil {
				t.Fatal(err)
			}
			if code == http.StatusForbidden && (err == nil || !strings.Contains(err.Error(), "test diagnostic")) {
				t.Fatalf("error = %v, want rejection diagnostic", err)
			}
		})
	}
}

func TestSignalSyncDaemonNotRunning(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	err = signalSync(context.Background(), p)
	if err == nil || !strings.Contains(err.Error(), "connection refused") || !strings.Contains(err.Error(), "herald serve") {
		t.Fatalf("error = %v, want actionable connection failure", err)
	}
}

func TestSignal_UsesServerPortFromConfig(t *testing.T) {
	signalled := make(chan struct{}, 1)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		signalled <- struct{}{}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer s.Close()
	p := s.Listener.Addr().(*net.TCPAddr).Port

	dataDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dataDir, "repo"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := fmt.Sprintf("server:\n  name: pi\n  services_dir: /srv\n  acme_email: ops@example.com\n  port: %d\n", p)
	if err := os.WriteFile(filepath.Join(dataDir, "repo", "config.yml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	if out, err := heraldProcess(t, "signal", "--data-dir", dataDir).CombinedOutput(); err != nil {
		t.Fatalf("herald signal: %v: %s", err, out)
	}
	select {
	case <-signalled:
	default:
		t.Fatalf("daemon on server.port %d was not signalled", p)
	}
}
