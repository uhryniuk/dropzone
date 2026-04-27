# Feature: Listing and Removal

## 1. Overview

`dz list` shows installed packages with their source registry, installed tag, and resolved digest. `dz remove <name>` unshims a package and deletes its files.

Listing *available* packages moved to `dz search` and `dz tags`, see `registry_management.md`. This doc covers installed-only listing and removal.

## 2. Goals

*   Give users a clear view of what dropzone has installed and where each package came from.
*   Surface signature verification status, an unsigned install (`--allow-unsigned`) is visually distinct.
*   Remove a package cleanly: unshim binaries, delete package files, delete metadata, in that order so partial failures don't leave dangling symlinks with no package behind them.

## 3. Components

### 3.1. `internal/packagehandler/packagehandler.go`

*   **`ListInstalled() ([]InstalledPackage, error)`**, reads all metadata from `~/.dropzone/packages/*/*/metadata.json` and returns a slice with: name, version (tag), digest, source registry, install timestamp, signature-verified boolean.
*   **`RemovePackage(name string) error`**:
    1.  Look up the installed package in the local store. Error if not installed.
    2.  `hostintegration.UnlinkPackageBinaries(name)`, remove the symlink from `~/.dropzone/bin/`.
    3.  `localstore.RemovePackageFiles(name)`, delete `~/.dropzone/packages/<name>/`.
    4.  On partial failure during step 2, do not proceed to step 3; report the inconsistent state clearly.

MVP installs one version per package (re-install replaces), so removal is not version-qualified. A future multi-version install would need `RemovePackage(name, version)`.

### 3.2. `internal/localstore/localstore.go`

*   `GetAllInstalled() ([]PackageMetadata, error)`, scans the packages directory.
*   `RemovePackageFiles(name string) error`, deletes the package directory.

### 3.3. `internal/hostintegration/hostintegration.go`

*   `UnlinkPackageBinaries(name string) error`, removes the `~/.dropzone/bin/<name>` symlink if it points into the package's directory. Does nothing (with a warning) if the symlink points elsewhere, something else has taken over the name.

## 4. CLI integration

### `dz list`

Tabular output. Columns: NAME, TAG, DIGEST (truncated), REGISTRY, SIGNED, INSTALLED AT. An unsigned install shows `signed: no` in a distinct color/marker.

### `dz remove <name>`

Prompts for confirmation unless `--yes` is passed. Prints each unshim / delete step so the user sees progress. On failure, exits non-zero with the partial state described.

## 5. Technical details

*   Tabular output via `text/tabwriter` (stdlib) or `olekukonko/tablewriter`, either is fine.
*   Digest truncation: first 12 characters, matching Docker's convention.

## 6. Testing

### 6.1. Unit

*   `ListInstalled` on an empty store → empty slice.
*   `ListInstalled` with mixed signed / unsigned metadata → signed field surfaced correctly.
*   `RemovePackage` happy path + each partial-failure path (unlink fails, file delete fails).

### 6.2. Integration

*   Install a package, `dz list` shows it, `dz remove` cleans it up, `dz list` shows it gone and `~/.dropzone/bin/` contains no leftover symlink.
*   Install two packages, remove one, verify the other is untouched.

## 7. Open questions

*   `dz purge`, wipe `~/.dropzone/` entirely. Useful, likely small scope, plausibly MVP.
*   `dz list --json` for scripting.
