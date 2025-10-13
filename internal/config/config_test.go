package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg, err := DefaultConfig()
	if err != nil {
		t.Fatalf("DefaultConfig failed: %v", err)
	}

	if cfg.ActiveContainerRuntime != "docker" {
		t.Errorf("Expected default runtime 'docker', got '%s'", cfg.ActiveContainerRuntime)
	}

	if cfg.LocalStorePath == "" {
		t.Error("Expected LocalStorePath to be set")
	}

	if len(cfg.ControlPlanes) != 0 {
		t.Error("Expected empty ControlPlanes list")
	}
}

func TestLoadAndSave(t *testing.T) {
	tmpDir := t.TempDir()
	configFile := filepath.Join(tmpDir, "config.yaml")

	// 1. Load non-existent file should return defaults
	cfg, err := Load(configFile)
	if err != nil {
		t.Fatalf("Load failed for non-existent file: %v", err)
	}
	if cfg.ActiveContainerRuntime != "docker" {
		t.Errorf("Expected default runtime 'docker' for new config, got '%s'", cfg.ActiveContainerRuntime)
	}

	// 2. Modify and Save
	cfg.ActiveContainerRuntime = "podman"
	cfg.LocalStorePath = "/tmp/custom/dropzone"
	cp := ControlPlaneConfig{
		Name:     "test-repo",
		Type:     "oci",
		Endpoint: "registry.example.com",
		Auth: AuthOptions{
			Username: "user",
			Password: "password",
		},
	}
	cfg.AddControlPlane(cp)

	if err := cfg.Save(configFile); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// 3. Load existing file
	loadedCfg, err := Load(configFile)
	if err != nil {
		t.Fatalf("Load failed for existing file: %v", err)
	}

	if loadedCfg.ActiveContainerRuntime != "podman" {
		t.Errorf("Expected runtime 'podman', got '%s'", loadedCfg.ActiveContainerRuntime)
	}
	if loadedCfg.LocalStorePath != "/tmp/custom/dropzone" {
		t.Errorf("Expected LocalStorePath '/tmp/custom/dropzone', got '%s'", loadedCfg.LocalStorePath)
	}
	if len(loadedCfg.ControlPlanes) != 1 {
		t.Errorf("Expected 1 control plane, got %d", len(loadedCfg.ControlPlanes))
	}
	if loadedCfg.ControlPlanes[0].Name != "test-repo" {
		t.Errorf("Expected control plane name 'test-repo', got '%s'", loadedCfg.ControlPlanes[0].Name)
	}
}

func TestControlPlaneOperations(t *testing.T) {
	cfg, _ := DefaultConfig()

	cp1 := ControlPlaneConfig{Name: "repo1", Type: "oci", Endpoint: "e1"}
	cp2 := ControlPlaneConfig{Name: "repo2", Type: "github", Endpoint: "e2"}

	// Add
	cfg.AddControlPlane(cp1)
	cfg.AddControlPlane(cp2)

	if len(cfg.ControlPlanes) != 2 {
		t.Errorf("Expected 2 control planes, got %d", len(cfg.ControlPlanes))
	}

	// Get
	got, found := cfg.GetControlPlane("repo1")
	if !found {
		t.Error("Expected to find repo1")
	}
	if got.Endpoint != "e1" {
		t.Errorf("Expected endpoint 'e1', got '%s'", got.Endpoint)
	}

	_, found = cfg.GetControlPlane("nonexistent")
	if found {
		t.Error("Expected not to find nonexistent repo")
	}

	// Update (Add existing name)
	cp1Updated := ControlPlaneConfig{Name: "repo1", Type: "oci", Endpoint: "e1-updated"}
	cfg.AddControlPlane(cp1Updated)

	got, _ = cfg.GetControlPlane("repo1")
	if got.Endpoint != "e1-updated" {
		t.Errorf("Expected updated endpoint 'e1-updated', got '%s'", got.Endpoint)
	}
	if len(cfg.ControlPlanes) != 2 {
		t.Errorf("Expected count to remain 2 after update, got %d", len(cfg.ControlPlanes))
	}

	// Remove
	cfg.RemoveControlPlane("repo1")
	if len(cfg.ControlPlanes) != 1 {
		t.Errorf("Expected 1 control plane after removal, got %d", len(cfg.ControlPlanes))
	}
	_, found = cfg.GetControlPlane("repo1")
	if found {
		t.Error("Expected repo1 to be removed")
	}
}

func TestLoadMalformed(t *testing.T) {
	tmpDir := t.TempDir()
	configFile := filepath.Join(tmpDir, "badconfig.yaml")

	if err := os.WriteFile(configFile, []byte("invalid: yaml: content: :"), 0644); err != nil {
		t.Fatalf("Failed to write malformed config file: %v", err)
	}

	_, err := Load(configFile)
	if err == nil {
		t.Error("Expected Load to fail for malformed config file")
	}
}
