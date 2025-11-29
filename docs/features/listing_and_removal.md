# Feature: Listing and Removal

## 1. Overview

This document describes the design and implementation for managing installed and available care packages within `dropzone`. This includes the `dropzone list` command for displaying package information and the `dropzone remove` command for **uninstalling specific versions of care packages** from the host system and local storage. These features complete the basic lifecycle management of care packages in the MVP, emphasizing user control over installed versions.

## 2. Goals

*   Provide users with a clear overview of locally installed and remotely available care packages.
*   Allow users to inspect versions and originating control planes for packages.
*   **Enable users to cleanly remove specific installed care package versions**, reverting host system changes pertaining only to that version.
*   Ensure efficient data retrieval and consistent presentation for package lists.
*   Provide robust cleanup mechanisms during package removal.

## 3. Components

### 3.1. `internal/packagehandler/packagehandler.go` (Listing and Removal Logic)

*   **Responsibility:** Orchestrates the listing of packages and the version-specific removal process.
*   **Functionality - `ListPackages()`:**
    *   Retrieves a list of all locally installed care packages (names, versions, metadata) from `internal/localstore`.
    *   Retrieves a list of all available care packages and their tags from all configured control planes by querying `controlplane.Manager` (which internally uses `localstore`'s cached index).
    *   Aggregates and dedupes this information, indicating which packages are installed, which are available, and their respective versions/tags and source control planes.
    *   Formats the combined list for user-friendly output (e.g., tabular format).
    *   Supports filtering by installed status, control plane origin, or package name (via CLI flags).
*   **Functionality - `RemovePackage(packageName, targetVersion string)`:**
    *   Receives the target package name and an *optional* `targetVersion` string.
    *   Checks `internal/localstore` to retrieve all installed versions of `packageName`.
    *   **Version Resolution:**
        *   If `targetVersion` is empty and multiple versions are installed, `dropzone` will *interactively prompt the user* to select which version(s) to remove or to confirm removal of all.
        *   If `targetVersion` is empty and only one version is installed, `dropzone` will remove that single version after confirmation.
        *   If `targetVersion` is specified, `dropzone` will only attempt to remove that specific version.
    *   For each resolved package version to be removed:
        *   Calls `hostintegration.UnlinkPackageBinaries(packageName, packageVersion)` to remove all symbolic links associated with *that specific package version* from `~/.dropzone/bin`.
        *   Deletes the package's extracted contents from `~/.dropzone/packages/<name>/<version>/` using `localstore.RemovePackageFiles()` for *that specific version*.
        *   Deletes the package's metadata from `localstore.RemovePackageMetadata()` for *that specific version*.
    *   Provides feedback to the user on the success or failure of the removal.

### 3.2. `internal/localstore/localstore.go` (Package Data Management)

*   **Responsibility:** Provides persistent storage and retrieval for installed package data and metadata, and cached control plane indexes.
*   **Functionality:**
    *   **`GetAllInstalledPackages() ([]PackageMetadata, error)`:**
        *   Scans `~/.dropzone/packages/` and retrieves metadata for all installed care packages.
    *   **`GetInstalledPackageVersions(packageName string) ([]PackageMetadata, error)`:**
        *   Retrieves metadata for all installed versions of a specific package.
    *   **`GetAllAvailablePackagesFromIndexes() ([]PackageMetadata, error)`:**
        *   Aggregates package metadata from all cached control plane indexes (e.g., `~/.dropzone/index/*.json`).
    *   **`RemovePackageFiles(packageName, packageVersion string) error`:**
        *   Deletes the directory containing the extracted contents of a specific care package version.
    *   **`RemovePackageMetadata(packageName, packageVersion string) error`:**
        *   Deletes the metadata file associated with a specific care package version.

### 3.3. `internal/controlplane/controlplane.go` (Control Plane Integration)

*   **Responsibility:** Manages the interaction with registered control planes to provide data on available packages.
*   **Functionality (via Manager struct):**
    *   `GetControlPlaneIndex(controlPlaneName string) (map[string][]PackageMetadata, error)`: Used by `localstore` to access cached remote package data.
    *   (Potentially direct call for `dropzone tags <package-name>`): `GetPackageTags(packageName string) ([]string, error)`: Fetches tags from a specific control plane, using authentication.

### 3.4. `internal/hostintegration/hostintegration.go` (Host Cleanup)

*   **Responsibility:** Manages the removal of host-level integrations.
*   **Functionality:**
    *   **`UnlinkPackageBinaries(packageName, packageVersion string) error`:**
        *   As described in `host_integration_and_package_definition.md`, removes symlinks from `~/.dropzone/bin` that belong to the *specific `packageName` and `packageVersion`*.

## 4. `dropzone` CLI Integration

*   **`dropzone list [--installed|--available|--repo <name>|--package <name>]`:**
    *   Invokes `packagehandler.ListPackages()`.
    *   Prints a formatted table of package names, versions, installation status, and source control plane.
    *   The `--installed` flag would filter to only show locally installed packages.
    *   The `--available` flag would filter to only show remotely available packages.
    *   The `--repo <name>` flag would filter packages by a specific control plane.
    *   The `--package <name>` flag would show all versions of a specific package across all relevant sources.
*   **`dropzone tags <package-name> [--repo <name>]`:**
    *   Invokes `controlplane.Manager.Get(repoName).GetPackageTags(packageName)` (or similar logic via `packagehandler`).
    *   Displays all available tags/versions for the specified package from the specified (or all) control plane(s).
*   **`dropzone remove <package-name>[:<tag>]`:**
    *   Invokes `packagehandler.RemovePackage(packageName, targetVersion)`.
    *   **If no tag is specified and multiple versions are installed, it will interactively prompt the user for selection.**
    *   Prompts the user for final confirmation before proceeding with deletion.
    *   Provides clear messages about which files and links are being removed.
    *   Handles cases where the specified package/version is not installed.

## 5. Technical Details

*   **Data Aggregation:** `packagehandler.ListPackages` will need to merge data from `localstore` (for installed packages) and `localstore`'s cached control plane indexes (for available packages).
*   **User Interface:** Use `fmt` or a text-based table rendering library (e.g., `github.com/olekukonko/tablewriter`) for `dropzone list` output.
*   **Confirmation/Interaction:** Implement a simple `yes/no` prompt for removal, and potentially a numbered selection for multiple versions, using `bufio` or a simple interactive library for Go.
*   **Error Handling:** Ensure graceful handling of errors during file operations (e.g., permissions, non-existent files) and provide informative messages.

## 6. Testing

### 6.1. Unit Tests

*   **`internal/packagehandler/packagehandler.go` (Listing Logic):**
    *   Test `ListPackages` by mocking `localstore` and `controlplane.Manager` to return various combinations of installed/available packages.
    *   Verify correct aggregation, deduplication, and sorting of package data.
    *   Test filtering logic (if implemented in the handler).
*   **`internal/packagehandler/packagehandler.go` (Removal Logic):**
    *   Test `RemovePackage` orchestration, mocking `localstore`, `hostintegration`.
    *   Test `RemovePackage` with a specific version.
    *   Test `RemovePackage` without a specific version when multiple are installed, mocking user input for interactive selection.
    *   Test `RemovePackage` without a specific version when only one is installed.
    *   Test error handling if `localstore` cannot find the package, or if `hostintegration`/`localstore` cleanup operations fail.
    *   Ensure all cleanup steps are attempted even if one fails.
*   **`internal/localstore/localstore.go`:**
    *   Test `GetAllInstalledPackages`, `GetInstalledPackageVersions`, and `GetAllAvailablePackagesFromIndexes` with various dummy package data in the local storage.
    *   Test `RemovePackageFiles` and `RemovePackageMetadata` for correct file deletion and error handling for non-existent files.

### 6.2. Integration Tests

*   **End-to-end `dropzone list`:**
    *   In a clean `dropzone` environment, run `dropzone list` and verify it shows no packages.
    *   Install a local care package, then run `dropzone list` and verify it appears as "Installed".
    *   Install *another version* of the same care package. Run `dropzone list` and verify both versions are shown as installed.
    *   Add a control plane, run `dropzone update`, then `dropzone list` and verify new "Available" packages are shown.
    *   Test various filter flags (`--installed`, `--available`, `--repo`, `--package`).
*   **End-to-end `dropzone remove` (Version-Specific):**
    *   Install `myapp:1.0.0` and `myapp:2.0.0`.
    *   Verify both versions are installed and their binaries are linked.
    *   Run `dropzone remove myapp:1.0.0` and confirm removal.
    *   Verify that only `myapp:1.0.0`'s files, metadata, and symlinks are removed. `myapp:2.0.0` should remain fully functional.
    *   Run `dropzone list` to confirm `myapp:1.0.0` is no longer shown as installed, but `myapp:2.0.0` is.
    *   Run `dropzone remove myapp` (without a tag) when `myapp:2.0.0` is the only version left. Verify confirmation prompt and then full removal.
    *   Run `dropzone remove myapp` (without a tag) when multiple versions are installed. Mock interactive input to select one or all for removal, and verify.
    *   Test `dropzone remove` on a non-existent package or version to ensure it handles the scenario gracefully.

## 7. Open Questions / Future Considerations

*   **Forced Removal:** A `--force` flag for `remove` to skip confirmation and potentially ignore some cleanup errors.
*   **Garbage Collection:** Automatically detect and remove orphaned files or unlinked packages from `~/.dropzone` (e.g., if a manual symlink removal breaks state).
*   **Query Language:** For complex `list` operations, a more powerful query language beyond simple flags could be considered (e.g., `dropzone find 'name=nginx AND status=installed'`).
*   **Package Status Indicators:** Richer output for `dropzone list`, e.g., showing if a package has an update available, or if it's currently linked/active.
*   **`dropzone purge`:** A command to completely remove `dropzone` and all its managed data from the system.