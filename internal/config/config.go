package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/dropzone/internal/util"
	"gopkg.in/yaml.v3"
)

// AuthOptions holds authentication credentials.
// In the future, sensitive fields should be encrypted before storage.
type AuthOptions struct {
	Username  string `yaml:"username,omitempty"`
	Password  string `yaml:"password,omitempty"`
	Token     string `yaml:"token,omitempty"`
	AccessKey string `yaml:"access_key,omitempty"`
	SecretKey string `yaml:"secret_key,omitempty"`
}

// ControlPlaneConfig defines the configuration for a remote package repository.
type ControlPlaneConfig struct {
	Name     string      `yaml:"name"`
	Type     string      `yaml:"type"` // e.g., "oci", "github", "s3"
	Endpoint string      `yaml:"endpoint"`
	Auth     AuthOptions `yaml:"auth,omitempty"`
}

// Config represents the global configuration for dropzone.
type Config struct {
	LocalStorePath         string               `yaml:"local_store_path"`
	ActiveContainerRuntime string               `yaml:"active_container_runtime"` // "docker" or "podman"
	ControlPlanes          []ControlPlaneConfig `yaml:"control_planes"`

	// mu protects concurrent access to the config
	mu sync.RWMutex
}

// DefaultConfig returns a configuration with default values.
func DefaultConfig() (*Config, error) {
	home, err := util.GetHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to determine home directory: %w", err)
	}

	return &Config{
		LocalStorePath:         filepath.Join(home, ".dropzone"),
		ActiveContainerRuntime: "docker", // Default to docker
		ControlPlanes:          []ControlPlaneConfig{},
	}, nil
}

// Load reads configuration from the specified path.
// If the file does not exist, it returns a default configuration.
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

	// Ensure defaults if fields are missing (e.g. empty file)
	defaults, err := DefaultConfig()
	if err == nil {
		if cfg.LocalStorePath == "" {
			cfg.LocalStorePath = defaults.LocalStorePath
		}
		if cfg.ActiveContainerRuntime == "" {
			cfg.ActiveContainerRuntime = defaults.ActiveContainerRuntime
		}
	}

	return cfg, nil
}

// Save writes the current configuration to the specified path.
func (c *Config) Save(path string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	// Ensure directory exists
	if err := util.CreateDirIfNotExist(filepath.Dir(path)); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	return nil
}

// AddControlPlane adds or updates a control plane configuration.
func (c *Config) AddControlPlane(cp ControlPlaneConfig) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Check if exists and update
	for i, existing := range c.ControlPlanes {
		if existing.Name == cp.Name {
			c.ControlPlanes[i] = cp
			return
		}
	}
	// Append new
	c.ControlPlanes = append(c.ControlPlanes, cp)
}

// GetControlPlane retrieves a control plane by name.
func (c *Config) GetControlPlane(name string) (ControlPlaneConfig, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	for _, cp := range c.ControlPlanes {
		if cp.Name == name {
			return cp, true
		}
	}
	return ControlPlaneConfig{}, false
}

// RemoveControlPlane removes a control plane by name.
func (c *Config) RemoveControlPlane(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for i, cp := range c.ControlPlanes {
		if cp.Name == name {
			c.ControlPlanes = append(c.ControlPlanes[:i], c.ControlPlanes[i+1:]...)
			return
		}
	}
}
