# Feature: Remote Package Installation

## 1. Overview

This document details the design and implementation for the `dropzone install` command, which enables users to download a care package from a configured control plane, **fully verify its integrity and authenticity using signed checksums**, resolve any **binary name conflicts** during integration, extract its contents, and integrate it into the host system. This is the core mechanism for consuming care packages distributed remotely, prioritizing security and host system stability.

## 2. Goals

*   Allow users to install care packages from any configured decentralized control plane, including private ones requiring authentication.
*   **Guarantee the integrity and authenticity of downloaded care packages through cryptographic signed checksum verification.**
*   **Prevent unpredictable behavior and user frustration due to binary name conflicts during host integration.**
*   Seamlessly integrate remote packages into the host's `PATH`, making them immediately usable.
*   Provide clear progress and feedback during the download, verification, and installation process.
*   Support installing specific package versions or the latest available version.

## 3. Components

### 3.1. `internal/packagehandler/packagehandler.go` (Installation Specific Logic)

*   **Responsibility:** Orchestrates the entire remote package installation workflow, from discovery and download to signed attestation and host integration.
*   **Functionality - `InstallPackage(packageName, requestedTag string)`:**
    *   Receives the target package name and an optional specific tag/version.
    *   Checks `internal/localstore` first: if the package is already installed locally (and matches `requestedTag`), it might skip or offer to reinstall.
    *   If not local, consults the `controlplane.Manager` to find the package and its available tags across all configured control planes.
    *   If `requestedTag` is empty, resolves to the latest stable tag available.
    *   Determines the originating control plane for the chosen package/tag.
    *   Calls `controlplane.GetPackageMetadata` to fetch package metadata, including its expected signed checksum (checksum string, signature, public key reference).
    *   Calls `controlplane.DownloadArtifact` to download the raw care package artifact (e.g., OCI image, tarball) to a temporary local path, *authenticating if the control plane is private*.
    *   **Calls `attestation.VerifySignedChecksum` on the downloaded artifact using the fetched metadata (checksum, signature, public key). If verification fails, the installation is aborted with a clear security warning.**
    *   Extracts the contents of the downloaded artifact into a temporary directory, ensuring the `/dropzone/install` structure is respected. This might involve `docker save | tar xf` or specific OCI library extraction (e.g., from `oras-project/oras-go` for direct content extraction from OCI images).
    *   Moves the extracted contents from the temporary directory to the permanent `~/.dropzone/packages/<name>/<version>/` location via `localstore.StorePackage`.
    *   Calls `localstore.StorePackageMetadata` to persist metadata for the installed package.
    *   **Calls `hostintegration.LinkPackageBinaries` to create symbolic links to `~/.dropzone/bin`, which will handle binary conflict detection and resolution.**
    *   Cleans up temporary files and directories.

### 3.2. `internal/controlplane/controlplane.go` (Download Artifact)

*   **Responsibility:** Provides the control plane-specific logic for downloading care package artifacts.
*   **Functionality (via `ControlPlane` interface):**
    *   **`DownloadArtifact(packageName, tag, destinationPath string, auth *AuthOptions) error`:**
        *   Each concrete `ControlPlane` implementation (e.g., OCI, GitHub, S3) will provide its specific method for downloading the actual package artifact (e.g., pulling an OCI image and saving it, fetching a tarball from GitHub Releases).
        *   The `destinationPath` will be a temporary location where the raw artifact is saved.
        *   **It will utilize the provided `AuthOptions` for authentication during the download, if required by the control plane.**

### 3.3. `internal/download/download.go`

*   **Responsibility:** Provides generic file download capabilities, utilized by control plane implementations.
*   **Functionality:**
    *   **`DownloadFile(url string, destinationPath string, auth *AuthOptions) error`:**
        *   Performs a robust HTTP/HTTPS download.
        *   Handles network errors, timeouts, and redirects.
        *   **Accepts optional `AuthOptions` for basic or token-based HTTP authentication.**
        *   (Optional for MVP) Provides progress reporting.

### 3.4. `internal/attestation/attestation.go` (Signed Checksum Verification)

