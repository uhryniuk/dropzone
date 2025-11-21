package packagehandler

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/dropzone/internal/attestation"
	"github.com/dropzone/internal/builder"
	"github.com/dropzone/internal/controlplane"
	"github.com/dropzone/internal/hostintegration"
	"github.com/dropzone/internal/localstore"
	"github.com/dropzone/internal/util"
)

// PackageHandler orchestrates package operations.
type PackageHandler struct {
	store      *localstore.LocalStore
	integrator *hostintegration.HostIntegrator
	builder    *builder.Builder
	cpManager  *controlplane.Manager
}

// New creates a new PackageHandler.
func New(store *localstore.LocalStore, integrator *hostintegration.HostIntegrator, builder *builder.Builder, cpManager *controlplane.Manager) *PackageHandler {
	return &PackageHandler{
		store:      store,
		integrator: integrator,
		builder:    builder,
		cpManager:  cpManager,
	}
}

// BuildPackage builds a care package locally.
func (h *PackageHandler) BuildPackage(packageName, dockerfilePath, buildContextPath string, buildArgs, envVars map[string]string) error {
	// 1. Generate a version identifier
	packageVersion := fmt.Sprintf("local-%d", time.Now().Unix())

	util.LogInfo("Building package %s:%s...", packageName, packageVersion)

	// 2. Build and Extract
	tmpPath, err := h.builder.BuildAndExtract(dockerfilePath, buildContextPath, packageName, packageVersion, buildArgs, envVars)
	if err != nil {
		return fmt.Errorf("build failed: %w", err)
	}
	defer os.RemoveAll(tmpPath) // Cleanup extracted temp dir

	// 3. Generate Checksum
	util.LogInfo("Generating checksum...")
	checksum, err := attestation.GenerateChecksum(tmpPath)
	if err != nil {
		return fmt.Errorf("failed to generate checksum: %w", err)
	}

	// 4. Prompt for Signing
	signature, pubKeyRef, err := attestation.PromptForSigning(checksum)
	if err != nil {
		util.LogInfo("Signing failed or skipped: %v", err)
	}

	// 5. Store Package
	util.LogInfo("Storing package...")
	finalPath, err := h.store.StorePackage(packageName, packageVersion, tmpPath)
	if err != nil {
		return fmt.Errorf("failed to store package: %w", err)
	}

	// 6. Store Metadata
	meta := localstore.PackageMetadata{
		Name:        packageName,
		Version:     packageVersion,
		Checksum:    checksum,
		Signature:   signature,
		PublicKey:   pubKeyRef,
		InstallDate: time.Now(),
		SourceRepo:  "local",
		BuildInfo: map[string]string{
			"dockerfile": dockerfilePath,
		},
	}
	if err := h.store.StorePackageMetadata(meta); err != nil {
		return fmt.Errorf("failed to store metadata: %w", err)
	}

	util.LogInfo("Package built and stored successfully at %s", finalPath)
	util.LogInfo("To install, run: dropzone install %s:%s", packageName, packageVersion)
	return nil
}

