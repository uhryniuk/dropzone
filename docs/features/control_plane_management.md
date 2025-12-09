# Feature: Control Plane Management (Add/Update)

## 1. Overview

This document describes the design and implementation for `dropzone`'s decentralized control plane management. This feature allows users to add, track, and update information from various remote repositories (control planes) that host care package artifacts. By supporting diverse repository types like OCI registries, GitHub Releases, and S3 buckets, `dropzone` empowers users to leverage existing distribution channels, **including private repositories that require authentication**.

**Additionally, `dropzone` streamlines the experience for GitHub users by automatically discovering care packages from a user's dedicated `care-package` repository.**

## 2. Goals

*   Enable users to define and manage multiple decentralized care package sources.
*   Support various common artifact distribution methods (OCI registries, GitHub, S3).
*   **Provide robust mechanisms for authenticating with private control planes.**
*   **Simplify repository addition for GitHub users via convention-based discovery (e.g., `<username>/care-package`).**
*   Provide a mechanism to refresh the local cache of available packages and their versions/tags from configured control planes.
*   Ensure robust and extensible handling of different control plane types.
*   Provide clear feedback to the user on the status of control planes and available packages.

## 3. Components

### 3.1. `internal/controlplane/controlplane.go`

*   **Responsibility:** Defines the generic `ControlPlane` interface and manages all registered control planes.
*   **Functionality:**
    *   **`ControlPlane` Interface:**
        *   `Name() string`: Returns the unique name of the control plane.
        *   `Type() string`: Returns the type (e.g., "oci", "github", "s3").
        *   `Endpoint() string`: Returns the base URL/identifier of the control plane.
        *   `ListPackageNames() ([]string, error)`: Lists all unique care package names available in this control plane.
        *   `GetPackageTags(packageName string) ([]string, error)`: Lists all available tags (versions) for a specific care package.
        *   `GetPackageMetadata(packageName, tag string) (*PackageMetadata, error)`: Retrieves detailed metadata for a specific package version (including signed checksums).
        *   `DownloadArtifact(packageName, tag, destinationPath string) error`: Downloads the care package artifact to a specified local path.
        *   **`Authenticate(username, password, token string) error`**: A method to perform explicit authentication against the control plane, if required. This allows for updating credentials.
    *   **`Manager` Struct:**
        *   Manages a collection of registered `ControlPlane` implementations.
        *   `Add(name, type, endpoint string, authOpts AuthOptions)`: Registers a new control plane, storing its configuration (including encrypted authentication details) in `internal/config`. Instantiates the correct `ControlPlane` implementation.
        *   **`AddFromGitHubUser(username string, authOpts AuthOptions)` (New):** Convenience method. Checks for `<username>/care-package` on GitHub. If found, adds it as a GitHub-type control plane using the releases endpoint.
        *   `Remove(name string)`: Deregisters a control plane.
        *   `List() []ControlPlane`: Returns all registered control planes.
        *   `Get(name string) (ControlPlane, error)`: Retrieves a specific registered control plane.
        *   `UpdateAll()`: Iterates through all registered control planes, calls their `ListPackageNames` and `GetPackageTags` methods, and updates the local package index in `internal/localstore`. This will implicitly handle authentication during discovery.
        *   **Factory Method:** A private factory function to create concrete `ControlPlane` implementations (e.g., `ociControlPlane`, `githubControlPlane`) based on the provided `type` string and `AuthOptions`.

### 3.2. `internal/controlplane/oci/oci.go`