*   **Responsibility:** Verifies the integrity and authenticity of downloaded care package artifacts.
*   **Functionality:**
    *   **`VerifySignedChecksum(filePath string, expectedChecksum string, signature []byte, publicKeyRef string) error`:**
        *   Calculates the checksum of the downloaded `filePath`.
        *   Compares the calculated checksum against the `expectedChecksum` from metadata.
        *   **Verifies the `signature` against the `expectedChecksum` using the `publicKeyRef` (e.g., GPG public key).**
        *   Returns an error if checksums do not match OR if the signature is invalid/unverifiable.

### 3.5. `internal/localstore/localstore.go` (Storing Installed Packages)

*   **Responsibility:** Manages the storage of extracted care package files and their metadata.
*   **Functionality:**
    *   **`StorePackage(packageName, packageVersion, sourcePath string) (string, error)`:**
        *   Moves/copies the contents from a `sourcePath` (e.g., temporary extraction directory) to the permanent `~/.dropzone/packages/<name>/<version>/` directory.
        *   Returns the final installed path.
    *   **`GetInstalledPackagePath(packageName, packageVersion string) (string, error)`:**
        *   Returns the filesystem path to an installed package.
    *   **`GetPackageMetadata(packageName, packageVersion string) (*PackageMetadata, error)`:**
        *   Retrieves the metadata associated with an installed package.

### 3.6. `internal/hostintegration/hostintegration.go` (Linking and Conflict Resolution)

*   **Responsibility:** Integrates the binaries of the extracted care package into the host's `PATH`, *robustly handling binary name conflicts*.
*   **Functionality:**
    *   **`LinkPackageBinaries(packageName, packageVersion, packageInstallPath string) ([]string, error)`:**
        *   As described in `host_integration_and_package_definition.md`, this function now includes **mandatory conflict detection and resolution logic** for binaries.
        *   It will detect conflicts with other `dropzone`-managed binaries (defaulting to overwriting, with a warning) and with system-installed binaries (defaulting to non-overwrite, with a warning and suggestion for alternative).

## 4. `dropzone` CLI Integration

*   **`dropzone install <package-name>[:<tag>]`:**
    *   Invokes `packagehandler.InstallPackage` with the provided package name and optional tag.
    *   Displays informative messages to the user during:
        *   Package lookup (including control plane selection).
        *   Authentication prompts if required for private control planes.
        *   Download progress (if supported by `internal/download`).
        *   **Signed checksum verification status (success/failure).**
        *   Extraction progress.
        *   **Host integration, including warnings and feedback on binary conflicts.**
        *   Success or failure messages.

## 5. Technical Details

*   **Artifact Extraction:** For OCI images, the `DownloadArtifact` method in `internal/controlplane/oci` might involve:
    1.  Using `docker pull` or `podman pull` to get the image locally (these commands use existing credential helpers).
    2.  Using `docker save` or `podman save` to export the image as a tarball.
    3.  Extracting the tarball (which typically contains OCI layers and manifest) to find the `/dropzone/install` contents. This might require inspecting image layers or directly extracting the final layer filesystem. Alternatively, using `skopeo copy --format oci-dir` and then extracting the specific layer or the entire directory can be used for direct content extraction.
*   **Signed Checksum Distribution:** For MVP, signed checksums (and public key references) will be expected as annotations or labels within the OCI image manifest, or as separate metadata files alongside the artifact in other control plane types (e.g., `.checksum.sig` file in GitHub Releases).
*   **GPG Integration:** For signing/verification, `os/exec` calls to the `gpg` command-line tool are a viable MVP approach, leveraging the user's existing GPG setup.
*   **Temporary Files:** Heavy reliance on `os.MkdirTemp` and `os.RemoveAll` for managing temporary download and extraction directories.
*   **Concurrency:** Downloads and extractions should be managed carefully to avoid resource contention, especially if multiple packages are to be installed concurrently (future consideration).
*   **Error Handling:** Detailed error messages are crucial for debugging failed installations, indicating issues with network, authentication, checksum verification, signature authenticity, extraction, or host integration conflicts.

## 6. Testing

### 6.1. Unit Tests

