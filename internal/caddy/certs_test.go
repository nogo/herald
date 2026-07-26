package caddy

import (
	"archive/tar"
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"
)

// certTarball builds the same shape `docker cp` streams out of the container:
// certificates/<ca>/<domain>/<domain>.crt, alongside the .key and .json files
// that must be ignored.
func certTarball(t *testing.T, domain string, notAfter time.Time) []byte {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}
	// Self-signed, so the parsed Issuer CN comes back as the subject CN.
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: domain},
		DNSNames:     []string{domain},
		NotBefore:    time.Now().Add(-24 * time.Hour),
		NotAfter:     notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("creating certificate: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	base := "certificates/acme-v02.api.letsencrypt.org-directory/" + domain + "/" + domain
	for name, body := range map[string][]byte{
		base + ".crt":  certPEM,
		base + ".key":  []byte("not a certificate"),
		base + ".json": []byte(`{"sans":["` + domain + `"]}`),
	} {
		if err := tw.WriteHeader(&tar.Header{
			Name: name, Mode: 0600, Size: int64(len(body)), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatalf("tar header: %v", err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatalf("tar write: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	return buf.Bytes()
}

func TestParseCertificates(t *testing.T) {
	notAfter := time.Now().Add(45 * 24 * time.Hour).Truncate(time.Second)
	certs, err := parseCertificates(certTarball(t, "app.example.com", notAfter))
	if err != nil {
		t.Fatalf("parseCertificates: %v", err)
	}
	if len(certs) != 1 {
		t.Fatalf("want 1 certificate (.key and .json must be skipped), got %d", len(certs))
	}
	c := certs[0]
	if c.Name() != "app.example.com" {
		t.Errorf("Name() = %q, want app.example.com", c.Name())
	}
	if c.Issuer != "app.example.com" {
		t.Errorf("Issuer = %q, want the self-signed issuer CN", c.Issuer)
	}
	if !c.NotAfter.Equal(notAfter.UTC()) {
		t.Errorf("NotAfter = %v, want %v", c.NotAfter, notAfter.UTC())
	}
	if c.Expired() {
		t.Error("Expired() = true for a certificate 45 days out")
	}
}

func TestParseCertificatesExpired(t *testing.T) {
	certs, err := parseCertificates(certTarball(t, "blog.example.net", time.Now().Add(-14*24*time.Hour)))
	if err != nil {
		t.Fatalf("parseCertificates: %v", err)
	}
	if len(certs) != 1 || !certs[0].Expired() {
		t.Fatalf("want one expired certificate, got %+v", certs)
	}
	if certs[0].Remaining() > 0 {
		t.Errorf("Remaining() = %v, want negative", certs[0].Remaining())
	}
}

func TestParseCertificatesEmpty(t *testing.T) {
	certs, err := parseCertificates(nil)
	if err != nil {
		t.Fatalf("parseCertificates(nil): %v", err)
	}
	if len(certs) != 0 {
		t.Errorf("want no certificates, got %d", len(certs))
	}
}

func TestParseRenewalError(t *testing.T) {
	// Verbatim shape of a real CAA failure, with the info lines that surround it
	// and the "will retry" follow-up that must not win as the newest match.
	log := []byte(`{"level":"info","ts":1785047259.8,"logger":"tls.renew","msg":"renewing certificate","identifier":"wiki.example.org"}
{"level":"error","ts":1785047262.71,"msg":"challenge failed","identifier":"wiki.example.org","challenge_type":"http-01","problem":{"type":"urn:ietf:params:acme:error:caa","detail":"While processing CAA for wiki.example.org: CAA record for example.org prevents issuance"}}
{"level":"error","ts":1785047576.07,"logger":"tls.renew","msg":"could not get certificate from issuer","identifier":"wiki.example.org","error":"order took too long"}
{"level":"error","ts":1785047576.08,"logger":"tls.renew","msg":"will retry","error":"order took too long","attempt":77,"retrying_in":21600}
not json at all
`)

	got := parseRenewalError(log)
	if got == nil {
		t.Fatal("parseRenewalError returned nil, want the newest ACME error")
	}
	if got.Identifier != "wiki.example.org" {
		t.Errorf("Identifier = %q", got.Identifier)
	}
	if got.Detail != "order took too long" {
		t.Errorf("Detail = %q, want the issuer error (not the 'will retry' line)", got.Detail)
	}
	if want := time.Unix(1785047576, 0); !got.At.Equal(want) {
		t.Errorf("At = %v, want %v", got.At, want)
	}
}

func TestParseRenewalErrorPrefersProblemDetail(t *testing.T) {
	log := []byte(`{"level":"error","ts":1785047262.71,"msg":"challenge failed","identifier":"shop.example.com","problem":{"detail":"CAA record for example.com prevents issuance"}}`)
	got := parseRenewalError(log)
	if got == nil {
		t.Fatal("parseRenewalError returned nil")
	}
	if got.Detail != "CAA record for example.com prevents issuance" {
		t.Errorf("Detail = %q, want the ACME problem detail", got.Detail)
	}
}

func TestParseRenewalErrorNone(t *testing.T) {
	log := []byte(`{"level":"info","ts":1785052770.4,"logger":"tls.cache.maintenance","msg":"certificate expires soon; queuing for renewal","identifiers":["app.example.com"]}
{"level":"error","ts":1785052771.0,"logger":"http.log.access","msg":"handled request","status":500}
`)
	if got := parseRenewalError(log); got != nil {
		t.Errorf("parseRenewalError = %+v, want nil (no ACME errors present)", got)
	}
}