// InstallPackage installs a care package from a control plane.
func (h *PackageHandler) InstallPackage(packageRef string) error {
	packageName, requestedTag := parsePackageRef(packageRef)

	// 1. Check if already installed (optional for MVP, user might want to reinstall/upgrade)

	util.LogInfo("Resolving package '%s'...", packageRef)

	// 2. Find package in available indexes
	available, err := h.store.GetAllAvailablePackagesFromIndexes()
	if err != nil {
		return fmt.Errorf("failed to list available packages: %w", err)
	}

	var candidate *localstore.PackageMetadata
	var versions []string

	for _, pkg := range available {
		if pkg.Name == packageName {
			versions = append(versions, pkg.Version)
			// Match logic:
			// If tag requested, match exact.
			// If no tag, find latest (simple string compare or just pick first for MVP logic if not sorted/semver).
			if requestedTag != "" {
				if pkg.Version == requestedTag {
					p := pkg
					candidate = &p
					break
				}
			} else {
				// Pick "latest" - for MVP let's pick the last one found or rely on source order
				// Real logic should use semver sorting.
				p := pkg
				candidate = &p
			}
		}
	}

	if candidate == nil {
		if len(versions) > 0 {
			return fmt.Errorf("package '%s' found but version '%s' not available. Available versions: %v", packageName, requestedTag, versions)
		}
		return fmt.Errorf("package '%s' not found in any repository. Try running 'dropzone update'", packageName)
	}

	util.LogInfo("Found %s:%s in repo '%s'", candidate.Name, candidate.Version, candidate.SourceRepo)

	// 3. Get Control Plane
	cp, err := h.cpManager.Get(candidate.SourceRepo)
	if err != nil {
		return fmt.Errorf("failed to get control plane '%s': %w", candidate.SourceRepo, err)
	}

	// 4. Download Artifact
	tmpDir, err := os.MkdirTemp("", "dropzone-install-*")
	if err != nil {
		return fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	util.LogInfo("Downloading package artifact...")
	if err := cp.DownloadArtifact(candidate.Name, candidate.Version, tmpDir); err != nil {
		return fmt.Errorf("download failed: %w", err)
	}

	// 5. Verify Checksum/Attestation
	util.LogInfo("Verifying attestation...")
	if err := attestation.VerifySignedChecksum(tmpDir, candidate.Checksum, candidate.Signature, candidate.PublicKey); err != nil {
		return fmt.Errorf("attestation verification failed: %w", err)
	}
	util.LogInfo("Attestation verified successfully.")

	// 6. Store Package
	util.LogInfo("Installing package files...")
	installPath, err := h.store.StorePackage(candidate.Name, candidate.Version, tmpDir)
	if err != nil {
		return fmt.Errorf("failed to store package: %w", err)
	}

	// 7. Store Metadata
	candidate.InstallDate = time.Now()
	if err := h.store.StorePackageMetadata(*candidate); err != nil {
		return fmt.Errorf("failed to store metadata: %w", err)
	}

	// 8. Link Binaries (Host Integration)
	util.LogInfo("Linking binaries...")
	linked, err := h.integrator.LinkPackageBinaries(candidate.Name, candidate.Version, installPath)
	if err != nil {
		util.LogInfo("Warning: failed to link binaries: %v", err)
	} else {
		if len(linked) > 0 {
			util.LogInfo("Linked binaries: %v", linked)
		} else {
			util.LogInfo("No binaries linked.")
		}
	}

	util.LogInfo("Successfully installed %s:%s", candidate.Name, candidate.Version)
	return nil
}

func parsePackageRef(ref string) (string, string) {
	parts := strings.Split(ref, ":")
	if len(parts) > 1 {
		return parts[0], parts[1]
	}
	return parts[0], ""
}

// ListPackages displays installed and available packages.
func (h *PackageHandler) ListPackages(installedOnly, availableOnly bool, repoFilter, packageFilter string) error {
	var rows [][]string

	// Helper to add row
	addRow := func(name, version, status, source string) {
		if packageFilter != "" && name != packageFilter {
			return
		}
		if repoFilter != "" && source != repoFilter {
			return
		}
		rows = append(rows, []string{name, version, status, source})
	}

	// 1. Get Installed
	if !availableOnly {
		installed, err := h.store.GetAllInstalledPackages()
		if err != nil {
			return fmt.Errorf("failed to list installed packages: %w", err)
		}
		for _, pkg := range installed {
			addRow(pkg.Name, pkg.Version, "Installed", pkg.SourceRepo)
		}
	}

	// 2. Get Available (Remote)
	if !installedOnly {
		available, err := h.store.GetAllAvailablePackagesFromIndexes()
		if err != nil {
			return fmt.Errorf("failed to list available packages: %w", err)
		}
		for _, pkg := range available {
			addRow(pkg.Name, pkg.Version, "Available", pkg.SourceRepo)
		}
	}

	// Sort rows by name then version
	sort.Slice(rows, func(i, j int) bool {
		if rows[i][0] != rows[j][0] {
			return rows[i][0] < rows[j][0]
		}
		return rows[i][1] < rows[j][1]
	})

	// Print Table
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, "NAME\tVERSION\tSTATUS\tSOURCE")
	for _, row := range rows {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", row[0], row[1], row[2], row[3])
	}
	w.Flush()

	return nil
}

