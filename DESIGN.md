# Dropzone Design Document

## 1. Introduction

`dropzone` is a novel meta-package manager designed to leverage the power and isolation of OCI (Open Container Initiative) containers for building and delivering software packages. It introduces the concept of "care packages," which are essentially self-contained software distributions built within Docker or Podman containers. The primary goal is to provide a reproducible, isolated, and portable way to manage software dependencies and applications, making them easily consumable on a host system.

A key aspect of `dropzone` is its decentralized nature, utilizing "control planes" which are essentially repositories for discovering and distributing care packages. These control planes can be any accessible location capable of hosting container images or package artifacts, such as container registries, GitHub Releases pages, or S3 buckets.

## 2. Goals

The `dropzone` project aims to achieve the following:

*   **Reproducibility**: Ensure that package builds are consistent across different environments by encapsulating all build-time dependencies within an OCI container.
*   **Isolation**: Prevent dependency conflicts on the host system by building and packaging applications in isolated container environments.
*   **Portability**: Allow "care packages" to be easily shared and installed across various Linux distributions and environments that support Docker or Podman.
*   **Simplicity**: Utilize familiar container technologies (Dockerfiles/Containerfiles) as the primary package definition format, reducing the learning curve for developers.
*   **Version Control Integration**: Naturally integrate with existing version control systems by treating Dockerfiles as the source of truth for package definitions.
*   **Host System Integration**: Seamlessly integrate container-built applications into the host's user environment, making them accessible via the PATH, with robust conflict resolution.
*   **Decentralization**: Empower users to define and manage their own package sources (control planes) without reliance on a central authority, including support for private repositories.
*   **Attestation**: Provide mechanisms for package integrity verification through signed checksums, ensuring both integrity and authenticity.
*   **Developer Experience**: Enable package creators to easily define and build care packages, including passing custom build arguments.

## 3. Minimum Viable Product (MVP) Design

The MVP focuses on the core functionality required to establish `dropzone` as a usable and valuable decentralized package manager.

### 3.1. MVP Core Concepts

*   **Care Packages**: Self-contained software distributions built in OCI containers, organized into a specific `/dropzone/install` directory structure. Identified by `LABEL dropzone.package="<package-name>"`.
*   **OCI Container Builds**: All packages are built using Docker or Podman, leveraging multi-stage builds.
*   **Dockerfile/Containerfile as Package Definition**: The primary format for defining care packages, specifying build steps and final artifact layout.
*   **Host System Integration**: Care packages are integrated into the host's PATH via symbolic links from a `dropzone`-managed directory (`~/.dropzone/bin`).
*   **Control Planes (Decentralized Repositories)**: User-defined remote sources for discovering and distributing care packages (OCI registries, GitHub Releases, S3 buckets). **MVP includes support for authentication for private OCI registries.**
*   **GitHub User Discovery**: Users can simply provide a GitHub username (e.g., `dropzone add repo <username>`). Dropzone will automatically discover care packages in that user's `care-package` repository (via Releases or GHCR).
*   **Attestation (Signed Checksums)**: Care packages downloaded from control planes *must* be accompanied by and verified against cryptographically signed checksums to ensure both integrity and authenticity.
*   **Binary Conflict Resolution**: During installation, `dropzone` will detect and provide a clear, predictable mechanism for handling conflicts when multiple packages attempt to install binaries with the same name.
*   **Custom Build Arguments**: Users creating care packages can pass custom arguments/environment variables to the underlying `docker build` or `podman build` command.

### 3.2. MVP Architecture

`dropzone` will consist of these key components for the MVP:

*   **`dropzone` CLI**: The command-line interface for users to interact with `dropzone`, including commands for building, installing, listing, removing specific package versions, managing control planes, and authentication.
*   **Control Plane Manager**: Responsible for adding, removing, updating, and authenticating with configured control planes. It will abstract interactions with different repository types.
*   **Build Engine Abstraction**: Interfaces with Docker or Podman to execute container builds from Dockerfiles, *including passing custom build arguments*.
*   **Local Cache/Storage**: A dedicated directory (`~/.dropzone`) on the host system to store `dropzone`'s configuration, downloaded care package artifacts, extracted package contents, metadata, and fetched control plane indices.
*   **Runtime Linker/Mounter**: A component responsible for creating symbolic links to expose care package contents to the host's PATH, *implementing binary conflict detection and resolution*.
*   **Attestation Verifier**: A component to verify *signed checksums* of care packages retrieved from control planes, ensuring authenticity.

