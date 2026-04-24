package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/uhryniuk/dropzone/internal/util"
	"gopkg.in/yaml.v3"
)

// Config represents the global configuration for dropzone.
//
// This is a deliberately thin placeholder; the Phase 1 rewrite replaces the
// whole schema with {DefaultRegistry, Registries, CosignPolicy} per
// docs/features/cli_foundations.md §3.4.
type Config struct {
	LocalStorePath string `yaml:"local_store_path"`

	mu sync.RWMutex
}

// DefaultConfig returns a configuration with default values.
func DefaultConfig() (*Config, error) {
	home, err := util.GetHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to determine home directory: %w", err)
	}
	return &Config{
		LocalStorePath: filepath.Join(home, ".dropzone"),
	}, nil
}

// Load reads configuration from the specified path. Returns defaults when the
// file does not exist.
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

	defaults, err := DefaultConfig()
	if err == nil && cfg.LocalStorePath == "" {
		cfg.LocalStorePath = defaults.LocalStorePath
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

	if err := util.CreateDirIfNotExist(filepath.Dir(path)); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	return nil
}
