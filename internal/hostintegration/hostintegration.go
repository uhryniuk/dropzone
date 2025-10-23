package hostintegration

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/dropzone/internal/util"
)

// HostIntegrator manages the integration with the host system.
type HostIntegrator struct {
	basePath string
	binPath  string
}

// New creates a new HostIntegrator.
func New(basePath string) *HostIntegrator {
	return &HostIntegrator{
		basePath: basePath,
		binPath:  filepath.Join(basePath, "bin"),
	}
}

// SetupDropzoneBinPath ensures the bin directory exists and advises on PATH setup.
func (h *HostIntegrator) SetupDropzoneBinPath() error {
	if err := util.CreateDirIfNotExist(h.binPath); err != nil {
		return fmt.Errorf("failed to create bin directory: %w", err)
	}

	// Check if binPath is in PATH
	pathEnv := os.Getenv("PATH")
	if strings.Contains(pathEnv, h.binPath) {
		return nil
	}

	// Detect shell for advice
	shell := os.Getenv("SHELL")
	configFile := "your shell configuration file"
	if strings.Contains(shell, "zsh") {
		configFile = "~/.zshrc"
	} else if strings.Contains(shell, "bash") {
		configFile = "~/.bashrc"
	}

	util.LogInfo("NOTE: %s is not in your PATH.", h.binPath)
	util.LogInfo("To configure it, add the following to %s:", configFile)
	util.LogInfo("  export PATH=\"%s:$PATH\"", h.binPath)

	return nil
}

// LinkPackageBinaries creates symlinks for a package's binaries.
// It detects conflicts with existing system binaries and dropzone-managed binaries.
// Returns a list of linked binary names.
func (h *HostIntegrator) LinkPackageBinaries(packageName, packageVersion, packageInstallPath string) ([]string, error) {
	pkgBinDir := filepath.Join(packageInstallPath, "bin")
	if !util.FileExists(pkgBinDir) {
		// No bin directory, nothing to link
		return nil, nil
	}

	entries, err := os.ReadDir(pkgBinDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read package bin directory: %w", err)
	}

	var linkedBinaries []string

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		binaryName := entry.Name()
		sourcePath := filepath.Join(pkgBinDir, binaryName)
		targetPath := filepath.Join(h.binPath, binaryName)

		// Conflict Detection

		// 1. Check if it exists in dropzone bin (managed by us)
		if util.FileExists(targetPath) {
			// Check if it's a symlink
			info, err := os.Lstat(targetPath)
			if err == nil && (info.Mode()&os.ModeSymlink != 0) {
				util.LogInfo("Updating existing dropzone link for binary '%s'", binaryName)
				// We remove it to overwrite/relink
				if err := os.Remove(targetPath); err != nil {
					util.LogError("Failed to remove existing link %s: %v", targetPath, err)
					continue
				}
			} else {
				util.LogInfo("Binary '%s' exists in %s and is not a symlink. Skipping.", binaryName, h.binPath)
				continue
			}
		} else {
			// 2. Check if it exists elsewhere in PATH (System binary)
			// exec.LookPath finds the executable in PATH.
			// If we haven't added h.binPath to PATH yet in this session, LookPath won't find the one we are about to create.
			existingPath, err := exec.LookPath(binaryName)
			if err == nil && existingPath != "" {
				// We found a binary in PATH.
				// Since we checked targetPath above (and it didn't exist), this must be a system/other binary.
				// We should not shadow it silently.
				util.LogInfo("Warning: Binary '%s' already exists at '%s'. Skipping link creation to avoid shadowing system binary.", binaryName, existingPath)
				util.LogInfo("You can access the package binary directly at: %s", sourcePath)
				// TODO: Implement prefixing or interactive choice in future
				continue
			}
		}

		// Create Symlink
		if err := os.Symlink(sourcePath, targetPath); err != nil {
			util.LogError("Failed to link binary '%s': %v", binaryName, err)
			continue
		}
		linkedBinaries = append(linkedBinaries, binaryName)
	}

	return linkedBinaries, nil
}

// UnlinkPackageBinaries removes symlinks for a package.
// It checks if the symlink target belongs to the specific package version being removed.
func (h *HostIntegrator) UnlinkPackageBinaries(packageName, packageVersion string) error {
	entries, err := os.ReadDir(h.binPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to read bin directory: %w", err)
	}

	// Target path suffix to identify links belonging to this package version
	// structure: .../packages/<packageName>/<version>/bin/<binaryName>
	// We use filepath.Join to ensure correct OS separators
	targetPathSubstring := filepath.Join("packages", packageName, packageVersion, "bin")

	for _, entry := range entries {
		linkPath := filepath.Join(h.binPath, entry.Name())
		info, err := os.Lstat(linkPath)
		if err != nil {
			continue
		}

		if info.Mode()&os.ModeSymlink != 0 {
			dest, err := os.Readlink(linkPath)
			if err != nil {
				continue
			}

			if strings.Contains(dest, targetPathSubstring) {
				if err := os.Remove(linkPath); err != nil {
					util.LogError("Failed to remove symlink %s: %v", linkPath, err)
				} else {
					util.LogDebug("Removed symlink %s", linkPath)
				}
			}
		}
	}
	return nil
}

// VerifyRuntime checks if the container runtime is available.
func (h *HostIntegrator) VerifyRuntime(runtime string) error {
	cmd := exec.Command(runtime, "--version")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("container runtime '%s' not found or not working: %w", runtime, err)
	}
	return nil
}
