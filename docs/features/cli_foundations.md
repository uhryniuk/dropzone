# Feature: CLI Foundations

## 1. Overview

Foundational elements of the `dz` (dropzone) CLI: command tree, argument parsing, application context, and the config + local store it sits on top of. Everything here pre-dates the registry / cosign / shim work; it's what those features plug into.

## 2. Goals

*   Consistent, scannable command tree across registry management, discovery, and lifecycle operations.
*   One `App` context that wires the Registry Manager, Sigstore Verifier, Shim Builder, Host Integrator, and Local Store together.
*   Zero-config first run: create `~/.dropzone/` and pre-seed the default `chainguard` registry.
*   Runs on Linux and macOS (both amd64 and arm64).
*   Distributable as a single self-contained binary (`go install` or a release artifact). No runtime dependencies: Sigstore verification, OCI registry access, and rootfs unpack are all in-process via embedded libraries. Only a POSIX `/bin/sh` is needed to execute generated wrapper scripts, which both Linux and macOS provide by default.

## 3. Components

### 3.1. `cmd/dropzone/main.go`

Entry point. Constructs the `App`, calls `SetupCommands()`, runs `cobra.Execute()`, exits with a non-zero code on error.

### 3.2. `internal/app/`

*   `App` struct holds: `Config`, `LocalStore`, `RegistryManager`, `CosignVerifier`, `ShimBuilder`, `HostIntegrator`. Records detected host OS (`runtime.GOOS`) and arch (`runtime.GOARCH`), used by the Registry Manager for manifest-list platform selection and by the Shim Builder for entrypoint format validation.
*   `Initialize()` is idempotent: create `~/.dropzone/` subdirectories if missing, load config, seed the default `chainguard` registry entry if the config file doesn't exist yet, set up `~/.dropzone/bin` on `PATH` if needed (shell-aware, see host_integration.md). Refuses to run on any `runtime.GOOS` that isn't `linux` or `darwin`.

### 3.3. `internal/app/commands.go`

Command tree (Cobra):

```
dz
├── add
│   └── registry <name> <url> [--template github|gitlab] [--identity-issuer <url>] [--identity-regex <regex>]
├── list
│   ├── (no subcommand) → installed packages
│   └── registries
├── remove
│   ├── <package>
│   └── registry <name>
├── search [<term>] [--registry <name>]
├── tags <image> [--registry <name>]
├── install <ref> [--allow-unsigned]
├── update [<package>]
└── version
```

Notes:

*   `dz list` with no subcommand shows installed packages. `dz list registries` shows the registry config.
*   `dz remove` routes on its first positional: `dz remove registry <name>` vs `dz remove <package>`.
*   `--allow-unsigned` is the only install-time override of the signature policy. Name chosen over `--untrusted`/`--insecure` because it describes what's being permitted (an unsigned source), not a property of the user.

### 3.4. `internal/config/config.go`

Config schema (see DESIGN.md §4.6). No credential fields, auth is delegated to Docker credential helpers via `go-containerregistry`.

```go
type Config struct {
    DefaultRegistry string           `yaml:"default_registry"`
    Registries      []RegistryConfig `yaml:"registries"`
}

type RegistryConfig struct {
    Name         string        `yaml:"name"`
    URL          string        `yaml:"url"`
    CosignPolicy *CosignPolicy `yaml:"cosign_policy,omitempty"`
}

type CosignPolicy struct {
    Issuer        string `yaml:"issuer"`
    IdentityRegex string `yaml:"identity_regex"`
}
```

If `CosignPolicy` is nil, every install from that registry requires `--allow-unsigned`.

### 3.5. `internal/localstore/localstore.go`

On-disk layout per DESIGN.md §4.5. Responsibilities:

*   `Init()`, create subdirectories, seed config if missing.
*   `GetPackagePath(name, version)`, path to an installed package directory.
*   `StorePackageMetadata(metadata)` / `GetPackageMetadata(name, version)`, per-install metadata.
*   `GetAllInstalled()`, for `dz list`.
*   `CacheCatalog(registry, data)` / `GetCachedCatalog(registry)`, catalog + tags cache with TTL.

### 3.6. `internal/util/util.go`

Unchanged from the current code. Provides `GetHomeDir`, `FileExists`, `CreateDirIfNotExist`, `CopyFile`, `RemovePath`, and the logging helpers.

## 4. Technical details

*   **Language:** Go 1.23+.
*   **CLI framework:** `cobra` (already in use).
*   **Config format:** YAML.
*   **Logging:** `stderr` for human-readable output; no log file. Verbose mode via `-v` prints debug.

## 5. Testing

### 5.1. Unit tests

*   Config load/save round-trip, including the pre-seeded default registry on first run.
*   Local store directory creation, metadata store/fetch, catalog cache TTL behavior.
*   Command argument parsing for all subcommands, particularly the `remove` routing between `remove registry <name>` and `remove <package>`.

### 5.2. Integration tests

*   Fresh-install flow on Linux: run `dz version` in an empty `$HOME`, verify the `~/.dropzone/` tree exists and config has the default `chainguard` entry.
*   Same on macOS (CI matrix covering `darwin/arm64` + `darwin/amd64`).
*   `dz` on a non-Linux / non-macOS GOOS fails at startup with a clear message (`"dropzone supports Linux and macOS only"`).

## 6. Open questions

*   Shell completion (bash / zsh / fish), nice-to-have, not MVP.
*   A `dz doctor` command that checks `~/.dropzone/bin` on `PATH`, package dir consistency (`current` symlink points at a real digest dir, wrappers exist for every installed package), and config health, plausibly MVP, small scope.