*   **`internal/packagehandler/packagehandler.go` (Installation Logic):**
    *   Test orchestration of `InstallPackage`, mocking all external dependencies (`controlplane`, `download`, `attestation`, `localstore`, `hostintegration`).
    *   Test correct package/tag resolution (latest vs. specific tag).
    *   Test error handling for each stage (e.g., package not found, download failure, authentication failure, **signed checksum mismatch/invalid signature**, extraction error, linking error).
    *   Ensure temporary files are cleaned up even on failure.
    *   Test various responses from `hostintegration.LinkPackageBinaries` regarding conflicts.
*   **`internal/controlplane/*/oci.go` (DownloadArtifact):**
    *   Mock `exec.Command` for `docker/podman pull` and `save` or mock OCI registry API calls to simulate successful and failed downloads, *including scenarios with and without authentication*.
    *   Verify the artifact is saved to the correct temporary path.
*   **`internal/download/download.go`:**
    *   Test `DownloadFile` with mock HTTP servers to simulate various scenarios: successful download, 404, network error, partial download, corrupted data, *and successful/failed downloads with basic HTTP authentication*.
*   **`internal/attestation/attestation.go`:**
    *   Test `VerifySignedChecksum` with known good and bad files/checksums/signatures/public keys.
    *   Mock GPG or other signing tool executions to test signature verification with various scenarios (valid, invalid, expired key).

### 6.2. Integration Tests

*   **End-to-end `dropzone install` (Public Repository):**
    *   Set up a local test OCI registry (e.g., `registry:2`) pre-populated with a `dropzone`-compliant care package and its manifest including a *valid signed checksum*.
    *   Configure `dropzone add repo` to point to this test registry.
    *   Run `dropzone update`.
    *   Execute `dropzone install testapp:1.0.0`.
    *   Verify that:
        *   The package is downloaded, **signed checksum verified**, extracted to `~/.dropzone/packages/testapp/1.0.0/`.
        *   The binaries are correctly symlinked to `~/.dropzone/bin/`.
        *   The package metadata is stored in `localstore`.
        *   The `testapp` binary is executable from the mock shell.
    *   Test `dropzone install` with a non-existent package or tag, verifying appropriate error messages.
    *   Test with a corrupted artifact (e.g., manually alter the checksum in the manifest or the downloaded file, or tamper with the signature) to ensure **signed checksum verification fails correctly** and aborts installation.
*   **End-to-end `dropzone install` (Private Repository with Authentication):**
    *   Set up a local authenticated OCI registry with a signed care package.
    *   Configure `dropzone add repo` with correct authentication details.
    *   Execute `dropzone install private-app:1.0.0`. Verify successful installation.
    *   Repeat with incorrect authentication details, verify authentication failure and installation abort.
*   **Binary Conflict Resolution Integration:**
    *   Simulate installing `app1:1.0.0` with `binaryA`.
    *   Simulate installing `app2:1.0.0` which also provides `binaryA`. Verify `dropzone` warns and `app2`'s `binaryA` takes precedence.
    *   Simulate a system-wide binary (e.g., `/usr/local/bin/mytool`).
    *   Simulate installing `app3:1.0.0` which also provides `mytool`. Verify `dropzone` warns and does *not* overwrite the system binary, but still installs other binaries from `app3`.

## 7. Open Questions / Future Considerations

*   **Bandwidth Optimization:** How to handle large care packages efficiently? Resume broken downloads? Delta updates for subsequent versions?
*   **Resource Management:** How to limit CPU/memory/disk usage during download and extraction, especially for large packages.
*   **Rollbacks:** Ability to revert to a previous version of an installed care package if an installation fails or a new version is problematic.
*   **Package Dependencies:** If care packages have dependencies on other care packages, how would `dropzone` resolve and install them? (Major future feature).
*   **Privilege Escalation:** How to handle installations that require root privileges for specific host integration steps (e.g., installing to `/usr/local/bin` instead of `~/.dropzone/bin`)? For MVP, `~/.dropzone` is user-level.
*   **Download Mirroring/Caching:** Can `dropzone` use local mirrors or content delivery networks (CDNs) for faster downloads?
*   **User Interaction for Shell Config:** Improve the user experience for prompting to source shell configuration files after `PATH` modifications.
*   **Alternative Host Integration:** Explore FUSE-based mounting for more complex, read-only filesystem layers for certain care packages.