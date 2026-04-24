package registry

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// authFile is the on-disk credential store at ~/.dropzone/auth.json.
//
// The format matches Docker's ~/.docker/config.json "auths" section
// intentionally: base64(username:password). This isn't encryption — anyone
// with read access to the file can decode the password — but it matches
// the ecosystem convention and avoids introducing a keyring dependency.
// The file is written with mode 0600.
//
// Users who want real key storage should use Docker's credential helpers;
// our chained keychain falls through to the Docker keychain automatically.
type authFile struct {
	Auths map[string]authEntry `json:"auths"`
}

type authEntry struct {
	Auth     string `json:"auth,omitempty"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

// AuthStore reads and writes ~/.dropzone/auth.json.
type AuthStore struct {
	Path string
}

// NewAuthStore returns an AuthStore rooted at path. The file is created on
// first Save; Load tolerates the file being absent.
func NewAuthStore(path string) *AuthStore {
	return &AuthStore{Path: path}
}

// Save writes credentials for the given registry URL. If an entry already
// exists for the same key, it is replaced. Creates parent directories and
// sets file mode 0600 so the secrets aren't world-readable.
func (s *AuthStore) Save(registryURL, username, password string) error {
	if registryURL == "" {
		return errors.New("registry url is required")
	}
	if username == "" || password == "" {
		return errors.New("username and password are required")
	}

	af, err := s.load()
	if err != nil {
		return err
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
	af.Auths[registryURL] = authEntry{Auth: encoded}
	return s.write(af)
}

// Delete removes credentials for registryURL. Returns false if no entry
// existed. Not an error — delete is idempotent.
func (s *AuthStore) Delete(registryURL string) (bool, error) {
	af, err := s.load()
	if err != nil {
		return false, err
	}
	if _, ok := af.Auths[registryURL]; !ok {
		return false, nil
	}
	delete(af.Auths, registryURL)
	if len(af.Auths) == 0 {
		// Leave no empty file behind when the last entry goes.
		if err := os.Remove(s.Path); err != nil && !os.IsNotExist(err) {
			return true, fmt.Errorf("remove empty auth file: %w", err)
		}
		return true, nil
	}
	if err := s.write(af); err != nil {
		return true, err
	}
	return true, nil
}

// lookup returns (username, password, true) if credentials exist for the
// given registry key. The key is matched exactly, then by host prefix
// (so an entry for "registry.example" matches a resource at
// "registry.example/namespace").
func (s *AuthStore) lookup(key string) (string, string, bool) {
	af, err := s.load()
	if err != nil {
		return "", "", false
	}
	if entry, ok := af.Auths[key]; ok {
		return entry.decode()
	}
	// Host-only fallback: strip any trailing path from the resource and
	// try again.
	if host := strings.SplitN(key, "/", 2)[0]; host != key {
		if entry, ok := af.Auths[host]; ok {
			return entry.decode()
		}
	}
	return "", "", false
}

func (s *AuthStore) load() (*authFile, error) {
	af := &authFile{Auths: map[string]authEntry{}}
	data, err := os.ReadFile(s.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return af, nil
		}
		return nil, fmt.Errorf("read auth file: %w", err)
	}
	if len(data) == 0 {
		return af, nil
	}
	if err := json.Unmarshal(data, af); err != nil {
		return nil, fmt.Errorf("parse auth file: %w", err)
	}
	if af.Auths == nil {
		af.Auths = map[string]authEntry{}
	}
	return af, nil
}

func (s *AuthStore) write(af *authFile) error {
	data, err := json.MarshalIndent(af, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal auth file: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o755); err != nil {
		return fmt.Errorf("create auth dir: %w", err)
	}
	// 0600 keeps the password bytes out of casual view.
	if err := os.WriteFile(s.Path, data, 0o600); err != nil {
		return fmt.Errorf("write auth file: %w", err)
	}
	return nil
}

func (e authEntry) decode() (string, string, bool) {
	// Accept either the Docker-style base64 blob or explicit username/password
	// fields (which some registries' credential helpers emit).
	if e.Username != "" && e.Password != "" {
		return e.Username, e.Password, true
	}
	if e.Auth == "" {
		return "", "", false
	}
	raw, err := base64.StdEncoding.DecodeString(e.Auth)
	if err != nil {
		return "", "", false
	}
	parts := strings.SplitN(string(raw), ":", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	return parts[0], parts[1], true
}
