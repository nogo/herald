package webhook_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/nogo/herald/internal/webhook"
)

func TestSyncConnectionPeer(t *testing.T) {
	for _, tc := range []struct {
		peer     string
		accepted bool
	}{
		{"127.0.0.1:1234", true}, {"[::1]:1234", true},
		{"[::ffff:127.0.0.1]:1234", true},
		{"172.18.0.2:1234", false}, {"192.168.42.20:1234", false},
		{"[2001:db8::1]:1234", false}, {"invalid", false},
	} {
		t.Run(tc.peer, func(t *testing.T) {
			called := make(chan struct{}, 1)
			s := &webhook.Server{OnIaCPush: func() { called <- struct{}{} }}
			req := httptest.NewRequest(http.MethodPost, "/sync", nil)
			req.RemoteAddr = tc.peer
			req.Header.Set("X-Forwarded-For", "127.0.0.1")
			req.Header.Set("X-Real-IP", "127.0.0.1")
			req.Header.Set("Forwarded", "for=127.0.0.1")
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, req)
			if tc.accepted {
				if w.Code != http.StatusAccepted {
					t.Fatalf("status = %d: %s", w.Code, w.Body.String())
				}
				select {
				case <-called:
				case <-time.After(time.Second):
					t.Fatal("maintenance callback not called")
				}
			} else {
				if w.Code != http.StatusForbidden {
					t.Fatalf("status = %d, want 403", w.Code)
				}
				select {
				case <-called:
					t.Fatal("rejected signal started maintenance")
				case <-time.After(50 * time.Millisecond):
				}
			}
		})
	}
}

func TestWebhookWithoutSecretRejected(t *testing.T) {
	s := newServer()
	s.Secret = ""
	mac := hmac.New(sha256.New, nil)
	mac.Write([]byte(`{}`))
	signature := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if w := post(s.Handler(), "/webhook", []byte(`{}`), map[string]string{"X-Hub-Signature-256": signature}); w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}
