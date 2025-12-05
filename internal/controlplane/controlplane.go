package controlplane

import (
	"fmt"
	"net/http"
	"path/filepath"
	"sync"

	"github.com/uhryniuk/dropzone/internal/config"
	"github.com/uhryniuk/dropzone/internal/localstore"
	"github.com/uhryniuk/dropzone/internal/util"
)

// ControlPlane defines the interface for remote package repositories.
type ControlPlane interface {
	Name() string
	Type() string
	Endpoint() string

	// Discovery
	ListPackageNames() ([]string, error)
	GetPackageTags(packageName string) ([]string, error)
	GetPackageMetadata(packageName, tag string) (*localstore.PackageMetadata, error)

	// Artifact Retrieval
	DownloadArtifact(packageName, tag, destinationPath string) error

	// Authentication (verifies and updates internal state)
	Authenticate(username, password, token string) error
}

// Factory function type for creating ControlPlane instances
type Factory func(cfg config.ControlPlaneConfig) (ControlPlane, error)

var (
	factories   = make(map[string]Factory)
	factoriesMu sync.RWMutex
)

// RegisterFactory registers a factory for a control plane type.
func RegisterFactory(typeStr string, factory Factory) {
	factoriesMu.Lock()
	defer factoriesMu.Unlock()
	factories[typeStr] = factory
}

// Manager manages the lifecycle and operations of control planes.
type Manager struct {
	cfg       *config.Config
	store     *localstore.LocalStore
	instances map[string]ControlPlane
	mu        sync.RWMutex
}

// NewManager creates a new ControlPlane Manager.
func NewManager(cfg *config.Config, store *localstore.LocalStore) (*Manager, error) {
	m := &Manager{
		cfg:       cfg,
		store:     store,
		instances: make(map[string]ControlPlane),
	}

	// Initialize instances from config
	for _, cpConfig := range cfg.ControlPlanes {
		if err := m.loadInstance(cpConfig); err != nil {
			util.LogDebug("Failed to initialize control plane '%s': %v", cpConfig.Name, err)
		}
	}

	return m, nil
}

// loadInstance creates and registers a control plane instance from config.
func (m *Manager) loadInstance(cfg config.ControlPlaneConfig) error {
	factoriesMu.RLock()
	factory, ok := factories[cfg.Type]
	factoriesMu.RUnlock()

	if !ok {
		return fmt.Errorf("unknown control plane type: %s", cfg.Type)
	}

	instance, err := factory(cfg)
	if err != nil {
		return fmt.Errorf("factory failed: %w", err)
	}

	m.instances[cfg.Name] = instance
	return nil
}

// Add registers a new control plane.
func (m *Manager) Add(name, cpType, endpoint string, auth config.AuthOptions) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.instances[name]; exists {
		return fmt.Errorf("control plane '%s' already exists", name)
	}

	// Create Config
	newConfig := config.ControlPlaneConfig{
		Name:     name,
		Type:     cpType,
		Endpoint: endpoint,
		Auth:     auth,
	}

	// Try to create instance to validate
	factoriesMu.RLock()
	factory, ok := factories[cpType]
	factoriesMu.RUnlock()

	if !ok {
		return fmt.Errorf("unsupported control plane type: %s", cpType)
	}

	instance, err := factory(newConfig)
	if err != nil {
		return fmt.Errorf("failed to create control plane: %w", err)
	}

	// Update Config on disk
	// We operate on the global config object, then save it.
	m.cfg.AddControlPlane(newConfig)

	configFile := filepath.Join(m.store.ConfigPath(), "config.yaml")
	if err := m.cfg.Save(configFile); err != nil {
		// In a real transactional system we'd revert memory change,
		// but Config struct is just a data holder.
		return fmt.Errorf("failed to save config: %w", err)
	}

	m.instances[name] = instance
	util.LogInfo("Control plane '%s' added successfully.", name)
	return nil
}

// AddFromGitHubUser registers a new control plane for a GitHub user's care-package repository.
func (m *Manager) AddFromGitHubUser(username string, auth config.AuthOptions) error {
	repoURL := fmt.Sprintf("https://github.com/%s/care-package", username)

	// Verify existence
	req, err := http.NewRequest("GET", repoURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	if auth.Token != "" {
		req.Header.Set("Authorization", "token "+auth.Token)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to verify repository existence: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("repository '%s/care-package' not found or not accessible (status: %d)", username, resp.StatusCode)
	}

	endpoint := fmt.Sprintf("github://%s/care-package", username)
	return m.Add(username, "github", endpoint, auth)
}

// Get retrieves a control plane instance.
func (m *Manager) Get(name string) (ControlPlane, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	cp, ok := m.instances[name]
	if !ok {
		return nil, fmt.Errorf("control plane '%s' not found", name)
	}
	return cp, nil
}

// Remove deregisters a control plane.
func (m *Manager) Remove(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.instances[name]; !ok {
		return fmt.Errorf("control plane '%s' not found", name)
	}

	delete(m.instances, name)
	m.cfg.RemoveControlPlane(name)

	configFile := filepath.Join(m.store.ConfigPath(), "config.yaml")
	if err := m.cfg.Save(configFile); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	return nil
}

// List returns all registered control planes.
func (m *Manager) List() []ControlPlane {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var list []ControlPlane
	for _, cp := range m.instances {
		list = append(list, cp)
	}
	return list
}

// UpdateAll polls all control planes and updates local indexes.
func (m *Manager) UpdateAll() error {
	m.mu.RLock()
	// Copy instances to avoid holding lock during network ops
	instances := make([]ControlPlane, 0, len(m.instances))
	for _, cp := range m.instances {
		instances = append(instances, cp)
	}
	m.mu.RUnlock()

	for _, cp := range instances {
		util.LogInfo("Updating index for '%s'...", cp.Name())

		pkgNames, err := cp.ListPackageNames()
		if err != nil {
			util.LogError("Failed to list packages for '%s': %v", cp.Name(), err)
			continue
		}

		index := make(map[string][]localstore.PackageMetadata)

		for _, pkgName := range pkgNames {
			tags, err := cp.GetPackageTags(pkgName)
			if err != nil {
				util.LogDebug("Failed to get tags for package '%s' in '%s': %v", pkgName, cp.Name(), err)
				continue
			}

			var metas []localstore.PackageMetadata
			for _, tag := range tags {
				// Fetch full metadata (includes checksums/signatures)
				meta, err := cp.GetPackageMetadata(pkgName, tag)
				if err != nil {
					util.LogDebug("Failed to get metadata for %s:%s: %v", pkgName, tag, err)
					continue
				}
				meta.SourceRepo = cp.Name()
				metas = append(metas, *meta)
			}
			index[pkgName] = metas
		}

		if err := m.store.StoreControlPlaneIndex(cp.Name(), index); err != nil {
			util.LogError("Failed to store index for '%s': %v", cp.Name(), err)
		} else {
			util.LogInfo("Updated index for '%s'. Found %d packages.", cp.Name(), len(pkgNames))
		}
	}
	return nil
}
