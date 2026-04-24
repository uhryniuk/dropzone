package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/uhryniuk/dropzone/internal/util"
	"gopkg.in/yaml.v3"
)

// Default registry seed applied on first run when no config file exists.
const (
	chainguardName          = "chainguard"
	chainguardURL           = "cgr.dev/chainguard"
	chainguardIssuer        = "https://token.actions.githubusercontent.com"
	chainguardIdentityRegex = "https://github.com/chainguard-images/images/.*"
)

// CosignPolicy pins the Sigstore signer for a registry.
//
// Both fields are required for verification to succeed. A registry with a nil
// CosignPolicy has no trust root configured; installs from it will require
// the user to pass --allow-unsigned per docs/features/attestation_and_verification.md.
type CosignPolicy struct {
	Issuer        string `yaml:"issuer"`
	IdentityRegex string `yaml:"identity_regex"`
}

// RegistryConfig is the persisted form of a configured registry.
type RegistryConfig struct {
	Name         string        `yaml:"name"`
	URL          string        `yaml:"url"`
	CosignPolicy *CosignPolicy `yaml:"cosign_policy,omitempty"`
}

// Config is the global dropzone configuration.
type Config struct {
	LocalStorePath  string           `yaml:"local_store_path"`
	DefaultRegistry string           `yaml:"default_registry"`
	Registries      []RegistryConfig `yaml:"registries"`

	mu sync.RWMutex
}

// DefaultConfig returns a configuration with the chainguard registry
// pre-seeded and the local store rooted at ~/.dropzone.
func DefaultConfig() (*Config, error) {
	home, err := util.GetHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to determine home directory: %w", err)
	}
	return &Config{
		LocalStorePath:  filepath.Join(home, ".dropzone"),
		DefaultRegistry: chainguardName,
		Registries: []RegistryConfig{
			{
				Name: chainguardName,
				URL:  chainguardURL,
				CosignPolicy: &CosignPolicy{
					Issuer:        chainguardIssuer,
					IdentityRegex: chainguardIdentityRegex,
				},
			},
		},
	}, nil
}

// Load reads configuration from path; returns defaults when the file is
// absent. Existing files missing a LocalStorePath or DefaultRegistry are
// filled in from the defaults so older configs stay loadable.
func Load(path string) (*Config, error) {
	if !util.FileExists(path) {
		return DefaultConfig()
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	cfg := &Config{}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	if defaults, err := DefaultConfig(); err == nil {
		if cfg.LocalStorePath == "" {
			cfg.LocalStorePath = defaults.LocalStorePath
		}
		if cfg.DefaultRegistry == "" && len(cfg.Registries) > 0 {
			cfg.DefaultRegistry = cfg.Registries[0].Name
		}
	}

	return cfg, nil
}

// Save writes the current configuration to path.
func (c *Config) Save(path string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}
	if err := util.CreateDirIfNotExist(filepath.Dir(path)); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}
	return nil
}

// FindRegistry returns the registry entry with the given name, or false.
func (c *Config) FindRegistry(name string) (*RegistryConfig, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for i := range c.Registries {
		if c.Registries[i].Name == name {
			return &c.Registries[i], true
		}
	}
	return nil, false
}

// UpsertRegistry adds or replaces a registry entry by name.
func (c *Config) UpsertRegistry(r RegistryConfig) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.Registries {
		if c.Registries[i].Name == r.Name {
			c.Registries[i] = r
			return
		}
	}
	c.Registries = append(c.Registries, r)
}

// RemoveRegistry drops a registry entry by name. Returns false if absent.
func (c *Config) RemoveRegistry(name string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.Registries {
		if c.Registries[i].Name == name {
			c.Registries = append(c.Registries[:i], c.Registries[i+1:]...)
			return true
		}
	}
	return false
}