*   **Responsibility:** Implements the `ControlPlane` interface for OCI (Open Container Initiative) registries, including authentication.
*   **Functionality:**
    *   `NewOCIControlPlane(name, endpoint string, auth AuthOptions) ControlPlane`: Constructor. `AuthOptions` will contain username/password/token.
    *   Implement `Name()`, `Type()`, `Endpoint()`.
    *   **`Authenticate(username, password, token string) error`**: For OCI registries, this will primarily involve configuring the underlying OCI client library (or Docker/Podman client) with the provided credentials. This might also test credentials by attempting a login.
    *   `ListPackageNames()`: Interacts with the OCI registry API to discover available repositories (care package names). This might involve listing images in a specific path (e.g., `myregistry.com/dropzone-packages/*`). **Authentication credentials will be used for private registries.**
    *   `GetPackageTags(packageName string)`: Fetches tags for a given OCI image (package). **Authentication credentials will be used.**
    *   `GetPackageMetadata(packageName, tag string)`: Pulls the OCI image manifest for a specific tag and extracts metadata (e.g., from `Labels` within the manifest, particularly `dropzone.package`, checksum annotations, and *signature references*). **Authentication credentials will be used.**
    *   `DownloadArtifact(packageName, tag, destinationPath string)`: Pulls the specific OCI image (care package container) and, for MVP, extracts its contents to the `destinationPath`. This might involve using `docker pull`/`podman pull` (which use existing credential helpers) and then `docker save`/`podman save` followed by extraction, or directly extracting layers if image format is known (e.g., `skopeo copy`). **Authentication credentials will be used during the pull operation.**

### 3.3. `internal/controlplane/github/github.go` (MVP Placeholder for Auth)

*   **Responsibility:** Implements the `ControlPlane` interface for GitHub Releases, including authentication for private repositories.
*   **Functionality:**
    *   `NewGitHubControlPlane(name, endpoint string, auth AuthOptions) ControlPlane`: Constructor. `AuthOptions` will contain a personal access token.
    *   Implement `Name()`, `Type()`, `Endpoint()`.
    *   **`Authenticate(username, password, token string) error`**: This will validate the provided GitHub Personal Access Token (PAT) by making a simple authenticated API call.
    *   `ListPackageNames()`, `GetPackageTags()`, `GetPackageMetadata()`, `DownloadArtifact()`: Will involve GitHub API calls to list releases, assets, and download tarballs. **All API calls will use the provided PAT for private repository access.**

### 3.4. `internal/controlplane/s3/s3.go` (MVP Placeholder for Auth)

