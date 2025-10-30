package builder

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/dropzone/internal/util"
)

// Builder manages interactions with container runtimes to build packages.
type Builder struct {
	Runtime string // "docker" or "podman"
}

// New creates a new Builder instance.
func New(runtime string) *Builder {
	return &Builder{
		Runtime: runtime,
	}
}

// VerifyRuntime checks if the configured runtime is available.
func (b *Builder) VerifyRuntime() error {
	cmd := exec.Command(b.Runtime, "--version")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("container runtime '%s' is not available: %w", b.Runtime, err)
	}
	return nil
}

// BuildAndExtract builds a Dockerfile and extracts the /dropzone/install directory.
// It returns the path to the temporary directory containing the extracted artifacts.
func (b *Builder) BuildAndExtract(dockerfilePath, buildContextPath, packageName, packageVersion string, buildArgs, envVars map[string]string) (string, error) {
	// 1. Build the image
	// We tag it temporarily to reference it for extraction.
	// Tag format: dropzone-build-<packageName>:<packageVersion>
	imageTag := fmt.Sprintf("dropzone-build-%s:%s", packageName, packageVersion)

	// Construct build command
	args := []string{"build", "-t", imageTag, "-f", dockerfilePath}

	// Add build args
	for k, v := range buildArgs {
		args = append(args, "--build-arg", fmt.Sprintf("%s=%s", k, v))
	}

	// Treat envVars as build-args for the build process, as CLI --env is not standard for build commands
	for k, v := range envVars {
		args = append(args, "--build-arg", fmt.Sprintf("%s=%s", k, v))
	}

	args = append(args, buildContextPath)

	util.LogInfo("Building container image: %s %s", b.Runtime, strings.Join(args, " "))
	cmd := exec.Command(b.Runtime, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ() // Pass current env

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("build failed: %w", err)
	}

	// 2. Create a temporary container from the image (without running it)
	// We use 'create' so we can copy files out.
	containerName := fmt.Sprintf("dropzone-extract-%s-%s", packageName, packageVersion)
	// Ensure cleanup of any previous failed attempt
	exec.Command(b.Runtime, "rm", "-f", containerName).Run()

	util.LogInfo("Creating temporary container for extraction...")
	createCmd := exec.Command(b.Runtime, "create", "--name", containerName, imageTag)
	if out, err := createCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("failed to create temporary container: %s, %w", string(out), err)
	}

	// Ensure we remove the container when done
	defer func() {
		util.LogDebug("Removing temporary container %s", containerName)
		exec.Command(b.Runtime, "rm", "-f", containerName).Run()
	}()

	// 3. Extract /dropzone/install
	tmpExtractDir, err := os.MkdirTemp("", "dropzone-build-extract-*")
	if err != nil {
		return "", fmt.Errorf("failed to create temp extraction dir: %w", err)
	}

	// cp syntax: <container>:<path> <local_path>
	// We copy /dropzone/install/. to tmpExtractDir to get contents directly
	sourcePath := fmt.Sprintf("%s:/dropzone/install/.", containerName)

	util.LogInfo("Extracting package artifacts...")
	cpCmd := exec.Command(b.Runtime, "cp", sourcePath, tmpExtractDir)
	if out, err := cpCmd.CombinedOutput(); err != nil {
		os.RemoveAll(tmpExtractDir) // Cleanup on fail
		return "", fmt.Errorf("failed to extract artifacts (ensure /dropzone/install exists in image): %s, %w", string(out), err)
	}

	return tmpExtractDir, nil
}
