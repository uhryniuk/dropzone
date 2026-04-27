package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultConfigSeedsChainguard(t *testing.T) {
	cfg, err := DefaultConfig()
	if err != nil {
		t.Fatalf("DefaultConfig: %v", err)
	}
	if cfg.DefaultRegistry != "chainguard" {
		t.Errorf("default registry: got %q, want chainguard", cfg.DefaultRegistry)
	}
	if len(cfg.Registries) != 1 || cfg.Registries[0].Name != "chainguard" {
		t.Fatalf("expected one chainguard registry, got %+v", cfg.Registries)
	}
	if cfg.Registries[0].CosignPolicy == nil {
		t.Fatal("chainguard registry must have a cosign policy seeded")
	}
	if cfg.Registries[0].CosignPolicy.IdentityRegex == "" {
		t.Error("chainguard cosign identity_regex is empty")
	}
}

func TestLoadMissingFileReturnsDefaults(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "does-not-exist.yaml")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DefaultRegistry != "chainguard" {
		t.Errorf("want chainguard default when file missing, got %q", cfg.DefaultRegistry)
	}
}

func TestSaveLoadRoundTripPreservesRegistries(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "config.yaml")

	cfg, _ := DefaultConfig()
	cfg.UpsertRegistry(RegistryConfig{
		Name: "mycorp",
		URL:  "registry.mycorp.example/signed",
		CosignPolicy: &CosignPolicy{
			Issuer:        "https://accounts.google.com",
			IdentityRegex: ".*@mycorp.example",
		},
	})

	if err := cfg.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded.Registries) != 2 {
		t.Fatalf("round-trip registry count: got %d, want 2", len(loaded.Registries))
	}
	found, ok := loaded.FindRegistry("mycorp")
	if !ok {
		t.Fatal("mycorp registry not found after round-trip")
	}
	if found.CosignPolicy == nil || found.CosignPolicy.Issuer != "https://accounts.google.com" {
		t.Errorf("cosign policy not preserved: %+v", found.CosignPolicy)
	}
}

func TestUpsertAndRemoveRegistry(t *testing.T) {
	cfg, _ := DefaultConfig()

	cfg.UpsertRegistry(RegistryConfig{Name: "a", URL: "a.example"})
	cfg.UpsertRegistry(RegistryConfig{Name: "a", URL: "a.example/v2"})

	if len(cfg.Registries) != 2 {
		t.Fatalf("upsert should replace in place, got %d entries", len(cfg.Registries))
	}
	a, _ := cfg.FindRegistry("a")
	if a.URL != "a.example/v2" {
		t.Errorf("upsert did not replace: url=%q", a.URL)
	}

	if !cfg.RemoveRegistry("a") {
		t.Error("RemoveRegistry returned false for existing registry")
	}
	if cfg.RemoveRegistry("a") {
		t.Error("RemoveRegistry returned true for already-removed registry")
	}
}

func TestLoadMalformedYAMLFails(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "bad.yaml")
	if err := os.WriteFile(path, []byte("invalid: yaml: content: :"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := Load(path); err == nil {
		t.Error("expected parse error for malformed YAML")
	}
}

func TestAlwaysAllowUnsignedRoundTrip(t *testing.T) {
	// always_allow_unsigned defaults to false (the safe default), and
	// round-trips through save/load when set. The CLI consults this
	// flag to decide whether to skip verification on registries with
	// no cosign policy without requiring --allow-unsigned every time.
	cfg, _ := DefaultConfig()
	if cfg.AlwaysAllowUnsigned {
		t.Error("default should be AlwaysAllowUnsigned=false")
	}

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "config.yaml")
	cfg.AlwaysAllowUnsigned = true
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.AlwaysAllowUnsigned {
		t.Error("AlwaysAllowUnsigned did not survive round-trip")
	}
}

func TestLoadFillsMissingDefaultRegistryFromFirstEntry(t *testing.T) {
	// Simulates an older/hand-edited config without default_registry.
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "config.yaml")
	yaml := `local_store_path: /tmp/x
registries:
  - name: alpha
    url: alpha.example
  - name: beta
    url: beta.example
`
	if err := os.WriteFile(path, []byte(yaml), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DefaultRegistry != "alpha" {
		t.Errorf("expected default to backfill from first entry, got %q", cfg.DefaultRegistry)
	}
}

func TestLoadBackfillsSeededRegistryWhenFileHasNone(t *testing.T) {
	// A config from before the registry schema landed (only
	// local_store_path) — or a hand-emptied config — must still produce
	// a usable Config with the chainguard entry seeded. Without this,
	// `dz install jq` fails with "no default registry configured" and
	// the user has to delete the file or hand-edit it.
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(path, []byte("local_store_path: /tmp/x\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DefaultRegistry != "chainguard" {
		t.Errorf("default registry: got %q, want chainguard (backfilled)", cfg.DefaultRegistry)
	}
	if len(cfg.Registries) != 1 || cfg.Registries[0].Name != "chainguard" {
		t.Errorf("registries: got %+v, want chainguard seed", cfg.Registries)
	}
	if cfg.Registries[0].CosignPolicy == nil {
		t.Error("backfilled chainguard entry should carry its cosign policy")
	}
}
