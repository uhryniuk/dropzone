package oci

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/dropzone/internal/config"
	"github.com/dropzone/internal/controlplane"
	"github.com/dropzone/internal/localstore"
	"github.com/dropzone/internal/util"
)

func init() {
	controlplane.RegisterFactory("oci", New)
}

// OCIControlPlane implements ControlPlane for OCI registries.
type OCIControlPlane struct {
	cfg config.ControlPlaneConfig
}

// New creates a new OCIControlPlane.
func New(cfg config.ControlPlaneConfig) (controlplane.ControlPlane, error) {
	return &OCIControlPlane{cfg: cfg}, nil
}

func (c *OCIControlPlane) Name() string     { return c.cfg.Name }
func (c *OCIControlPlane) Type() string     { return "oci" }
func (c *OCIControlPlane) Endpoint() string { return c.cfg.Endpoint }

// Authenticate updates config and performs docker login.
func (c *OCIControlPlane) Authenticate(username, password, token string) error {
	c.cfg.Auth.Username = username
	c.cfg.Auth.Password = password
	c.cfg.Auth.Token = token

	// We assume Endpoint is the registry URL for login purposes.
	// If Endpoint is "oci://registry.com/path", we parse out registry.com
	server := strings.TrimPrefix(c.cfg.Endpoint, "oci://")
	server = strings.Split(server, "/")[0]

	util.LogInfo("Authenticating with OCI registry: %s", server)

	// Use password or token
	secret := password
	if token != "" {
		secret = token
	}

	cmd := exec.Command("docker", "login", "-u", username, "--password-stdin", server)
	cmd.Stdin = strings.NewReader(secret)

	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("docker login failed: %s, %w", string(out), err)
	}

	return nil
}

// ListPackageNames is a stub for MVP as OCI catalog API is not universally supported/easy via CLI.
func (c *OCIControlPlane) ListPackageNames() ([]string, error) {
	// TODO: Implement using catalog API or infer from endpoint conventions
	util.LogDebug("ListPackageNames not fully implemented for OCI in MVP (requires OCI Catalog API support)")
	return []string{}, nil
}

// GetPackageTags is a stub for MVP.
func (c *OCIControlPlane) GetPackageTags(packageName string) ([]string, error) {
	// TODO: Implement using tags list API
	util.LogDebug("GetPackageTags not fully implemented for OCI in MVP")
	return []string{}, nil
}

// GetPackageMetadata retrieves metadata. For MVP, we might need to pull the manifest.
// Using `docker manifest inspect` (experimental) or `skopeo inspect`.
// Since we rely on docker, we'll try to use a lightweight pull or inspection if possible.
// For now, returning a basic metadata struct or erroring if we can't fetch.
func (c *OCIControlPlane) GetPackageMetadata(packageName, tag string) (*localstore.PackageMetadata, error) {
	// Full implementation requires pulling/inspecting manifest to get labels.
	// For MVP, we might defer this until download, or implement a basic inspection.

	// Construct image reference
	imageRef := c.getImageRef(packageName, tag)

	util.LogInfo("Fetching metadata for %s...", imageRef)

	// Attempt `docker manifest inspect`
	// Note: This might require `export DOCKER_CLI_EXPERIMENTAL=enabled` in some environments.
	cmd := exec.Command("docker", "manifest", "inspect", imageRef)
	// We might need to ensure we are logged in (handled by Authenticate/docker config)

	out, err := cmd.Output()
	if err != nil {
		// Fallback: If manifest inspect fails (e.g. locally missing), maybe we just return basic info
		// and let DownloadArtifact handle the rest, but VerifySignedChecksum needs expected checksum.
		// If we can't get metadata (checksum) from registry, we can't verify before download.
		// For MVP without a dedicated library, we'll return a placeholder or error.
		return nil, fmt.Errorf("failed to inspect manifest (ensure DOCKER_CLI_EXPERIMENTAL=enabled or image exists): %v", err)
	}

	// Parse manifest to find labels/annotations
	// This is complex as `docker manifest inspect` returns the manifest JSON which varies (v2 schema 1/2, list).
	// For MVP simplicity, let's assume we can't get it easily without a library and return a placeholder.
	// Real implementation should use `google/go-containerregistry`.

	util.LogDebug("Metadata inspection raw output length: %d", len(out))

	return &localstore.PackageMetadata{
		Name:    packageName,
		Version: tag,
		// Checksum: ??? - We need this for attestation.
		// In a real system, the checksum is likely stored in a label or a separate file in the registry.
		// For MVP, we might have to skip remote checksum retrieval if we can't parse manifest labels easily via CLI.
	}, nil
}

// DownloadArtifact pulls the image and extracts the /dropzone/install contents.
func (c *OCIControlPlane) DownloadArtifact(packageName, tag, destinationPath string) error {
	imageRef := c.getImageRef(packageName, tag)

	util.LogInfo("Pulling OCI image: %s", imageRef)
	pullCmd := exec.Command("docker", "pull", imageRef)
	pullCmd.Stdout = os.Stdout
	pullCmd.Stderr = os.Stderr
	if err := pullCmd.Run(); err != nil {
		return fmt.Errorf("failed to pull image: %w", err)
	}

	// Create temp container
	containerName := fmt.Sprintf("dropzone-down-%d", time.Now().UnixNano())
	util.LogDebug("Creating temp container %s", containerName)
	createCmd := exec.Command("docker", "create", "--name", containerName, imageRef)
	if out, err := createCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to create container: %s, %w", string(out), err)
	}
	defer exec.Command("docker", "rm", "-f", containerName).Run()

	// Extract /dropzone/install
	// destinationPath is the target directory (usually a temp dir created by caller)
	// docker cp source target
	source := fmt.Sprintf("%s:/dropzone/install/.", containerName)
	util.LogInfo("Extracting artifacts to %s...", destinationPath)

	cpCmd := exec.Command("docker", "cp", source, destinationPath)
	if out, err := cpCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to copy artifacts: %s, %w", string(out), err)
	}

	return nil
}

func (c *OCIControlPlane) getImageRef(packageName, tag string) string {
	// Endpoint: oci://registry.com/namespace
	base := strings.TrimPrefix(c.cfg.Endpoint, "oci://")
	base = strings.TrimSuffix(base, "/")

	// If package name is implied by endpoint (single package repo), usage might differ.
	// But assuming generic registry path: registry.com/namespace/packageName:tag
	return fmt.Sprintf("%s/%s:%s", base, packageName, tag)
}
