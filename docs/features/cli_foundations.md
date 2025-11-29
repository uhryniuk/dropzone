# Feature: CLI Foundations

## 1. Overview

This document outlines the design and implementation plan for the foundational elements of the `dropzone` Command Line Interface (CLI). This includes setting up the basic command structure, argument parsing, application context initialization, and core utility functions. This stage is crucial for establishing a stable and extensible base for all subsequent `dropzone` features.

## 2. Goals

*   Provide a robust and user-friendly CLI experience.
*   Establish a clear and consistent command structure.
*   Ensure proper initialization and lifecycle management for the `dropzone` application.
*   Provide common utility functions to reduce code duplication and improve maintainability.
*   The CLI tool itself should be easily installable via `go install` or downloadable as a pre-built binary.

## 3. Components

### 3.1. `cmd/dropzone/main.go`

*   **Responsibility:** The entry point of the `dropzone` application.
*   **Functionality:**
    *   Initialize `cobra` (or similar CLI framework) root command.
    *   Execute the CLI.
    *   Handle top-level errors.

### 3.2. `internal/app/dropzone.go`

*   **Responsibility:** Manages the core application context and lifecycle.
*   **Functionality:**
    *   Define an `App` struct holding references to all core services (e.g., `ConfigManager`, `LocalStore`, `Logger`).
    *   Provide an `Initialize` method to set up the `App` context, including loading configuration and ensuring local storage directories exist.
    *   Provide a `Shutdown` method for graceful cleanup.

### 3.3. `internal/app/commands.go`

*   **Responsibility:** Defines and registers all `dropzone` CLI commands and subcommands.
*   **Functionality:**
    *   Define the root command.
    *   Define placeholder commands for `add`, `build`, `install`, `list`, `remove`, `update`, `version`.
    *   Implement basic flag parsing and validation for each command.
    *   Attach command handlers that leverage the `App` context.

### 3.4. `internal/config/config.go`

*   **Responsibility:** Manages `dropzone`'s persistent configuration.
*   **Functionality:**
    *   Define a `Config` struct (e.g., `LocalStorePath string`, `ControlPlanes []ControlPlaneConfig`, `ActiveContainerRuntime string`).
    *   Load configuration from `~/.dropzone/config.yaml`.
    *   Save configuration to `~/.dropzone/config.yaml`.
    *   Provide default configuration values if no file exists.
    *   Handle concurrent access to configuration data.

### 3.5. `internal/localstore/localstore.go`

*   **Responsibility:** Manages the `~/.dropzone` directory structure and local data persistence.
*   **Functionality:**
    *   Define `LocalStore` struct with methods for interacting with the filesystem within `~/.dropzone`.
    *   `Init()`: Create base directories (`~/.dropzone`, `~/.dropzone/bin`, `~/.dropzone/packages`, `~/.dropzone/config`).
    *   `GetPackagePath(packageName, version string)`: Returns the path to an installed care package.
    *   `GetConfigPath()`: Returns the path to the configuration file.
    *   `StorePackageMetadata(metadata)`: Persists package metadata.
    *   `GetPackageMetadata(packageName, version string)`: Retrieves package metadata.

### 3.6. `internal/util/util.go`

*   **Responsibility:** Provides common, reusable utility functions.
*   **Functionality:**
    *   `CreateDirIfNotExist(path string)`
    *   `CopyFile(src, dest string)`
    *   `RemovePath(path string)`
    *   `FileExists(path string)`
    *   `GetHomeDir()`: Returns the user's home directory.
    *   Basic logging interface (`LogInfo`, `LogError`, `LogDebug`).

## 4. Technical Details

*   **Language:** Go (targeting 1.23+).
*   **CLI Framework:** `cobra` or `urfave/cli` will be evaluated for command parsing and structure. `cobra` is preferred for its robust features and widespread use in the Go ecosystem.
*   **Configuration Format:** YAML (`~/.dropzone/config.yaml`).
*   **File System Interactions:** Standard Go library `os` and `io/fs` packages.
*   **Error Handling:** Use Go's standard error wrapping.

## 5. Testing

### 5.1. Unit Tests

*   **`internal/app/commands.go`:**
    *   Verify correct parsing of CLI flags and arguments for each command.
    *   Test command validation logic (e.g., required arguments).
*   **`internal/config/config.go`:**
    *   Test `Load` and `Save` methods for correct serialization/deserialization.
    *   Verify default configuration values are applied correctly.
    *   Test error handling for malformed configuration files.
*   **`internal/localstore/localstore.go`:**
    *   Test `Init()` for correct directory creation.
    *   Test `StorePackageMetadata` and `GetPackageMetadata` for data integrity.
    *   Verify path generation methods.
    *   Mock filesystem errors to test robust error handling.
*   **`internal/util/util.go`:**
    *   Test all utility functions (`CreateDirIfNotExist`, `CopyFile`, `RemovePath`, `FileExists`, `GetHomeDir`) against various scenarios (e.g., existing/non-existing paths, permissions issues).

### 5.2. Integration Tests

*   **CLI Command Execution:**
    *   Run `dropzone` with various command combinations (e.g., `dropzone version`, `dropzone help`) and verify expected output and exit codes.
    *   Test the overall initialization flow from `main.go` to `App.Initialize`.
    *   Create a temporary `~/.dropzone` environment for testing that configuration and local storage are initialized correctly on first run.

## 6. Open Questions / Future Considerations

*   Should `dropzone` manage its own `log` file or rely solely on `stderr`/`stdout` for the MVP? For MVP, `stderr`/`stdout` is sufficient, with an option for verbose output.
*   Detailed error reporting: how much context should be provided to the user for different types of failures? Start with concise messages and improve over time.
*   Cross-platform compatibility: While Go handles this well, specific filesystem interactions (e.g., `PATH` manipulation) might need OS-specific adjustments (beyond MVP scope for now).