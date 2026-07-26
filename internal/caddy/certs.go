package caddy

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"os/exec"
	"path"
	"strings"
	"time"
)

// ExpiryThreshold is the point at which a certificate proves renewal is broken
// rather than merely pending: Caddy starts renewing with a third of the lifetime
// left (30 days on a 90-day certificate), so anything still unrenewed this close
// to expiry has been failing for weeks.
const ExpiryThreshold = 20 * 24 * time.Hour

// RenewalErrorWindow bounds the log scan for ACME failures. Caddy retries on a
// multi-hour backoff, so a day is wide enough to catch a persistent failure and
// narrow enough that a resolved one clears itself.
const RenewalErrorWindow = 24 * time.Hour

// CertInfo is one certificate from Caddy's store.
type CertInfo struct {
	Domains  []string
	Issuer   string
	NotAfter time.Time
}

// Name is the primary domain, for display.
func (c CertInfo) Name() string {
	if len(c.Domains) == 0 {
		return "unknown"
	}
	return c.Domains[0]
}

// Remaining is the time left before expiry, negative once expired.
func (c CertInfo) Remaining() time.Duration { return time.Until(c.NotAfter) }

// Expired reports whether the certificate is past its NotAfter.
func (c CertInfo) Expired() bool { return c.Remaining() < 0 }

// ListCertificates reads Caddy's certificate store out of the running container.
// `docker cp` streams a tar over the Docker API, which is the only way in: the
// caddy-docker-proxy image is built from scratch so `docker exec` has no shell,
// and the data volume is root-owned on the host.
//
// A server that has not issued anything yet has no certificates directory; that
// is reported as an empty list, not an error.
func ListCertificates(ctx context.Context) ([]CertInfo, error) {
	cmd := exec.CommandContext(ctx, "docker", "cp", caddyContainerName+":/data/caddy/certificates", "-")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if strings.Contains(msg, "not find") || strings.Contains(msg, "No such") {
			return nil, nil
		}
		return nil, fmt.Errorf("docker cp: %w: %s", err, msg)
	}
	return parseCertificates(out)
}

func parseCertificates(tarball []byte) ([]CertInfo, error) {
	var certs []CertInfo
	tr := tar.NewReader(bytes.NewReader(tarball))
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading certificate archive: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg || path.Ext(hdr.Name) != ".crt" {
			continue
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", hdr.Name, err)
		}
		block, _ := pem.Decode(data)
		if block == nil {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			continue
		}
		names := cert.DNSNames
		if len(names) == 0 && cert.Subject.CommonName != "" {
			names = []string{cert.Subject.CommonName}
		}
		certs = append(certs, CertInfo{
			Domains:  names,
			Issuer:   cert.Issuer.CommonName,
			NotAfter: cert.NotAfter,
		})
	}
	return certs, nil
}

// ExportACMEAccount returns Caddy's ACME account directory as a base64 tar, or
// "" when Caddy has not registered one yet. The account key it contains is what
// binds Herald to its ACME identity — it exists only inside the caddy_data
// volume, and Caddy silently registers a new account if it goes missing.
func ExportACMEAccount(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", "cp", caddyContainerName+":/data/caddy/acme", "-")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if strings.Contains(msg, "not find") || strings.Contains(msg, "No such") {
			return "", nil
		}
		return "", fmt.Errorf("docker cp: %w: %s", err, msg)
	}
	return base64.StdEncoding.EncodeToString(out), nil
}

// ImportACMEAccount restores an ExportACMEAccount payload into the running
// container. It only ever writes into an empty /data/caddy/acme — the caller
// checks that — so it cannot clobber a live account.
func ImportACMEAccount(ctx context.Context, encoded string) error {
	tarball, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return fmt.Errorf("decoding ACME account backup: %w", err)
	}
	cmd := exec.CommandContext(ctx, "docker", "cp", "-", caddyContainerName+":/data/caddy")
	cmd.Stdin = bytes.NewReader(tarball)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker cp: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// RenewalError is an ACME failure Caddy logged.
type RenewalError struct {
	At         time.Time
	Identifier string
	Detail     string
}

// RecentRenewalError returns the newest ACME failure Caddy logged within window,
// or nil if there is none. Caddy retries renewals on a multi-hour backoff and only
// ever reports the failure to its own log, so without this a certificate can die
// over 30 days while the container stays healthy and every other check passes.
func RecentRenewalError(ctx context.Context, window time.Duration) (*RenewalError, error) {
	since := fmt.Sprintf("%ds", int(window.Seconds()))
	// Caddy logs to stderr, so stdout alone would come back empty.
	out, err := exec.CommandContext(ctx, "docker", "logs", "--since", since, caddyContainerName).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("docker logs: %w", err)
	}
	return parseRenewalError(out), nil
}

// caddyLogLine is the subset of Caddy's JSON log format the ACME failures use.
type caddyLogLine struct {
	Level      string  `json:"level"`
	TS         float64 `json:"ts"`
	Logger     string  `json:"logger"`
	Msg        string  `json:"msg"`
	Error      string  `json:"error"`
	Identifier string  `json:"identifier"`
	Problem    struct {
		Detail string `json:"detail"`
	} `json:"problem"`
}

func parseRenewalError(logOutput []byte) *RenewalError {
	var newest *RenewalError
	for _, line := range bytes.Split(logOutput, []byte("\n")) {
		if !bytes.Contains(line, []byte(`"level":"error"`)) {
			continue
		}
		var l caddyLogLine
		if json.Unmarshal(line, &l) != nil {
			continue
		}
		if !isACMEError(l) {
			continue
		}
		detail := l.Problem.Detail
		if detail == "" {
			detail = l.Error
		}
		if detail == "" {
			detail = l.Msg
		}
		at := time.Unix(int64(l.TS), 0)
		if newest == nil || at.After(newest.At) {
			newest = &RenewalError{At: at, Identifier: l.Identifier, Detail: detail}
		}
	}
	return newest
}

// isACMEError matches the issuance failures worth surfacing. "will retry" repeats
// the detail of the error immediately before it with backoff noise attached, so
// skipping it keeps the reported message the actual cause.
func isACMEError(l caddyLogLine) bool {
	if l.Msg == "will retry" {
		return false
	}
	return strings.HasPrefix(l.Logger, "tls") ||
		l.Msg == "challenge failed" ||
		l.Msg == "validating authorization"
}
