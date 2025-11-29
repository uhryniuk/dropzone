package app

import (
	"fmt"
	"path/filepath"

	"github.com/uhryniuk/dropzone/internal/builder"
	"github.com/uhryniuk/dropzone/internal/config"
	"github.com/uhryniuk/dropzone/internal/controlplane"
	_ "github.com/uhryniuk/dropzone/internal/controlplane/oci" // Register OCI factory
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
	Builder        *builder.Builder
	CPManager      *controlplane.Manager
	PackageHandler *packagehandler.PackageHandler
	ConfigPath     string
}

// New creates a new App instance.
func New() *App {
	return &App{}
}

// Initialize sets up the application context.
func (a *App) Initialize() error {
	home, err := util.GetHomeDir()
	if err != nil {
		return fmt.Errorf("failed to get home directory: %w", err)
	}

	// We place the config file in ~/.dropzone/config/config.yaml
	// This aligns with the LocalStore creating a 'config' subdirectory.
	a.ConfigPath = filepath.Join(home, ".dropzone", "config", "config.yaml")

	// Load configuration (returns defaults if file doesn't exist)
	cfg, err := config.Load(a.ConfigPath)
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}
	a.Config = cfg

	// Initialize Local Store
	a.LocalStore = localstore.New(cfg.LocalStorePath)
	if err := a.LocalStore.Init(); err != nil {
		return fmt.Errorf("failed to initialize local store: %w", err)
	}

	// Initialize Host Integrator
	a.HostIntegrator = hostintegration.New(cfg.LocalStorePath)
	if err := a.HostIntegrator.SetupDropzoneBinPath(); err != nil {
		util.LogDebug("Failed to setup bin path: %v", err)
		// Continue, as this might be just about PATH advice
	}

	// Initialize Builder
	a.Builder = builder.New(cfg.ActiveContainerRuntime)

	// Initialize Control Plane Manager
	cpManager, err := controlplane.NewManager(a.Config, a.LocalStore)
	if err != nil {
		return fmt.Errorf("failed to initialize control plane manager: %w", err)
	}
	a.CPManager = cpManager

	// Initialize Package Handler
	a.PackageHandler = packagehandler.New(a.LocalStore, a.HostIntegrator, a.Builder, a.CPManager)

	// If config file doesn't exist, save the defaults to it.
	// We do this after LocalStore.Init because it ensures the directory structure exists.
	if !util.FileExists(a.ConfigPath) {
		util.LogInfo("Initializing default configuration at %s", a.ConfigPath)
		if err := cfg.Save(a.ConfigPath); err != nil {
			util.LogDebug("Failed to save default config: %v", err)
		}
	}

	return nil
}

// Shutdown performs any necessary cleanup.
func (a *App) Shutdown() {
	// No cleanup required for MVP
}
