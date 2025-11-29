# Feature: Local Package Building

## 1. Overview

This document details the design and implementation for enabling `dropzone` users to build "care packages" locally from a `Dockerfile` (or `Containerfile`). This feature leverages existing container runtimes (Docker or Podman) to perform multi-stage builds, **allowing custom build arguments and environment variables to be passed to the container runtime**. After a successful build, `dropzone` extracts the final package contents into its local storage and **generates a cryptographic checksum which can then be signed by the user** for integrity and authenticity.

## 2. Goals

*   Allow users to build care packages directly from a local Dockerfile using their preferred OCI runtime (Docker/Podman).
*   **Enable package creators to pass custom build arguments and environment variables during the build process.**
*   Ensure the build process adheres to the `dropzone` package definition interface (`/dropzone/install` directory structure and `dropzone.package` label).
*   Extract built artifacts efficiently into `dropzone`'s local storage.
*   **Generate a cryptographic checksum for the built package and provide a mechanism for the user to sign it for authenticity.**
*   Provide clear feedback on the build status and any errors encountered.

## 3. Components

### 3.1. `internal/builder/builder.go`

*   **Responsibility:** Abstracts interactions with local container runtimes (Docker/Podman) for building and extracting container contents.
*   **Functionality:**
    *   **`BuildAndExtract(dockerfilePath, buildContextPath, packageName, packageVersion string, buildArgs, envVars map[string]string) (string, error)`:**
        *   Takes the path to a `Dockerfile`, the build context path, target package name/version, and **maps for `buildArgs` and `envVars`**.
        *   Determines the active container runtime (Docker or Podman) based on `dropzone`'s configuration or environment variables.
        *   Executes `docker build` or `podman build` command, **incorporating the provided `buildArgs` (`--build-arg KEY=VALUE`) and `envVars` (`--env KEY=VALUE`)**.
        *   After a successful build, it creates a temporary container from the final image stage.
        *   Uses `docker cp` or `podman cp` to extract the contents of `/dropzone/install` from the temporary container to a temporary directory on the host filesystem.
        *   Removes the temporary container.
        *   Returns the path to the temporary host directory containing the extracted package contents.
        *   Handles build failures and extraction errors gracefully.
    *   **`VerifyRuntime(runtime string) error`:** (Could be shared with `hostintegration` or part of a common `runtime` package).
        *   Checks if the specified container runtime (Docker or Podman) is installed and operational.

### 3.2. `internal/packagehandler/packagehandler.go` (Build Specific Logic)

*   **Responsibility:** Orchestrates the entire local package build workflow, validating package definitions, managing local storage, and overseeing checksum generation and signing.
*   **Functionality:**
    *   **`BuildPackage(packageName, dockerfilePath, buildContextPath string, buildArgs, envVars map[string]string)`:**
        *   Receives the package name, Dockerfile path, build context, and **custom build arguments/environment variables**.
        *   Validates the `Dockerfile` (e.g., checks for `LABEL dropzone.package`, ensuring `/dropzone/install` target is likely).
        *   Calls `builder.BuildAndExtract` to perform the container build and extraction, **passing `buildArgs` and `envVars`**.
        *   Generates a version for the package (e.g., based on a timestamp, Dockerfile hash, or user input).
        *   Calls `attestation.GenerateChecksum` on the extracted temporary directory.
        *   **Interactively prompts the user to sign the generated checksum** (e.g., via GPG, if a key is configured or specified). The signature and public key reference are stored along with the checksum.
        *   Creates a `PackageMetadata` struct (name, version, checksum, signature, public key reference, build-time info).
        *   Calls `localstore.StorePackage(packageName, packageVersion, extractedPath)` to move the extracted contents from the temporary directory to the permanent `~/.dropzone/packages/<name>/<version>/` location.
        *   Calls `localstore.StorePackageMetadata(metadata)` to persist metadata.
        *   Cleans up the temporary extraction directory.

### 3.3. `internal/attestation/attestation.go` (Checksum Generation and Signing)

*   **Responsibility:** Generates cryptographic checksums for package contents and provides a mechanism for signing them.
*   **Functionality:**
    *   **`GenerateChecksum(path string) (string, error)`:**
        *   Calculates a SHA256 (or similar) checksum for all files within the given directory path.
        *   The checksum should be stable and reproducible for the same directory contents regardless of file order or last modified times (e.g., by sorting file paths).
        *   Returns the checksum string.
    *   **`SignChecksum(checksum string, signingKeyIdentifier string) (signature []byte, publicKeyRef string, error)`:**
        *   Takes the generated checksum and an identifier for the signing key (e.g., GPG key ID, path to a private key file).
        *   Interactively prompts the user for passphrase if required.
        *   Cryptographically signs the checksum.
        *   Returns the binary signature and a reference to the public key used for verification.
    *   **`PromptForSigning(checksum string) (signature []byte, publicKeyRef string, error)`:**
        *   An interactive helper function to guide the user through the signing process, asking for key identifiers, passphrases, etc.

