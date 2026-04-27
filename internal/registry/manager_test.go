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
