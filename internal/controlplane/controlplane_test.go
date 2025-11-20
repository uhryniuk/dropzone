package controlplane

import (
	"path/filepath"
	"testing"

	"github.com/dropzone/internal/config"
	"github.com/dropzone/internal/localstore"
)

// mockControlPlane implements ControlPlane for testing.
type mockControlPlane struct {
	name string
	typ  string
	endp string
}

func (m *mockControlPlane) Name() string     { return m.name }
func (m *mockControlPlane) Type() string     { return m.typ }
func (m *mockControlPlane) Endpoint() string { return m.endp }
func (m *mockControlPlane) ListPackageNames() ([]string, error) {
	return []string{"pkg1", "pkg2"}, nil
}
func (m *mockControlPlane) GetPackageTags(packageName string) ([]string, error) {
	return []string{"1.0.0", "1.1.0"}, nil
}
func (m *mockControlPlane) GetPackageMetadata(packageName, tag string) (*localstore.PackageMetadata, error) {
	return &localstore.PackageMetadata{
		Name:       packageName,
		Version:    tag,
		Checksum:   "mocksum",
		SourceRepo: m.name,
	}, nil
}
func (m *mockControlPlane) DownloadArtifact(packageName, tag, destinationPath string) error {
	return nil
}
func (m *mockControlPlane) Authenticate(username, password, token string) error {
	return nil
}

const mockType = "mock-cp"

func init() {
	RegisterFactory(mockType, func(cfg config.ControlPlaneConfig) (ControlPlane, error) {
		return &mockControlPlane{
			name: cfg.Name,
			typ:  cfg.Type,
			endp: cfg.Endpoint,
		}, nil
	})
}

func setupManager(t *testing.T) (*Manager, *localstore.LocalStore) {
	tmpDir := t.TempDir()
	store := localstore.New(tmpDir)
	if err := store.Init(); err != nil {
		t.Fatalf("Failed to init store: %v", err)
	}

	cfg, _ := config.DefaultConfig()
	cfg.LocalStorePath = tmpDir

	m, err := NewManager(cfg, store)
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	return m, store
}

func TestManager_AddRemove(t *testing.T) {
	m, store := setupManager(t)

	cpName := "test-repo"
	cpEndpoint := "http://example.com"

	// Test Add
	err := m.Add(cpName, mockType, cpEndpoint, config.AuthOptions{})
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	// Verify in memory
	cp, err := m.Get(cpName)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if cp.Endpoint() != cpEndpoint {
		t.Errorf("Endpoint mismatch. Got %s, want %s", cp.Endpoint(), cpEndpoint)
	}

	// Verify persistence
	configFile := filepath.Join(store.ConfigPath(), "config.yaml")
	loadedCfg, err := config.Load(configFile)
	if err != nil {
		t.Fatalf("Failed to load persisted config: %v", err)
	}
	loadedCP, found := loadedCfg.GetControlPlane(cpName)
	if !found {
		t.Error("Control plane not found in persisted config")
	}
	if loadedCP.Type != mockType {
		t.Errorf("Persisted type mismatch. Got %s, want %s", loadedCP.Type, mockType)
	}

	// Test Duplicate Add
	err = m.Add(cpName, mockType, "other", config.AuthOptions{})
	if err == nil {
		t.Error("Add should fail for duplicate name")
	}

	// Test Remove
	if err := m.Remove(cpName); err != nil {
		t.Fatalf("Remove failed: %v", err)
	}

	if _, err := m.Get(cpName); err == nil {
		t.Error("Get should fail after removal")
	}

	// Verify persistence removal
	loadedCfg, _ = config.Load(configFile)
	_, found = loadedCfg.GetControlPlane(cpName)
	if found {
		t.Error("Control plane still exists in persisted config after removal")
	}
}

func TestManager_List(t *testing.T) {
	m, _ := setupManager(t)

	m.Add("repo1", mockType, "e1", config.AuthOptions{})
	m.Add("repo2", mockType, "e2", config.AuthOptions{})

	list := m.List()
	if len(list) != 2 {
		t.Errorf("Expected 2 control planes, got %d", len(list))
	}
}

func TestManager_UpdateAll(t *testing.T) {
	m, store := setupManager(t)
	cpName := "repo1"

	m.Add(cpName, mockType, "e1", config.AuthOptions{})

	if err := m.UpdateAll(); err != nil {
		t.Fatalf("UpdateAll failed: %v", err)
	}

	// Verify index was created
	index, err := store.GetControlPlaneIndex(cpName)
	if err != nil {
		t.Fatalf("Failed to get index: %v", err)
	}

	// mockControlPlane returns ["pkg1", "pkg2"]
	if len(index) != 2 {
		t.Errorf("Expected 2 packages in index, got %d", len(index))
	}

	if _, ok := index["pkg1"]; !ok {
		t.Error("Index missing pkg1")
	}
}

func TestManager_LoadExisting(t *testing.T) {
	// 1. Create a config with existing CPs
	tmpDir := t.TempDir()
	store := localstore.New(tmpDir)
	store.Init()

	cfg, _ := config.DefaultConfig()
	cfg.LocalStorePath = tmpDir
	cfg.AddControlPlane(config.ControlPlaneConfig{
		Name:     "existing",
		Type:     mockType,
		Endpoint: "e1",
	})

	// 2. New Manager should load it
	m, err := NewManager(cfg, store)
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	cp, err := m.Get("existing")
	if err != nil {
		t.Fatalf("Failed to get existing CP: %v", err)
	}
	if cp.Name() != "existing" {
		t.Errorf("Name mismatch")
	}
}

func TestManager_UnsupportedType(t *testing.T) {
	m, _ := setupManager(t)
	err := m.Add("bad", "unsupported-type", "e", config.AuthOptions{})
	if err == nil {
		t.Error("Add should fail for unsupported type")
	}
}