```/dev/null/dropzone_architecture.plantuml#L1-15
+-------------------+      +-------------------+\
|   dropzone CLI    |----->| Build Engine      |\
|  (Auth Prompts)   |      | (Docker/Podman,   |\
++-------------------+      | w/ Build Args)    |\
+          |                         +--------+----------+\
+          v                                   |\
+-------------------+      +-------------------+\
| Control Plane     |<-----| Package Building  |\
| Manager           |      | (Dockerfile exec) |\
|(w/ Authentication)|      +--------+----------+\
++--------+----------+            | (built artifacts)\
+          | (pull index)          v\
+          v            +-------------------+\
++-------------------+  | Attestation       |\
+|  Local Storage    |<---| Verifier          |\
+| (~/.dropzone)     |  | (Signed Checksums)|\
++--------+----------+  +-------------------+\
+          |                         ^\
+          v                         |\
++-------------------+      +-------------------+\
+| Runtime Linker/   |----->| Host System PATH  |\
+| Mounter           |      | (Symbolic Links/  |\
+|(w/ Conflict Res.) |      | Mount Points)     |\
++-------------------+      +-------------------+\
```

### 3.3. MVP Workflow

#### 3.3.1. Defining a Care Package

A user defines a care package by creating a `Dockerfile` (or `Containerfile`) in a dedicated directory. This `Dockerfile` must:
1.  Define a multi-stage build, if necessary.
2.  Have a final stage that copies the desired executables, libraries, and other assets into a specific output directory within the container, for example, `/dropzone/install`.
3.  Include `LABEL dropzone.package="<package-name>"` in the final stage.

Example `Dockerfile`:

```/dev/null/example.Dockerfile#L1-15
# Stage 1: Build the application
FROM golang:1.22-alpine AS builder
WORKDIR /app
COPY . .
RUN go mod tidy && go build -o myapp ./cmd/myapp

# Stage 2: Create the care package
FROM alpine:latest
LABEL dropzone.package="myapp"
WORKDIR /dropzone/install

# Create standard bin, lib, share directories within the package
RUN mkdir -p /dropzone/install/bin \
           /dropzone/install/lib \
           /dropzone/install/share

# Copy the built application into the 'bin' directory
COPY --from=builder /app/myapp /dropzone/install/bin/myapp
```

#### 3.3.2. Building a Care Package Locally

The user would invoke the `dropzone` CLI to build a care package from a `Dockerfile`, *optionally providing build arguments*:

```sh
dropzone build myapp /path/to/myapp/Dockerfile [--build-arg KEY=VALUE] [--env KEY=VALUE]
```

This command would:
1.  Initiate a Docker/Podman build process for the specified `Dockerfile`, *passing any provided build arguments or environment variables*.
2.  After a successful build, `dropzone` would extract the contents of the `/dropzone/install` directory from the final image stage into the `dropzone` local storage (e.g., `~/.dropzone/packages/myapp/v1.0.0`).
3.  Generate a cryptographic checksum for the extracted package contents and *prompt the user to sign it (e.g., with GPG or a specified key) before storing, if enabled*.
4.  Store metadata about the package, such as its version, the source Dockerfile hash, and the (signed) checksum.

#### 3.3.3. Managing Control Planes (Including Authentication)

Users can add new control planes to their `dropzone` client:

```sh
dropzone add repo my-registry oci://myregistry.example.com/dropzone-packages [--username <user>] [--password <pass>|--token <token>]
dropzone add repo my-gh-releases github://myorg/myrepo/releases [--token <token>]
dropzone add repo my-s3 s3://my-dropzone-bucket/packages [--access-key <key>] [--secret-key <secret>]
dropzone add repo <github-username> # Auto-discovers <github-username>/care-package
```

The `dropzone update` command would then poll all configured repositories to check for updates:

```sh
dropzone update
```

This command would:
1.  Connect to each configured control plane, *using stored or provided authentication credentials if it's a private repository*.
2.  Fetch metadata (e.g., package lists, versions, signed checksums) for available care packages.
3.  Update the local cache of available packages.

#### 3.3.4. Installing a Care Package

To make a care package accessible on the host system, potentially from a remote control plane:

```sh
dropzone install myapp[:<tag>]
```

