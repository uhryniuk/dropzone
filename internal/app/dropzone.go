package app

import (
	"fmt"
	"path/filepath"

	"github.com/uhryniuk/dropzone/internal/config"
	"github.com/uhryniuk/dropzone/internal/hostintegration"
	"github.com/uhryniuk/dropzone/internal/localstore"
	"github.com/uhryniuk/dropzone/internal/packagehandler"
	"github.com/uhryniuk/dropzone/internal/registry"
	"github.com/uhryniuk/dropzone/internal/util"
)

// App holds the application context and core services.
//
// As phases land, more fields get populated:
//
//	Phase 1: Registry manager + cache + client (this file).
//	Phase 4: Sigstore verifier.
//	Phase 3: Shim builder.
type App struct {
	Config          *config.Config
	ConfigPath      string
	LocalStore      *localstore.LocalStore
	HostIntegrator  *hostintegration.HostIntegrator
	RegistryManager *registry.Manager
	PackageHandler  *packagehandler.PackageHandler
}

// New creates a new App instance.
func New() *App {
	return &App{}
}

// Initialize sets up the application context. Idempotent; safe to call
// across cobra command invocations.
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

	// Persist defaults (including the seeded chainguard registry) on first
	// run so users can see and edit the config file. We do this before
	// constructing the Manager so its save callback writes to a file that
	// already exists.
	if !util.FileExists(a.ConfigPath) {
		util.LogInfo("Initializing default configuration at %s", a.ConfigPath)
		if err := cfg.Save(a.ConfigPath); err != nil {
			util.LogDebug("Failed to save default config: %v", err)
		}
	}

	cacheDir := filepath.Join(cfg.LocalStorePath, "cache")
	a.RegistryManager = registry.NewManager(
		cfg,
		func() error { return cfg.Save(a.ConfigPath) },
		registry.NewClient(),
		registry.NewCache(cacheDir, registry.DefaultCacheTTL),
	)

	a.PackageHandler = packagehandler.New(a.LocalStore, a.HostIntegrator)

	return nil
}

// Shutdown performs any necessary cleanup.
func (a *App) Shutdown() {}
