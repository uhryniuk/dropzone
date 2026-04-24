package config

import (
	"os"
	"path/filepath"
	"testing"
)

// Phase 0 config tests cover the minimal placeholder schema. The richer
// schema (DefaultRegistry, Registries, CosignPolicy) lands in Phase 1 and
// brings its own test suite at that time.

func TestDefaultConfig(t *testing.T) {
	cfg, err := DefaultConfig()
	if err != nil {
		t.Fatalf("DefaultConfig: %v", err)
	}
	if cfg.LocalStorePath == "" {
		t.Error("LocalStorePath should be set by default")
	}
}

func TestLoadAndSaveRoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	configFile := filepath.Join(tmpDir, "config.yaml")

	// Non-existent → defaults.
	cfg, err := Load(configFile)
	if err != nil {
		t.Fatalf("Load of missing file: %v", err)
	}

	cfg.LocalStorePath = "/tmp/custom/dropzone"
	if err := cfg.Save(configFile); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := Load(configFile)
	if err != nil {
		t.Fatalf("Load of existing file: %v", err)
	}
	if loaded.LocalStorePath != "/tmp/custom/dropzone" {
		t.Errorf("LocalStorePath round-trip: got %q", loaded.LocalStorePath)
	}
}

func TestLoadMalformed(t *testing.T) {
	tmpDir := t.TempDir()
	configFile := filepath.Join(tmpDir, "bad.yaml")
	if err := os.WriteFile(configFile, []byte("invalid: yaml: content: :"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := Load(configFile); err == nil {
		t.Error("expected error on malformed YAML")
	}
}