// RemovePackage removes a package.
func (h *PackageHandler) RemovePackage(packageName, targetVersion string) error {
	// 1. Find installed versions
	versions, err := h.store.GetInstalledPackageVersions(packageName)
	if err != nil {
		return fmt.Errorf("failed to lookup package versions: %w", err)
	}

	if len(versions) == 0 {
		return fmt.Errorf("package '%s' is not installed", packageName)
	}

	var versionsToRemove []string

	// 2. Resolve target version(s)
	if targetVersion != "" {
		// Specific version requested
		found := false
		for _, v := range versions {
			if v.Version == targetVersion {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("version '%s' of package '%s' is not installed", targetVersion, packageName)
		}
		versionsToRemove = append(versionsToRemove, targetVersion)
	} else {
		// No version specified
		if len(versions) == 1 {
			// Only one installed, ask to confirm removal of it
			v := versions[0].Version
			if !h.confirmRemoval(packageName, v) {
				util.LogInfo("Removal cancelled.")
				return nil
			}
			versionsToRemove = append(versionsToRemove, v)
		} else {
			// Multiple versions, prompt user
			selected, err := h.promptVersionSelection(packageName, versions)
			if err != nil {
				return err
			}
			versionsToRemove = selected
		}
	}

	// 3. Perform Removal
	for _, ver := range versionsToRemove {
		util.LogInfo("Removing %s:%s...", packageName, ver)

		// Unlink binaries
		if err := h.integrator.UnlinkPackageBinaries(packageName, ver); err != nil {
			util.LogInfo("Warning: failed to unlink binaries: %v", err)
		}

		// Remove files (this also removes metadata if stored inside)
		if err := h.store.RemovePackageFiles(packageName, ver); err != nil {
			return fmt.Errorf("failed to remove package files: %w", err)
		}

		util.LogInfo("Removed %s:%s", packageName, ver)
	}

	return nil
}

func (h *PackageHandler) confirmRemoval(name, version string) bool {
	reader := bufio.NewReader(os.Stdin)
	fmt.Printf("Are you sure you want to remove %s:%s? [y/N]: ", name, version)
	response, _ := reader.ReadString('\n')
	response = strings.TrimSpace(strings.ToLower(response))
	return response == "y" || response == "yes"
}

func (h *PackageHandler) promptVersionSelection(name string, versions []localstore.PackageMetadata) ([]string, error) {
	fmt.Printf("Multiple versions of '%s' are installed:\n", name)
	for i, v := range versions {
		fmt.Printf("%d. %s\n", i+1, v.Version)
	}
	fmt.Printf("A. All\n")
	fmt.Printf("Enter number to remove one, 'A' to remove all, or anything else to cancel: ")

	reader := bufio.NewReader(os.Stdin)
	response, _ := reader.ReadString('\n')
	response = strings.TrimSpace(strings.ToLower(response))

	if response == "a" {
		var all []string
		for _, v := range versions {
			all = append(all, v.Version)
		}
		return all, nil
	}

	var idx int
	if _, err := fmt.Sscanf(response, "%d", &idx); err == nil {
		if idx > 0 && idx <= len(versions) {
			return []string{versions[idx-1].Version}, nil
		}
	}

	return nil, fmt.Errorf("cancelled")
}