This command would:
1.  If `myapp` is not locally available, `dropzone` would consult its configured control planes to find the latest or specified version of `myapp`.
2.  Download the care package artifact (e.g., a container image, a tarball) from the relevant control plane, *authenticating if necessary*.
3.  *Verify the integrity and authenticity of the downloaded package using its signed checksum.* If verification fails, the installation is aborted.
4.  Extract the contents of the `/dropzone/install` directory from the artifact into the `dropzone` local storage (e.g., `~/.dropzone/packages/myapp/v1.0.0`).
5.  *Before linking, `dropzone` will detect any binary name conflicts with already installed care packages or existing `PATH` binaries.* If conflicts are found, it will warn the user and apply a defined resolution strategy (e.g., allow explicit `--force` to overwrite, or suggest a `dropzone-` prefixed alternative).
6.  Create symbolic links from `~/.dropzone/packages/myapp/v1.0.0/bin/*` to a `dropzone`-managed binary directory (e.g., `~/.dropzone/bin`).
7.  Ensure that `~/.dropzone/bin` is added to the user's `PATH` environment variable (prompting the user for shell configuration updates if necessary).

#### 3.3.5. Listing Installed/Available Packages

```sh
dropzone list [--installed|--available|--repo <name>|--package <name>]
dropzone tags <package-name> [--repo <name>]
```

The `dropzone list` command would display a list of all locally available and/or installed care packages, along with their versions, indicating which control plane they originated from and their installation status.
The `dropzone tags` command would display all available versions/tags for a specific package, potentially filtered by a control plane.

#### 3.3.6. Removing a Care Package (Version-Specific)

```sh
dropzone remove myapp[:<tag>]
```

This command would:
1.  Remove the symbolic links associated with the *specified version* of `myapp` from the `dropzone`-managed binary directory. If no tag is specified, it will interactively prompt the user to choose a version or confirm removal of all.
2.  Remove the extracted package contents for the *specified version* from the local storage (`~/.dropzone/packages/`).
3.  Remove the metadata for the *specified version* from local storage.

### 3.4. MVP Package Directory Format

Within the final stage of the `Dockerfile`, the care package's contents should adhere to a conventional directory structure under `/dropzone/install`. This structure mirrors a typical Unix filesystem layout, making it easy for `dropzone` to integrate with the host's PATH.

```/dev/null/package_format.txt#L1-7
/dropzone/install/
├── bin/          # Executables and scripts
├── lib/          # Shared libraries
├── share/        # Architecture-independent data (docs, man pages, etc.)
├── etc/          # Configuration files (less common for care packages)
└── var/          # Variable data (logs, temporary files - if applicable)
```

For example, if a `myapp` care package provides an executable, it would be copied to `/dropzone/install/bin/myapp` within the container.

### 3.5. MVP Generic Use Case: Distributing User Environments

`dropzone` is ideal for distributing personal developer environments or specific toolchains. A single care package can encapsulate a collection of favorite binaries, configuration files, and even shell scripts, ensuring a consistent setup across different machines.

For example, a "my-dev-env" care package could include:
*   `neovim` (binary)
*   `zed` (binary)
*   `helix` (binary)
*   `firefox` (binary, potentially requiring more complex integration)
*   User configuration files (e.g., `.bashrc`, `.zshrc`, `.gitconfig` - symlinked or copied to appropriate user directories)
*   Custom scripts

This allows users to quickly provision a new machine with their preferred tools and configurations by simply installing their "my-dev-env" care package from a personal control plane.

### 3.6. MVP Installation and Setup of `dropzone`

`dropzone` itself would be distributed as a single binary. Upon first run or installation:
1.  It would create its home directory (e.g., `~/.dropzone`).
2.  It would create `~/.dropzone/bin` and add it to the user's `PATH` by modifying their shell configuration file (e.g., `~/.bashrc`, `~/.zshrc`).
3.  It would verify the presence of Docker or Podman on the system.

## 4. Future Enhancements and Roadmap

Features planned for beyond the initial MVP include:

*   **Advanced Version Management**: Explicitly support multiple versions of the same care package, allowing users to switch between them. (Basic version-specific removal is in MVP, but advanced switching/symlinking is future).
*   **Dependency Resolution**: A mechanism to declare and resolve dependencies between care packages, potentially using a metadata file (e.g., `dropzone.yaml`) alongside the `Dockerfile`.
*   **Security Scanning**: Integrate with container image security scanning tools during the build process.
*   **Platform Specificity**: Handling packages that might require different builds for different host architectures (e.g., ARM vs. x86).
*   **Rollback**: Ability to revert to a previous version of an installed care package.
*   **GUI/Web Interface**: A graphical user interface for managing care packages.
*   **Advanced Host Integration**: Explore more sophisticated methods for integrating diverse package types, such as FUSE for read-only filesystem layers for certain care packages, or managing desktop entries, systemd services, etc.
*   **Garbage Collection**: Automatically detect and remove orphaned files or unlinked packages from `~/.dropzone`.
*   **Query Language**: For complex `list` operations, a more powerful query language beyond simple flags could be considered (e.g., `dropzone find 'name=nginx AND status=installed'`).