## 4. `dropzone` CLI Integration

*   The `dropzone build <package-name> <dockerfile-path> [--context <path>] [--build-arg KEY=VALUE] [--env KEY=VALUE]` command will trigger the `packagehandler.BuildPackage` function.
*   It should provide clear output to the user regarding the build progress, success, or failure, including prompts for signing.

## 5. Technical Details

*   **Container Runtime Interaction:** Primarily via `os/exec` to invoke `docker` or `podman` CLI commands.
*   **Dockerfile Validation:** Basic parsing of the `Dockerfile` to look for specific instructions/labels, or relying on `builder` output for structure validation.
*   **Checksum Algorithm:** SHA256 for integrity verification.
*   **Checksum Signing:** Integration with external tools like GPG (`os/exec`) for cryptographic signing. A simple, configurable interface for signing commands will be required. Alternatively, a Go-native signing library could be explored for supported key types.
*   **Temporary Files:** Utilize `os.MkdirTemp` and `os.RemoveAll` for safe management of temporary directories.

## 6. Testing

### 6.1. Unit Tests

*   **`internal/builder/builder.go`:**
    *   Mock `exec.Command` calls for `docker` and `podman` to ensure correct arguments are passed for `build` (including `--build-arg` and `--env`), `create`, `cp`, `rm`.
    *   Test error handling for failed `build` commands (e.g., invalid Dockerfile, build errors).
    *   Test error handling for failed `cp` commands (e.g., source not found).
    *   Ensure temporary containers are always removed, even on error.
*   **`internal/packagehandler/packagehandler.go` (Build Logic):**
    *   Test orchestration of `BuildPackage`, mocking `builder`, `attestation`, and `localstore` interactions.
    *   Test validation of `Dockerfile` (mocking content reading).
    *   Verify correct metadata (including checksum and signature reference) is generated and stored.
    *   Test error handling if any sub-component fails.
    *   Mock interactive prompts for signing to ensure correct flow.
*   **`internal/attestation/attestation.go`:**
    *   Test `GenerateChecksum` with various dummy file structures (empty directory, single file, multiple files, nested directories) to ensure consistent and correct checksum generation.
    *   Verify checksums match for identical content and differ for changed content.
    *   Mock GPG or other signing tool executions to test `SignChecksum` with various key types and passphrase requirements.
    *   Test `PromptForSigning` flow with mock user input.

### 6.2. Integration Tests

*   **End-to-end `dropzone build`:**
    *   Create a temporary directory with a valid `Dockerfile` that produces a `dropzone`-compliant care package (e.g., a simple Go binary).
    *   Execute `dropzone build testapp ./Dockerfile --context . --build-arg VERSION=1.0.0 --env DEBUG=true`.
    *   Verify that `testapp` is extracted to `~/.dropzone/packages/testapp/<version>/`.
    *   Verify that `~/.dropzone/packages/testapp/<version>/bin/` contains the expected executable.
    *   Verify that package metadata (including checksum and a placeholder/mocked signature) is correctly stored in `localstore`.
    *   Test with an invalid `Dockerfile` to ensure `dropzone build` fails gracefully with informative errors.
*   **Checksum Signing Integration:**
    *   Perform a `dropzone build` operation in a test environment where a GPG key is configured.
    *   Verify that the signing prompt appears and, upon successful (mocked) signing, the signature is stored with the package metadata.
*   **Container Runtime Selection:**
    *   Test with Docker available and Podman unavailable.
    *   Test with Podman available and Docker unavailable.
    *   (Future) Test with both available and specific runtime selection.

## 7. Open Questions / Future Considerations

*   **Signing Key Management:** How to securely manage signing keys (e.g., GPG keyrings, hardware tokens)? For MVP, rely on user's existing GPG setup. Future could include `dropzone`-specific key management.
*   **Signature Format:** What specific signature format should be used (e.g., detached GPG signature, Cosign attestations)? MVP will use a standard format easily verifiable.
*   **Dockerfile Validation Robustness:** How deeply should `dropzone` validate the `Dockerfile`? A simple label check is MVP, but deeper static analysis could prevent common user errors.
*   **Build Caching:** How to leverage container image layer caching effectively to speed up subsequent builds of the same Dockerfile? The current approach extracts from the final stage, but `dropzone` could manage intermediate image tags.
*   **Interactive Build Process:** Should `dropzone` stream the container build logs directly to the user's console? (Yes, for good UX, with options for quiet/verbose).
*   **Cross-architecture Builds:** (Future) How to support building for different target architectures (e.g., ARM on an x86 host) using `buildx` or Podman equivalents.