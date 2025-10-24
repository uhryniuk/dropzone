# Care Package Format

This document defines the technical specification for a "care package" in `dropzone`. Package authors must adhere to this format to ensure their packages can be correctly built, installed, and integrated into the host system by the `dropzone` manager.

## 1. Overview

A care package is essentially an OCI container image. However, to function as a distributable package within `dropzone`, the final stage of the container build must organize the package artifacts into a specific directory structure and provide specific metadata via labels.

## 2. Directory Structure

All files that are intended to be installed onto the host system must be placed under the root directory `/dropzone/install` within the container. `dropzone` treats this directory as the package root.

The internal structure under `/dropzone/install` should mirror a standard Unix filesystem hierarchy:

```text
/dropzone/install/
├── bin/          # Executable binaries and scripts.
├── lib/          # Shared libraries.
├── share/        # Architecture-independent data (documentation, man pages, assets).
├── etc/          # Configuration files.
└── var/          # Variable data (logs, temporary files).
```

### 2.1. `/dropzone/install/bin/` (Required for executables)
Any file placed in this directory will be candidates for symbolic linking into the user's `PATH`.
*   **Conflict Resolution:** If a binary with the same name exists in another installed package or on the host system, `dropzone` will apply conflict resolution strategies (e.g., warnings, skipping, or overwriting based on user preference).

### 2.2. `/dropzone/install/lib/`
Shared libraries required by the binaries in `bin/` should be placed here.
*   *Note:* `dropzone` does not currently add this directory to `LD_LIBRARY_PATH` automatically on the host. Binaries should preferably be statically linked or use `rpath` to locate libraries relative to the executable, or the package might need a wrapper script in `bin/` to set up the environment.

### 2.3. `/dropzone/install/share/`, `/dropzone/install/etc/`
These directories are extracted to the host storage but are not automatically linked or integrated in the MVP phase. They are available for the application to use if it knows its relative install path.

## 3. Package Metadata (Labels)

The `Dockerfile` must include specific `LABEL` instructions in the **final build stage** to identify the package.

### 3.1. `dropzone.package` (Required)
Identifies the unique name of the package.

```dockerfile
LABEL dropzone.package="my-package-name"
```

## 4. Example Dockerfile

Below is a complete example of a `Dockerfile` that builds a Go application and prepares it as a compliant care package.

```dockerfile
# Stage 1: Build the application
# We use a standard builder image to compile the source code.
FROM golang:1.23-alpine AS builder

WORKDIR /app

# Copy source code
COPY . .

# Build the binary.
# Statically linking is recommended to avoid dependency issues on the host.
RUN CGO_ENABLED=0 go build -o myapp ./cmd/myapp

# Stage 2: Create the Care Package
# This stage prepares the artifacts for dropzone.
FROM alpine:latest

# Metadata Label (Required)
LABEL dropzone.package="myapp"

# Set up the installation directory structure
WORKDIR /dropzone/install

# Create standard directories
RUN mkdir -p bin lib share/doc/myapp

# Copy the executable from the builder stage
COPY --from=builder /app/myapp bin/myapp

# Copy additional assets (e.g., documentation)
COPY README.md share/doc/myapp/

# The container entrypoint/cmd is generally not used by dropzone itself,
# but can be useful for testing the container directly.
CMD ["/dropzone/install/bin/myapp"]
```

## 5. Best Practices

1.  **Static Linking:** Whenever possible, compile binaries statically. This ensures they run on the host system regardless of the host's installed libraries (libc version, etc.).
2.  **Relative Paths:** If your application loads configuration or assets, ensure it can locate them relative to the executable's path (e.g., `../etc/config.yaml` relative to `bin/myapp`), as the absolute path on the host (`~/.dropzone/packages/myapp/v1.0.0/...`) will vary.
3.  **Minimal Final Image:** Using `scratch` or `alpine` as the base for the final stage keeps the download size small, even though `dropzone` extracts the files rather than running the container.