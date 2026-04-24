package app

import (
	"fmt"
	"path/filepath"

	"github.com/uhryniuk/dropzone/internal/config"
	"github.com/uhryniuk/dropzone/internal/hostintegration"
	"github.com/uhryniuk/dropzone/internal/localstore"
	"github.com/uhryniuk/dropzone/internal/packagehandler"
	"github.com/uhryniuk/dropzone/internal/util"
)

// App holds the application context and core services.
type App struct {
	Config         *config.Config
	LocalStore     *localstore.LocalStore
	HostIntegrator *hostintegration.HostIntegrator
	PackageHandler *packagehandler.PackageHandler
	ConfigPath     string
}

// New creates a new App instance.
func New() *App {
	return &App{}
}

// Initialize sets up the application context.
//
// This is the post-design-pivot initialization path. The Registry Manager,
// Sigstore Verifier, and Shim Builder slots on App are intentionally absent
// here and land in Phase 1+ of docs/roadmap.md.
func (a *App) Initialize() error {
	home, err := util.GetHomeDir()
	if err != nil {
		return fmt.Errorf("failed to get home directory: %w", err)
	}

	a.ConfigPath = filepath.Join(home, ".dropzone", "config", "config.yaml")

	cfg, err := config.Load(a.ConfigPath)
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}
	a.Config = cfg

	a.LocalStore = localstore.New(cfg.LocalStorePath)
	if err := a.LocalStore.Init(); err != nil {
		return fmt.Errorf("failed to initialize local store: %w", err)
	}

	a.HostIntegrator = hostintegration.New(cfg.LocalStorePath)
	if err := a.HostIntegrator.SetupDropzoneBinPath(); err != nil {
		util.LogDebug("Failed to setup bin path: %v", err)
	}

	a.PackageHandler = packagehandler.New(a.LocalStore, a.HostIntegrator)

	if !util.FileExists(a.ConfigPath) {
		util.LogInfo("Initializing default configuration at %s", a.ConfigPath)
		if err := cfg.Save(a.ConfigPath); err != nil {
			util.LogDebug("Failed to save default config: %v", err)
		}
	}

	return nil
}

// Shutdown performs any necessary cleanup.
func (a *App) Shutdown() {}
