package secrets

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"filippo.io/age"
	"github.com/nogo/herald/internal/config"
)

// Store manages an age-encrypted secrets file.
type Store struct {
	keyPath     string // path to age.key
	secretsPath string // path to secrets.age
}

// NewStore creates a Store rooted at dataDir.
func NewStore(dataDir string) *Store {
	return &Store{
		keyPath:     filepath.Join(dataDir, "age.key"),
		secretsPath: filepath.Join(dataDir, "secrets.age"),
	}
}

// Init generates the age key if it doesn't exist. If the key file has
// wrong permissions it fixes them and logs a warning.
func (s *Store) Init() error {
	info, err := os.Stat(s.keyPath)
	if err == nil {
		if info.Mode().Perm() != 0600 {
			slog.Warn("age key has insecure permissions, fixing", "path", s.keyPath, "mode", info.Mode().Perm())
			if err := os.Chmod(s.keyPath, 0600); err != nil {
				return fmt.Errorf("fixing age key permissions: %w", err)
			}
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("checking age key %s: %w", s.keyPath, err)
	}

	if err := os.MkdirAll(filepath.Dir(s.keyPath), 0700); err != nil {
		return fmt.Errorf("creating data directory: %w", err)
	}

	identity, err := age.GenerateX25519Identity()
	if err != nil {
		return fmt.Errorf("generating age key: %w", err)
	}

	// O_EXCL prevents overwriting if two processes race to init.
	f, err := os.OpenFile(s.keyPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("creating age key file: %w", err)
	}
	defer f.Close()

	if _, err := fmt.Fprintln(f, identity.String()); err != nil {
		return fmt.Errorf("writing age key: %w", err)
	}
	return nil
}

func (s *Store) loadIdentity() (*age.X25519Identity, error) {
	data, err := os.ReadFile(s.keyPath)
	if err != nil {
		return nil, fmt.Errorf("reading age key %s: %w", s.keyPath, err)
	}
	identity, err := age.ParseX25519Identity(strings.TrimSpace(string(data)))
	if err != nil {
		return nil, fmt.Errorf("parsing age key: %w", err)
	}
	return identity, nil
}

// withLock acquires an exclusive flock on a lock file for the duration of fn.
func (s *Store) withLock(fn func() error) error {
	lockPath := s.secretsPath + ".lock"
	lf, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return fmt.Errorf("opening lock file: %w", err)
	}
	defer lf.Close()

	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("acquiring secrets lock: %w", err)
	}
	defer syscall.Flock(int(lf.Fd()), syscall.LOCK_UN) //nolint:errcheck

	return fn()
}

func (s *Store) readSecrets() (map[string]string, error) {
	data, err := os.ReadFile(s.secretsPath)
	if errors.Is(err, os.ErrNotExist) {
		return make(map[string]string), nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading secrets file: %w", err)
	}

	identity, err := s.loadIdentity()
	if err != nil {
		return nil, err
	}

	r, err := age.Decrypt(bytes.NewReader(data), identity)
	if err != nil {
		return nil, fmt.Errorf("cannot decrypt secrets store: %w. Is the age key correct?", err)
	}

	var secrets map[string]string
	if err := json.NewDecoder(r).Decode(&secrets); err != nil {
		return nil, fmt.Errorf("decoding secrets: %w", err)
	}
	return secrets, nil
}

func (s *Store) writeSecrets(secrets map[string]string) error {
	identity, err := s.loadIdentity()
	if err != nil {
		return err
	}

	jsonData, err := json.Marshal(secrets)
	if err != nil {
		return fmt.Errorf("encoding secrets: %w", err)
	}

	tmpPath := s.secretsPath + ".tmp"
	f, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("creating temp secrets file: %w", err)
	}

	w, err := age.Encrypt(f, identity.Recipient())
	if err != nil {
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("initializing encryption: %w", err)
	}

	if _, err := w.Write(jsonData); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("writing encrypted data: %w", err)
	}

	if err := w.Close(); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("finalizing encryption: %w", err)
	}

	if err := f.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("closing temp file: %w", err)
	}

	return os.Rename(tmpPath, s.secretsPath)
}

// Set adds or updates a secret.
func (s *Store) Set(key, value string) error {
	return s.withLock(func() error {
		secrets, err := s.readSecrets()
		if err != nil {
			return err
		}
		secrets[key] = value
		return s.writeSecrets(secrets)
	})
}

// Get retrieves a secret value by key.
func (s *Store) Get(key string) (string, error) {
	secrets, err := s.readSecrets()
	if err != nil {
		return "", err
	}
	val, ok := secrets[key]
	if !ok {
		return "", fmt.Errorf("secret '%s' not found in store", key)
	}
	return val, nil
}

// List returns all secret keys sorted alphabetically.
func (s *Store) List() ([]string, error) {
	secrets, err := s.readSecrets()
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(secrets))
	for k := range secrets {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys, nil
}

// Delete removes a secret. Returns an error if the key doesn't exist.
func (s *Store) Delete(key string) error {
	return s.withLock(func() error {
		secrets, err := s.readSecrets()
		if err != nil {
			return err
		}
		if _, ok := secrets[key]; !ok {
			return fmt.Errorf("secret '%s' not found in store", key)
		}
		delete(secrets, key)
		return s.writeSecrets(secrets)
	})
}

// Import reads filePath and stores its contents as a secret.
func (s *Store) Import(key, filePath string) error {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("reading file %s: %w", filePath, err)
	}
	return s.Set(key, string(data))
}

// Resolve maps a list of SecretRefs to environment variables and Docker secret
// values. Returns an error if any referenced key is missing.
func (s *Store) Resolve(refs []config.SecretRef) (envVars map[string]string, dockerSecrets map[string]string, err error) {
	secrets, err := s.readSecrets()
	if err != nil {
		return nil, nil, err
	}

	envVars = make(map[string]string)
	dockerSecrets = make(map[string]string)

	for _, ref := range refs {
		val, ok := secrets[ref.Key]
		if !ok {
			return nil, nil, fmt.Errorf("secret '%s' not found in store", ref.Key)
		}
		switch ref.Type {
		case "env":
			envVars[ref.Target] = val
		case "docker-secret":
			dockerSecrets[ref.Target] = val
		}
	}

	return envVars, dockerSecrets, nil
}
