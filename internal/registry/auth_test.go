package registry

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
)

func TestAuthStoreSaveLookupRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	s := NewAuthStore(path)

	if err := s.Save("registry.mycorp.example", "alice", "hunter2"); err != nil {
		t.Fatalf("Save: %v", err)
	}

	u, p, ok := s.lookup("registry.mycorp.example")
	if !ok || u != "alice" || p != "hunter2" {
		t.Fatalf("lookup: got (%q,%q,%v), want (alice, hunter2, true)", u, p, ok)
	}

	// Host-prefix fallback: a resource path with a namespace should still
	// resolve against the registry-level entry.
	u, p, ok = s.lookup("registry.mycorp.example/namespace/repo")
	if !ok || u != "alice" {
		t.Errorf("prefix lookup: got (%q,%q,%v)", u, p, ok)
	}
}

func TestAuthStoreWritesMode0600(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	s := NewAuthStore(path)
	if err := s.Save("r.example", "u", "p"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("auth file mode: got %o, want 0600", info.Mode().Perm())
	}
}

func TestAuthStoreUsesDockerCompatibleBase64(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	s := NewAuthStore(path)
	if err := s.Save("r.example", "alice", "hunter2"); err != nil {
		t.Fatal(err)
	}

	// Read raw file and confirm the entry matches Docker's config.json
	// shape so users can hand-edit or share with tools that expect it.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Auths map[string]struct {
			Auth string `json:"auth"`
		} `json:"auths"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("raw file is not JSON: %v", err)
	}
	got, ok := parsed.Auths["r.example"]
	if !ok {
		t.Fatal("registry entry missing from auth file")
	}
	decoded, err := base64.StdEncoding.DecodeString(got.Auth)
	if err != nil {
		t.Fatalf("auth not base64: %v", err)
	}
	if string(decoded) != "alice:hunter2" {
		t.Errorf("auth payload: got %q", decoded)
	}
}

func TestAuthStoreDelete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	s := NewAuthStore(path)
	_ = s.Save("a.example", "u", "p")
	_ = s.Save("b.example", "u", "p")

	existed, err := s.Delete("a.example")
	if err != nil {
		t.Fatal(err)
	}
	if !existed {
		t.Error("Delete should report existed=true on removal")
	}
	if _, _, ok := s.lookup("a.example"); ok {
		t.Error("a.example still resolves after Delete")
	}
	// b.example should still be present.
	if _, _, ok := s.lookup("b.example"); !ok {
		t.Error("Delete removed the wrong entry")
	}

	// Deleting the last entry should remove the file entirely — prevents
	// empty auth files from lingering after logout.
	if _, err := s.Delete("b.example"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("auth file should be removed when empty, stat err=%v", err)
	}

	// Delete on an absent registry is a no-op, not an error.
	existed, err = s.Delete("never-saved")
	if err != nil {
		t.Errorf("Delete on missing key should succeed, got %v", err)
	}
	if existed {
		t.Error("existed should be false for missing key")
	}
}

func TestAuthStoreAcceptsPlainUsernamePasswordFields(t *testing.T) {
	// Some credential helpers write {"username":..., "password":...} rather
	// than a base64 "auth" blob. Lookup should tolerate either shape.
	path := filepath.Join(t.TempDir(), "auth.json")
	body := `{"auths":{"r.example":{"username":"alice","password":"hunter2"}}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewAuthStore(path)
	u, p, ok := s.lookup("r.example")
	if !ok || u != "alice" || p != "hunter2" {
		t.Errorf("plain-field lookup: got (%q,%q,%v)", u, p, ok)
	}
}

func TestAuthStoreLookupMissOnMissingFile(t *testing.T) {
	s := NewAuthStore(filepath.Join(t.TempDir(), "does-not-exist.json"))
	if _, _, ok := s.lookup("r.example"); ok {
		t.Error("lookup against a missing file should miss cleanly")
	}
}

func TestChainedKeychainResolvesFromDropzoneAuthFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	s := NewAuthStore(path)
	if err := s.Save("registry.mycorp.example", "alice", "hunter2"); err != nil {
		t.Fatal(err)
	}

	kc := NewChainedKeychain(path)
	res := resourceFor(t, "registry.mycorp.example/namespace/repo:tag")
	auth, err := kc.Resolve(res)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := auth.Authorization()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Username != "alice" || cfg.Password != "hunter2" {
		t.Errorf("resolved auth: got (%q,%q)", cfg.Username, cfg.Password)
	}
}

func TestChainedKeychainMissFallsThroughToAnonymous(t *testing.T) {
	// Empty auth file path entirely bypasses the dropzone tier; the result
	// for a fresh registry should be the anonymous authenticator, which
	// means "no Authorization header."
	kc := NewChainedKeychain("")
	res := resourceFor(t, "registry.example/thing:latest")
	auth, err := kc.Resolve(res)
	if err != nil {
		t.Fatal(err)
	}
	if auth != authn.Anonymous {
		// authn.DefaultKeychain returns Anonymous when no Docker config
		// entry matches. Validate we didn't accidentally get a real
		// credential (which would surprise tests running on a developer's
		// machine that happens to have Docker credentials for the host).
		cfg, err := auth.Authorization()
		if err == nil && cfg != nil && cfg.Username == "" && cfg.Password == "" {
			return // effectively anonymous
		}
		t.Errorf("expected anonymous or empty auth, got %+v", auth)
	}
}

// resourceFor parses a reference and returns the authn.Resource matching
// its registry, which is what Keychain.Resolve receives in real use.
func resourceFor(t *testing.T, ref string) authn.Resource {
	t.Helper()
	r, err := name.ParseReference(ref)
	if err != nil {
		t.Fatalf("ParseReference %q: %v", ref, err)
	}
	return r.Context().Registry
}

// Reference the url package so the go vet pass doesn't flag unused imports
// when the test suite is pruned. (Avoids churn when individual tests are
// added or removed.)
var _ = url.PathEscape
