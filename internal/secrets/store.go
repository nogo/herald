package secrets

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
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

// SetIfAbsent writes key=value only if the key is not already present.
// Returns true if the value was written, false if the key already existed.
func (s *Store) SetIfAbsent(key, value string) (bool, error) {
	var written bool
	err := s.withLock(func() error {
		secrets, err := s.readSecrets()
		if err != nil {
			return err
		}
		if _, ok := secrets[key]; ok {
			return nil
		}
		secrets[key] = value
		written = true
		return s.writeSecrets(secrets)
	})
	return written, err
}

// HealthCheck verifies the age key exists with secure (0600) permissions and that
// the secrets store decrypts. It is read-only — unlike Init it does not fix
// permissions — so it is safe for diagnosis. Returns nil when healthy.
func (s *Store) HealthCheck() error {
	info, err := os.Stat(s.keyPath)
	if err != nil {
		return fmt.Errorf("age key %s: %w", s.keyPath, err)
	}
	if info.Mode().Perm() != 0600 {
		return fmt.Errorf("age key %s has insecure permissions %#o (want 0600)", s.keyPath, info.Mode().Perm())
	}
	if _, err := s.readSecrets(); err != nil {
		return err
	}
	return nil
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

// MissingRequired returns the keys of refs that are absent from the store and
// have no Generate value (i.e. they cannot be auto-created). Returns nil, nil
// if nothing is missing.
func (s *Store) MissingRequired(refs []config.SecretRef) ([]string, error) {
	secrets, err := s.readSecrets()
	if err != nil {
		return nil, err
	}
	var missing []string
	for _, ref := range refs {
		if ref.Generate != "" {
			continue
		}
		if _, ok := secrets[ref.Key]; !ok {
			missing = append(missing, ref.Key)
		}
	}
	slices.Sort(missing)
	return missing, nil
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

// GenerateSecret produces a cryptographically random value using the given
// encoding ("base64", "hex", or "alphanumeric"). n is the number of source
// random bytes for base64/hex, or the output length for alphanumeric. n<=0
// defaults to 32.
func GenerateSecret(encoding string, n int) (string, error) {
	return generateSecret(encoding, n)
}

// generateSecret produces a cryptographically random value using the given
// encoding. n is the number of source random bytes (minimum 1).
func generateSecret(encoding string, n int) (string, error) {
	const minGeneratedLen = 16 // defense-in-depth floor; config validation also enforces >=16
	if n <= 0 {
		n = 32
	} else if n < minGeneratedLen {
		n = minGeneratedLen
	}
	switch encoding {
	case "base64":
		buf := make([]byte, n)
		if _, err := rand.Read(buf); err != nil {
			return "", fmt.Errorf("generating random bytes: %w", err)
		}
		return base64.StdEncoding.EncodeToString(buf), nil
	case "hex":
		buf := make([]byte, n)
		if _, err := rand.Read(buf); err != nil {
			return "", fmt.Errorf("generating random bytes: %w", err)
		}
		return hex.EncodeToString(buf), nil
	case "alphanumeric":
		const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
		result := make([]byte, n)
		buf := make([]byte, n*2) // extra bytes for rejection sampling
		written := 0
		for written < n {
			if _, err := rand.Read(buf); err != nil {
				return "", fmt.Errorf("generating random bytes: %w", err)
			}
			for _, b := range buf {
				if written >= n {
					break
				}
				// Reject bytes >= 248 to avoid modulo bias (248 = floor(256/62)*62).
				if b >= 248 {
					continue
				}
				result[written] = alphabet[int(b)%62]
				written++
			}
		}
		return string(result), nil
	default:
		return "", fmt.Errorf("unknown generate encoding %q", encoding)
	}
}

// Resolve maps a list of SecretRefs to environment variables and Docker secret
// values. Returns an error if any referenced key is missing. If a key is
// absent and the ref has Generate set, a value is generated and stored.
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
			if ref.Generate == "" {
				return nil, nil, fmt.Errorf("secret '%s' not found in store", ref.Key)
			}
			generated, err := generateSecret(ref.Generate, ref.Length)
			if err != nil {
				return nil, nil, fmt.Errorf("generating secret '%s': %w", ref.Key, err)
			}
			written, err := s.SetIfAbsent(ref.Key, generated)
			if err != nil {
				return nil, nil, fmt.Errorf("storing generated secret '%s': %w", ref.Key, err)
			}
			if written {
				slog.Info("generated secret", "key", ref.Key)
				val = generated
			} else {
				// Another process stored the key between our read and SetIfAbsent.
				// Re-read to get the stored value.
				reread, rerr := s.readSecrets()
				if rerr != nil {
					return nil, nil, rerr
				}
				val = reread[ref.Key]
			}
		}
		switch ref.Type {
		case "env":
			// A .env file is line-oriented with no escaping; a newline in a value
			// would inject additional KEY=VALUE entries into the container env.
			if strings.ContainsAny(val, "\r\n") {
				return nil, nil, fmt.Errorf("secret %q: value for env target %q contains a newline, which cannot be represented in a .env file", ref.Key, ref.Target)
			}
			envVars[ref.Target] = val
		case "docker-secret":
			dockerSecrets[ref.Target] = val
		}
	}

	return envVars, dockerSecrets, nil
}