*   **Responsibility:** Implements the `ControlPlane` interface for S3 buckets, including authentication.
*   **Functionality:**
    *   `NewS3ControlPlane(name, endpoint string, auth AuthOptions) ControlPlane`: Constructor. `AuthOptions` will contain AWS access key ID and secret access key.
    *   Implement `Name()`, `Type()`, `Endpoint()`.\
    *   **`Authenticate(username, password, token string) error`**: This will validate the provided AWS credentials by attempting a simple S3 API operation (e.g., listing a bucket or checking an object's existence).
    *   `ListPackageNames()`, `GetPackageTags()`, `GetPackageMetadata()`, `DownloadArtifact()`: Will involve S3 API calls to list objects, parse object keys for package names/versions, and download objects. **All S3 operations will use the provided AWS credentials.**

### 3.5. `internal/config/config.go` (Control Plane Configuration & Credentials)

*   **Responsibility:** Manages `dropzone`'s persistent configuration, now including sensitive control plane authentication details.
*   **Functionality:**
    *   **`Config` struct update:** Include a `ControlPlaneConfig` type that contains fields for `Name`, `Type`, `Endpoint`, and `AuthOptions` (which itself holds `Username`, `Password`, `Token`, `AccessKey`, `SecretKey`).
    *   **Sensitive Data Handling:** Passwords, tokens, and secret keys MUST be encrypted before being written to `~/.dropzone/config.yaml` and decrypted upon reading. A simple, configurable encryption mechanism (e.g., using a master password or system keyring) is required for MVP. If no encryption is configured, warn the user about storing sensitive data in plaintext.
    *   `Load` and `Save` methods for `Config` will handle encryption/decryption.

### 3.6. `internal/localstore/localstore.go` (Control Plane Integration)

*   **Responsibility:** Stores the locally cached index of available packages from control planes.
*   **Functionality:**
    *   `StoreControlPlaneIndex(controlPlaneName string, index map[string][]PackageMetadata)`: Saves the fetched metadata from a control plane.
    *   `GetControlPlaneIndex(controlPlaneName string) (map[string][]PackageMetadata, error)`: Retrieves the cached index.
    *   `GetAllAvailablePackages()`: Aggregates package information from all cached control plane indices for listing.

### 3.7. `internal/download/download.go`

*   **Responsibility:** Provides generic artifact downloading capabilities, utilized by control plane implementations.
*   **Functionality:**
    *   `DownloadFile(url string, destinationPath string, auth *AuthOptions) error`: Downloads a file from a given URL to a specified path. Handles basic HTTP/HTTPS. **Can accept optional authentication details for basic/token-based HTTP authentication.**

## 4. `dropzone` CLI Integration

*   **`dropzone add repo <name> <type> <endpoint> [--username <user>] ...`** (Explicit):
    *   Invokes `controlplane.Manager.Add` to register the new repository.
    *   Validates the `<type>` and `<endpoint>` format.
    *   **Collects authentication arguments and passes them to the `Manager.Add` function for storage and initial authentication.**
    *   If sensitive credentials are provided on the CLI, `dropzone` should prompt the user for confirmation and offer to encrypt them in the config file.
    *   Persists the control plane configuration via `internal/config`.
*   **`dropzone add repo <github-username> [--token <token>]`** (Discovery):
    *   **Simplified GitHub Discovery:** If a single argument is provided and it looks like a username (not a URL/URI), `dropzone` invokes `controlplane.Manager.AddFromGitHubUser`.
    *   It verifies the existence of `https://github.com/<username>/care-package`.
    *   If successful, it registers a control plane named `<username>` of type `github` pointing to the `care-package` repository.
*   **`dropzone login <repo-name> [--username <user>] [--password <pass>] [--token <token>]`:** (Alternative/explicit authentication)
    *   Allows users to explicitly log in or update credentials for an *already added* control plane.
    *   Invokes `controlplane.Manager.Get(repo-name).Authenticate()` with the provided credentials.
    *   Updates the `AuthOptions` in `internal/config` for that control plane.
*   **`dropzone update`:**
    *   Invokes `controlplane.Manager.UpdateAll()`.
    *   Displays progress to the user (e.g., "Updating OCI registry 'my-packages'...", "Found 5 new packages...").
    *   **Handles authentication challenges during update calls, potentially by prompting the user if credentials are missing/invalid.**
*   **`dropzone list` (Enhanced):**
    *   Will now leverage `localstore.GetAllAvailablePackages()` to display both locally installed and remotely available packages, indicating their source control plane.
    *   `dropzone list --repo <name>` to filter by a specific control plane.
    *   `dropzone tags <package-name> [--repo <name>]` to list all available versions for a specific package. This would call `controlplane.GetPackageTags`, which will utilize authentication.

## 5. Technical Details

*   **OCI Registry Interaction:** Will use a Go library for OCI registry clients (e.g., `google/go-containerregistry` or `oras-project/oras-go`) for pushing/pulling images and manifests. These libraries typically handle Docker's credential helpers or can be configured with explicit authentication. Direct execution of `skopeo` via `os/exec` is also an option, as it supports authentication.
*   **GitHub/S3 Interaction:** Dedicated Go SDKs (`github.com/google/go-github`, `github.com/aws/aws-sdk-go`) will be used, which have built-in support for authentication.
*   **Data Serialization:** JSON or YAML for storing control plane index data in `localstore` and configuration in `internal/config`.
*   **Encryption for Credentials:** For MVP, consider a simple, symmetric encryption scheme with a user-provided passphrase or a system-level keyring integration (e.g., `github.com/zalando/go-keyring` for OS-native keyring access).
*   **Error Handling:** Robust error reporting for network issues, invalid credentials, or malformed responses from control planes.
*   **AuthOptions Struct:** A common struct to pass authentication details across components:
    ```go
    type AuthOptions struct {
        Username   string
        Password   string // Should be handled securely (e.g., encrypted in config)
        Token      string // Should be handled securely
        AccessKey  string // For S3, etc. Should be handled securely
        SecretKey  string // For S3, etc. Should be handled securely
        // Add fields for GPG key IDs, etc. for signing/verification if needed
    }
    ```

## 6. Testing

### 6.1. Unit Tests

*   **`internal/controlplane/controlplane.go`:**
    *   Test `Manager.Add`, `Remove`, `List`, `Get` for correct management of registered control planes, including the storage and retrieval of `AuthOptions`.
    *   Test the control plane factory method to ensure correct implementations are instantiated based on type and auth options.
    *   Test `Manager.UpdateAll()` by mocking individual `ControlPlane` implementations to ensure local store is updated.
    *   Test `ControlPlane` interface methods against dummy data for generic behavior.
*   **`internal/controlplane/oci/oci.go`:**
    *   Mock HTTP requests to an OCI registry API to simulate listing repositories, fetching tags, and pulling manifests, *both for public and authenticated private registries*.
    *   Verify correct configuration of OCI client with provided credentials.
    *   Test `Authenticate` method for successful login and failed login scenarios.
    *   Test error handling for authentication failures, network issues, and invalid registry responses.
*   **`internal/controlplane/github/github.go` (MVP Placeholder):**
    *   Mock GitHub API calls to test `Authenticate` with valid/invalid PATs.
*   **`internal/controlplane/s3/s3.go` (MVP Placeholder):**
    *   Mock S3 API calls to test `Authenticate` with valid/invalid AWS credentials.
*   **`internal/download/download.go`:**
    *   Test `DownloadFile` against a mock HTTP server to simulate various scenarios: successful download, 404, network error, partial download, corrupted data, *and successful/failed downloads with basic HTTP authentication*.
*   **`internal/config/config.go` (Auth Integration):**
    *   Test `Load` and `Save` methods with `Config` structs containing `AuthOptions`.
    *   **Test encryption/decryption of sensitive fields within `AuthOptions`** (mocking the encryption key/passphrase).
    *   Verify default configuration values are applied correctly.

### 6.2. Integration Tests

*   **`dropzone add repo`:**
    *   Run `dropzone add repo my-oci oci://my-public-registry.com/my-dropzone-path`. Verify it's added.
    *   Run `dropzone add repo my-private-oci oci://my-private-registry.com/path --username user --password pass`. Verify it's added and authentication details are (mocked) encrypted in config.
*   **`dropzone login`:**
    *   Run `dropzone login my-private-oci --token new-token`. Verify credentials are updated and encrypted.
    *   Run `dropzone login non-existent-repo`, verify graceful error.
*   **`dropzone update`:**
    *   With a mock or dedicated test OCI registry (public and private with known credentials), run `dropzone update`.
    *   Verify that the local store (`~/.dropzone/index/<repo-name>.json`) is populated with package names and tags from *both* public and private registries.
    *   Test `dropzone update` with incorrect credentials for a private repo, verifying authentication failure and informative error message.
*   **`dropzone list` / `dropzone tags`:**
    *   Verify these commands correctly display packages from both public and privately authenticated repositories.

## 7. Open Questions / Future Considerations

*   **Signing Key Management:** How to securely manage signing keys (e.g., GPG keyrings, hardware tokens) for local package building and attestation? For MVP, rely on user's existing GPG setup. Future could include `dropzone`-specific key management and integration with Cosign.
*   **Credential Helper Integration:** Expand beyond basic username/password/token to integrate with existing OS/Docker credential helpers for a smoother UX, especially for OCI registries.
*   **Token Refresh:** How to handle short-lived tokens or OAuth flows for control planes that require it.
*   **More Granular Permissions:** Implement fine-grained permission models for credentials, e.g., read-only tokens for `dropzone update`.
*   **Error Reporting for Auth:** Improve clarity of authentication errors (e.g., "invalid credentials," "rate limit exceeded," "network unreachable").
*   **Proxy Support:** How to configure `dropzone` to work behind corporate proxies for reaching control planes.