package registry

import (
	"errors"
	"testing"

	"github.com/uhryniuk/dropzone/internal/config"
)

// seedManager builds a Manager over a fresh DefaultConfig (which pre-seeds
// the chainguard registry) plus any extra registries the test needs. The
// save callback is a no-op; Add/Remove persistence is covered by the
// round-trip test in the config package.
func seedManager(t *testing.T, extras ...config.RegistryConfig) *Manager {
	t.Helper()
	cfg, err := config.DefaultConfig()
	if err != nil {
		t.Fatalf("DefaultConfig: %v", err)
	}
	for _, r := range extras {
		cfg.UpsertRegistry(r)
	}
	return NewManager(cfg, func() error { return nil }, nil, nil)
}

func TestResolveShortFormUsesDefaultRegistry(t *testing.T) {
	m := seedManager(t)
	ref, err := m.Resolve("jq")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if ref.Registry.Name != "chainguard" {
		t.Errorf("registry: got %q, want chainguard", ref.Registry.Name)
	}
	if ref.Image != "jq" || ref.Tag != "latest" {
		t.Errorf("image/tag: got %q/%q, want jq/latest", ref.Image, ref.Tag)
	}
}

func TestResolveExplicitTag(t *testing.T) {
	m := seedManager(t)
	ref, _ := m.Resolve("jq:1.7.1")
	if ref.Image != "jq" || ref.Tag != "1.7.1" {
		t.Errorf("image/tag: got %q/%q, want jq/1.7.1", ref.Image, ref.Tag)
	}
}

func TestResolveQualifiedByRegistryName(t *testing.T) {
	m := seedManager(t, config.RegistryConfig{Name: "mycorp", URL: "registry.mycorp.example/signed"})
	ref, err := m.Resolve("mycorp/tool:dev")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if ref.Registry.Name != "mycorp" {
		t.Errorf("registry: got %q, want mycorp", ref.Registry.Name)
	}
	if ref.Image != "tool" || ref.Tag != "dev" {
		t.Errorf("image/tag: got %q/%q", ref.Image, ref.Tag)
	}
}

func TestResolveMultiSegmentImagePath(t *testing.T) {
	m := seedManager(t)
	// chainguard registry + nested path
	ref, err := m.Resolve("chainguard/private/nested:v2")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if ref.Image != "private/nested" || ref.Tag != "v2" {
		t.Errorf("image/tag: got %q/%q", ref.Image, ref.Tag)
	}
}

func TestResolveUnknownRegistryErrors(t *testing.T) {
	m := seedManager(t)
	_, err := m.Resolve("unknown/tool")
	if !errors.Is(err, ErrRegistryNotFound) {
		t.Fatalf("want ErrRegistryNotFound, got %v", err)
	}
}

func TestResolveHostnameQualifiedRefMaterializesEphemeralRegistry(t *testing.T) {
	// A user-typed URL like gitea.example.com/owner/repo:tag should
	// resolve directly without first running `dz add registry`. The
	// resulting Registry has no policy attached, so the install path
	// will require --allow-unsigned, which is the right safety stance.
	m := seedManager(t)
	ref, err := m.Resolve("gitea.example.com/owner/repo:v1.2")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if ref.Registry.URL != "gitea.example.com" {
		t.Errorf("registry URL: got %q, want gitea.example.com", ref.Registry.URL)
	}
	if ref.Registry.Name != "gitea.example.com" {
		t.Errorf("registry Name: got %q, want gitea.example.com", ref.Registry.Name)
	}
	if ref.Registry.CosignPolicy != nil {
		t.Error("ephemeral registry should have no cosign policy")
	}
	if ref.Image != "owner/repo" {
		t.Errorf("image: got %q, want owner/repo", ref.Image)
	}
	if ref.Tag != "v1.2" {
		t.Errorf("tag: got %q, want v1.2", ref.Tag)
	}
}

func TestResolveHostnameWithPort(t *testing.T) {
	// localhost:5000/foo is the canonical ephemeral case (a local
	// registry:2 container during dev). The colon in the first segment
	// is the giveaway that this is a hostname, not a configured name.
	m := seedManager(t)
	ref, err := m.Resolve("localhost:5000/foo:latest")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if ref.Registry.URL != "localhost:5000" {
		t.Errorf("registry URL: got %q", ref.Registry.URL)
	}
	if ref.Image != "foo" || ref.Tag != "latest" {
		t.Errorf("image/tag: got %q/%q", ref.Image, ref.Tag)
	}
}

func TestResolveConfiguredNameTakesPrecedenceOverHostnameLookalike(t *testing.T) {
	// A configured registry name with no dot or colon takes the
	// configured-name path, not the ephemeral path. We don't try to be
	// clever about disambiguation: register an entry called "myhost"
	// and that's what wins.
	m := seedManager(t, config.RegistryConfig{Name: "myhost", URL: "registry.example/myhost"})
	ref, err := m.Resolve("myhost/tool:latest")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if ref.Registry.URL != "registry.example/myhost" {
		t.Errorf("Resolve picked the wrong registry: %+v", ref.Registry)
	}
}

func TestResolveEmptyRefErrors(t *testing.T) {
	m := seedManager(t)
	if _, err := m.Resolve(""); !errors.Is(err, ErrEmptyRef) {
		t.Fatalf("want ErrEmptyRef, got %v", err)
	}
}

func TestResolveTrimsWhitespace(t *testing.T) {
	m := seedManager(t)
	ref, err := m.Resolve("  jq  ")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if ref.Image != "jq" {
		t.Errorf("image: got %q, want jq", ref.Image)
	}
}

func TestFullReferenceFormatsCorrectly(t *testing.T) {
	m := seedManager(t)
	ref, _ := m.Resolve("jq:1.7.1")
	if got := ref.FullReference(); got != "docker.io/chainguard/jq:1.7.1" {
		t.Errorf("FullReference: got %q", got)
	}
}

func TestAddRejectsDuplicates(t *testing.T) {
	m := seedManager(t)
	err := m.Add(Registry{Name: "chainguard", URL: "x.example"})
	if err == nil {
		t.Fatal("expected error on duplicate registry name")
	}
}

func TestAddRejectsMissingFields(t *testing.T) {
	m := seedManager(t)
	if err := m.Add(Registry{Name: "", URL: "x"}); err == nil {
		t.Error("expected error for missing name")
	}
	if err := m.Add(Registry{Name: "x", URL: ""}); err == nil {
		t.Error("expected error for missing url")
	}
}

func TestAddPersists(t *testing.T) {
	cfg, _ := config.DefaultConfig()
	saved := 0
	m := NewManager(cfg, func() error { saved++; return nil }, nil, nil)
	if err := m.Add(Registry{Name: "x", URL: "x.example"}); err != nil {
		t.Fatal(err)
	}
	if saved != 1 {
		t.Errorf("save callback invoked %d times, want 1", saved)
	}
	if _, err := m.Get("x"); err != nil {
		t.Errorf("Get after Add: %v", err)
	}
}

func TestRemoveUnknownErrors(t *testing.T) {
	m := seedManager(t)
	if err := m.Remove("nope"); !errors.Is(err, ErrRegistryNotFound) {
		t.Errorf("want ErrRegistryNotFound, got %v", err)
	}
}

func TestDefaultMissingErrors(t *testing.T) {
	cfg, _ := config.DefaultConfig()
	cfg.DefaultRegistry = ""
	m := NewManager(cfg, nil, nil, nil)
	if _, err := m.Default(); err == nil {
		t.Error("expected error when no default registry is configured")
	}
}
