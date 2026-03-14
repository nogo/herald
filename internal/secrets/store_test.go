package secrets

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/nogo/herald/internal/config"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	return NewStore(dir)
}

func initedStore(t *testing.T) *Store {
	t.Helper()
	s := newTestStore(t)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return s
}

// TestInit_GeneratesKey verifies that Init creates the key file with 0600 perms.
func TestInit_GeneratesKey(t *testing.T) {
	s := newTestStore(t)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	info, err := os.Stat(s.keyPath)
	if err != nil {
		t.Fatalf("key file not created: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("key perms = %o, want 0600", info.Mode().Perm())
	}

	data, err := os.ReadFile(s.keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(strings.TrimSpace(string(data)), "AGE-SECRET-KEY-1") {
		t.Errorf("key file content doesn't look like an age key: %q", string(data))
	}
}

// TestInit_Idempotent verifies that a second Init call doesn't overwrite the key.
func TestInit_Idempotent(t *testing.T) {
	s := newTestStore(t)
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(s.keyPath)

	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(s.keyPath)

	if string(first) != string(second) {
		t.Error("Init overwrote existing key")
	}
}

// TestInit_FixesPermissions verifies that Init corrects insecure key file permissions.
func TestInit_FixesPermissions(t *testing.T) {
	s := newTestStore(t)
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}

	// Make permissions too permissive.
	if err := os.Chmod(s.keyPath, 0644); err != nil {
		t.Fatal(err)
	}

	if err := s.Init(); err != nil {
		t.Fatalf("Init with bad perms: %v", err)
	}

	info, err := os.Stat(s.keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("perms after fix = %o, want 0600", info.Mode().Perm())
	}
}

// TestSetGet verifies a basic set/get round trip.
func TestSetGet(t *testing.T) {
	s := initedStore(t)

	if err := s.Set("myapp/password", "s3cret"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, err := s.Get("myapp/password")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "s3cret" {
		t.Errorf("Get = %q, want %q", got, "s3cret")
	}
}

// TestSet_Overwrite verifies that setting an existing key replaces the value.
func TestSet_Overwrite(t *testing.T) {
	s := initedStore(t)

	s.Set("k", "old")
	s.Set("k", "new")

	got, err := s.Get("k")
	if err != nil {
		t.Fatal(err)
	}
	if got != "new" {
		t.Errorf("Get after overwrite = %q, want %q", got, "new")
	}
}

// TestGet_NotFound verifies the error message for a missing key.
func TestGet_NotFound(t *testing.T) {
	s := initedStore(t)

	_, err := s.Get("no/such/key")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "not found in store") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestList_Empty verifies that List returns an empty slice (not nil) when no secrets exist.
func TestList_Empty(t *testing.T) {
	s := initedStore(t)

	keys, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 0 {
		t.Errorf("List on empty store = %v, want []", keys)
	}
}

// TestList_Sorted verifies that List returns keys in alphabetical order.
func TestList_Sorted(t *testing.T) {
	s := initedStore(t)

	s.Set("z/key", "1")
	s.Set("a/key", "2")
	s.Set("m/key", "3")

	keys, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a/key", "m/key", "z/key"}
	if len(keys) != len(want) {
		t.Fatalf("List = %v, want %v", keys, want)
	}
	for i, k := range keys {
		if k != want[i] {
			t.Errorf("List[%d] = %q, want %q", i, k, want[i])
		}
	}
}

// TestDelete removes a key and verifies it's gone.
func TestDelete(t *testing.T) {
	s := initedStore(t)

	s.Set("myapp/pass", "val")
	if err := s.Delete("myapp/pass"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, err := s.Get("myapp/pass")
	if err == nil {
		t.Error("Get after Delete should return error")
	}
}

// TestDelete_NotFound verifies the error message for deleting a missing key.
func TestDelete_NotFound(t *testing.T) {
	s := initedStore(t)

	err := s.Delete("no/such/key")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "not found in store") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestImport reads a temp file and stores its contents.
func TestImport(t *testing.T) {
	s := initedStore(t)

	content := "-----BEGIN CERTIFICATE-----\nMIIF...\n-----END CERTIFICATE-----\n"
	f, err := os.CreateTemp(t.TempDir(), "cert*.pem")
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(content)
	f.Close()

	if err := s.Import("myapp/cert", f.Name()); err != nil {
		t.Fatalf("Import: %v", err)
	}

	got, err := s.Get("myapp/cert")
	if err != nil {
		t.Fatal(err)
	}
	if got != content {
		t.Errorf("Get after Import = %q, want %q", got, content)
	}
}

// TestImport_FileNotFound returns an error when the file doesn't exist.
func TestImport_FileNotFound(t *testing.T) {
	s := initedStore(t)

	err := s.Import("k", filepath.Join(t.TempDir(), "nonexistent.pem"))
	if err == nil {
		t.Error("expected error for missing import file")
	}
}

// TestSpecialCharacters verifies that values with quotes, newlines, and binary-ish
// content survive the encrypt/decrypt round trip.
func TestSpecialCharacters(t *testing.T) {
	s := initedStore(t)

	cases := map[string]string{
		"quotes":   `it's a "test"`,
		"newlines": "line1\nline2\nline3",
		"unicode":  "日本語テスト",
		"backslash": `C:\Users\test\path`,
		"json":     `{"key":"value","nested":{"a":1}}`,
	}

	for k, v := range cases {
		if err := s.Set("test/"+k, v); err != nil {
			t.Fatalf("Set %q: %v", k, err)
		}
	}

	for k, want := range cases {
		got, err := s.Get("test/" + k)
		if err != nil {
			t.Fatalf("Get %q: %v", k, err)
		}
		if got != want {
			t.Errorf("Get %q = %q, want %q", k, got, want)
		}
	}
}

// TestNoSecretsFileOnRead verifies that reading from a non-existent secrets file
// returns an empty map (not an error).
func TestNoSecretsFileOnRead(t *testing.T) {
	s := initedStore(t)

	keys, err := s.List()
	if err != nil {
		t.Fatalf("List on missing secrets file: %v", err)
	}
	if len(keys) != 0 {
		t.Errorf("expected empty list, got %v", keys)
	}
}

// TestWrongKeyReturnsError verifies the error message when secrets.age can't be decrypted.
func TestWrongKeyReturnsError(t *testing.T) {
	s := initedStore(t)
	s.Set("k", "v")

	// Swap in a different key.
	s2 := NewStore(t.TempDir())
	if err := s2.Init(); err != nil {
		t.Fatal(err)
	}
	// Point s2 at s's secrets file.
	s2.secretsPath = s.secretsPath

	_, err := s2.Get("k")
	if err == nil {
		t.Fatal("expected decryption error, got nil")
	}
	if !strings.Contains(err.Error(), "cannot decrypt secrets store") {
		t.Errorf("unexpected error: %v", err)
	}
	if !strings.Contains(err.Error(), "Is the age key correct?") {
		t.Errorf("error missing hint: %v", err)
	}
}

// TestResolve verifies that Resolve splits refs into envVars and dockerSecrets.
func TestResolve(t *testing.T) {
	s := initedStore(t)
	s.Set("myapp/db_pass", "db-secret")
	s.Set("myapp/api_key", "key-val")

	refs := []config.SecretRef{
		{Key: "myapp/db_pass", Type: "env", Target: "DB_PASSWORD"},
		{Key: "myapp/api_key", Type: "docker-secret", Target: "api_key_secret"},
	}

	envVars, dockerSecrets, err := s.Resolve(refs)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if envVars["DB_PASSWORD"] != "db-secret" {
		t.Errorf("envVars[DB_PASSWORD] = %q, want %q", envVars["DB_PASSWORD"], "db-secret")
	}
	if dockerSecrets["api_key_secret"] != "key-val" {
		t.Errorf("dockerSecrets[api_key_secret] = %q, want %q", dockerSecrets["api_key_secret"], "key-val")
	}
}

// TestResolve_MissingSecret verifies the error message when a ref key is absent.
func TestResolve_MissingSecret(t *testing.T) {
	s := initedStore(t)

	refs := []config.SecretRef{
		{Key: "missing/key", Type: "env", Target: "SOME_VAR"},
	}

	_, _, err := s.Resolve(refs)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "not found in store") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestConcurrentWrites verifies that concurrent Set calls don't corrupt the store.
func TestConcurrentWrites(t *testing.T) {
	s := initedStore(t)

	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			key := "concurrent/key"
			_ = s.Set(key, strings.Repeat("x", n+1))
		}(i)
	}
	wg.Wait()

	// Store must still be readable.
	if _, err := s.List(); err != nil {
		t.Fatalf("List after concurrent writes: %v", err)
	}
}

// TestSecretsFilePermissions verifies that secrets.age is written with 0600.
func TestSecretsFilePermissions(t *testing.T) {
	s := initedStore(t)
	s.Set("k", "v")

	info, err := os.Stat(s.secretsPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("secrets.age perms = %o, want 0600", info.Mode().Perm())
	}
}
