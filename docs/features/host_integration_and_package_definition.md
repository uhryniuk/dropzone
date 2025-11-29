# Feature: Host Integration and Package Definition Interface

## 1. Overview

This document defines two critical aspects of `dropzone` for the MVP:
1.  **Host Integration:** How "care packages" are seamlessly made available and usable on the host system, *including robust detection and resolution of binary name conflicts*. For the MVP, this primarily involves managing symbolic links to the host's `PATH`.
2.  **Package Definition Interface:** The standardized directory structure within a care package container that `dropzone` expects for successful integration. This provides a clear contract for package creators.

## 2. Goals

*   Ensure applications within care packages are easily executable from the host's shell.
*   Avoid polluting the host filesystem with direct package installations.
*   **Prevent unpredictable behavior due to binary name conflicts on the host system.**
*   Provide a clear and simple standard for package authors using Dockerfiles.
*   Minimize conflicts with existing host-installed software.

## 3. Components

### 3.1. `internal/hostintegration/hostintegration.go`

*   **Responsibility:** Manages the integration of care package binaries and potentially other assets into the host's operating environment, with a focus on `PATH` management and conflict resolution.
*   **Functionality:**
    *   **`SetupDropzoneBinPath()`:**
        *   Creates the `~/.dropzone/bin` directory if it doesn't exist.
        *   Checks if `~/.dropzone/bin` is already in the user's `PATH`.
        *   If not, adds `export PATH="$HOME/.dropzone/bin:$PATH"` (or equivalent for other shells like `fish`, `csh`) to the user's shell configuration file (`~/.bashrc`, `~/.zshrc`, etc.) and prompts the user to source their shell configuration.
        *   Handles idempotent operations (i.e., doesn't add duplicate entries).
    *   **`LinkPackageBinaries(packageName, packageVersion, packageInstallPath string) ([]string, error)`:**
        *   Takes the name, version, and full path to an extracted care package (e.g., `~/.dropzone/packages/myapp/v1.0.0`).
        *   Identifies all executables within the `<package_install_path>/bin/` directory.
        *   **Conflict Detection:** For each executable, it checks if a binary with the same name already exists in `~/.dropzone/bin/` (from another `dropzone` package) or elsewhere in the user's `PATH` (from system/user installations).
        *   **Conflict Resolution:**
            *   **Default Behavior (MVP):** If a conflict is detected with an *already `dropzone`-managed binary*, `dropzone` will warn the user. The newly installed binary will overwrite the existing symlink in `~/.dropzone/bin/`, effectively making the new package's binary take precedence.
            *   If a conflict is detected with a *system/user-installed binary* (outside `~/.dropzone/bin`), `dropzone` will warn the user and *not* create a symlink for that specific binary, to avoid overriding system tools unintentionally. It will suggest using a fully qualified path or a `dropzone`-prefixed alternative (e.g., `dropzone-myapp-tool`).
            *   (Future refinement could include interactive prompts or explicit `--force` flags.)
        *   Creates symbolic links from each executable (that passed conflict resolution) to `~/.dropzone/bin/`.
        *   Returns a list of linked binary names.
    *   **`UnlinkPackageBinaries(packageName, packageVersion string)`:**
        *   Removes the symbolic links created by `LinkPackageBinaries` from `~/.dropzone/bin/` that belong specifically to the given `packageName` and `packageVersion`.
    *   **`VerifyRuntime(runtime string)`:**
        *   Checks for the presence of the specified container runtime (Docker or Podman) on the host system.
        *   Ensures the runtime is accessible and functional.

### 3.2. `docs/care_package_format.md`

*   **Responsibility:** Formal documentation of the expected internal structure of a care package container.
*   **Content:**
    *   **Root Installation Directory:** Specifies that all package artifacts must be copied to `/dropzone/install` within the final stage of the Dockerfile.
    *   **Standard Subdirectories:**
        *   `/dropzone/install/bin/`: For all executable binaries and scripts that should be made available in the host's PATH.
        *   `/dropzone/install/lib/`: For shared libraries required by the binaries.
        *   `/dropzone/install/share/`: For architecture-independent data, documentation, man pages, etc.
        *   `/dropzone/install/etc/`: (Optional) For configuration files that might need to be symlinked or copied to specific host locations.
        *   `/dropzone/install/var/`: (Optional) For variable data like logs or temporary files.
    *   **Package Name Label:** Mandates the use of `LABEL dropzone.package="<package-name>"` in the final Dockerfile stage to clearly identify the package.
    *   **Example Dockerfile:** A complete, well-commented example demonstrating a multi-stage build producing a compliant care package.

## 4. Technical Details

*   **Symbolic Linking:** Standard `os` package functions for creating and removing symlinks.
*   **Shell Configuration:** Direct file manipulation of `~/.bashrc`, `~/.zshrc`, etc. Detection of the active shell for correct file targeting. Requires careful parsing to avoid duplicate entries and handle various shell syntaxes.
*   **Container Runtime Check:** Executing `docker --version` or `podman --version` and checking exit codes.
*   **PATH Management:** Careful handling to ensure idempotence and avoid breaking user environments. `hostintegration` will maintain an internal registry of linked binaries to manage conflicts effectively.

## 5. Testing

### 5.1. Unit Tests

*   **`internal/hostintegration/hostintegration.go`:**
    *   Test `SetupDropzoneBinPath` for:
        *   Correct directory creation.
        *   Detection of `PATH` presence.
        *   Correct modification of mock shell config files (e.g., appending the `PATH` entry only once).
        *   Handling of different shell types (bash, zsh, fish - mocked).
    *   Test `LinkPackageBinaries` for:
        *   Correct symlink creation from package `bin` to `~/.dropzone/bin`.
        *   **Conflict Detection:** Test scenarios where a binary name exists in `~/.dropzone/bin` from another package, or exists in the system `PATH`.
        *   **Conflict Resolution:**
            *   Verify a newly linked binary overwrites an existing `dropzone`-managed symlink.
            *   Verify a newly linked binary *does not* overwrite a system `PATH` binary, and a warning is issued.
        *   Error handling for non-existent source directories or permission issues.
        *   Handling of empty `bin` directories.
    *   Test `UnlinkPackageBinaries` for:
        *   Correct removal of symlinks belonging to a specific package version.
        *   Handling of non-existent symlinks gracefully.
    *   Test `VerifyRuntime` for:
        *   Successfully detecting Docker/Podman when present (mock `exec.Command` output).
        *   Failing correctly when runtime is absent or dysfunctional.

### 5.2. Integration Tests

*   **End-to-end `PATH` setup:**
    *   Run `dropzone` in a clean temporary user environment.
    *   Verify that `~/.dropzone/bin` is created.
    *   Verify that `~/.dropzone/bin` is added to the temporary shell's `PATH` variable after sourcing config.
*   **Symlink integration with conflict scenarios:**
    *   Simulate an extracted care package (`app1:1.0.0`) with `binaryA`. Link it. Verify `binaryA` is accessible.
    *   Simulate a second extracted care package (`app2:1.0.0`) with `binaryA` and `binaryB`.
    *   Attempt to link `app2:1.0.0`.
    *   Verify `app2`'s `binaryA` now takes precedence in `~/.dropzone/bin`.
    *   Simulate a system-wide `tool` binary (e.g., `/usr/local/bin/tool`).
    *   Simulate `dropzone` package `app3:1.0.0` also providing `tool`.
    *   Attempt to link `app3:1.0.0`. Verify `dropzone` warns and *does not* link `app3`'s `tool` directly to `~/.dropzone/bin`.
    *   Invoke `dropzone` to unlink `app2:1.0.0`, and verify only `app2`'s symlinks are removed, `app1`'s `binaryA` (if still present) remains.
*   **Runtime Verification:**
    *   Test with Docker/Podman installed to ensure `VerifyRuntime` passes.
    *   Test with Docker/Podman uninstalled or non-functional to ensure `VerifyRuntime` fails gracefully.

## 6. Open Questions / Future Considerations

*   **Advanced Conflict Resolution:**
    *   Allow users to explicitly choose to overwrite system binaries (with a strong warning).
    *   Automatically generate `dropzone-` prefixed alternatives (e.g., `dropzone-nginx`) for conflicting binaries.
    *   Implement version switching for binaries (e.g., `dropzone switch myapp@v2`).
*   **More Complex Host Integration:** How to handle non-binary files (e.g., desktop entries, man pages, service files, configuration files)?
    *   MVP: Focus on `bin` and direct symlinking. Other files are just stored locally.
    *   Future: Support for `share` directories, templating or overlaying of `etc` files, FUSE-based mounting.
*   **Interactive Shell Updates:** How to best prompt the user to source their shell configuration without being overly intrusive?
*   **Windows/macOS Support:** The `PATH` modification and symlinking logic will differ significantly for other operating systems. This MVP focuses on Linux.