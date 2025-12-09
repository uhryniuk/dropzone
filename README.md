# Dropzone

**Dropzone** is a decentralized meta-package manager that leverages OCI (Open Container Initiative) containers for building, distributing, and installing software. It treats containers as "care packages," allowing for reproducible builds, isolation, and seamless integration into your host system.

## 🚀 Concept

The core idea is simple: **Distribute binaries and tools using containers.**

-   **Build**: Packages are defined using standard `Dockerfile`s.
-   **Distribute**: Packages are pushed to standard container registries (like Docker Hub, GHCR, etc.) or other storage backends.
-   **Install**: Dropzone downloads the artifact, verifies its integrity via signed checksums, and integrates the binaries into your host's `PATH` using symbolic links.

This approach ensures that dependencies used during the build process don't pollute your host system, while the final application feels like a native installation.

## ✨ Features (MVP)

*   **📦 Local Package Building**: Build care packages from Dockerfiles using Docker or Podman. Supports custom build arguments and environment variables.
*   **🔐 Decentralized Control Planes**: Add any OCI-compatible registry as a package source. Supports authentication for private repositories.
*   **🛡️ Security & Attestation**: Enforces cryptographic checksum verification. Packages must be signed to be installed, ensuring integrity and authenticity.
*   **🤝 Host Integration**: Seamlessly links installed binaries to your user `PATH`.
*   **⚡ Conflict Resolution**: Robustly detects binary name conflicts with system tools or other packages, preventing accidental overwrites.
*   **📝 Version Management**: Install, list, and remove specific versions of packages.

## 🛠️ Installation

### Prerequisites

-   **Go** (1.23+)
-   **Docker** or **Podman** installed and running.
-   **GPG** (for signing and verifying packages).

### Build from Source

```bash
git clone https://github.com/uhryniuk/dropzone.git
cd dropzone
go build -o dropzone cmd/dropzone/main.go

# Optional: Move to a directory in your PATH
sudo mv dropzone /usr/local/bin/
```

## 📖 Usage

### 1. Initialize

On the first run, Dropzone will create its configuration directory at `~/.dropzone` and advise you on how to add `~/.dropzone/bin` to your `PATH`.

```bash
dropzone version
```

### 2. Build a Package Locally

Create a `Dockerfile` that defines your package. The final stage must use `LABEL dropzone.package="<name>"` and place files in `/dropzone/install`.

```bash
# Build the package (prompts for GPG signing)
dropzone build myapp ./Dockerfile --build-arg VERSION=1.0.0
```

### 3. Manage Repositories

Add a remote repository (Control Plane) to discover packages.

```bash
# Add a public OCI registry
dropzone add repo my-registry oci://registry.example.com/packages

# Add a private registry with authentication
dropzone add repo private-repo oci://private.example.com/packages --username myuser --token mytoken
```

Update the local package index:

```bash
dropzone update
```

### 4. Install a Package

Download and install a package. Dropzone verifies the signature and links binaries.

```bash
# Install the latest version
dropzone install myapp

# Install a specific version
dropzone install myapp:1.0.0
```

### 5. List and Remove

View installed and available packages:

```bash
dropzone list
```

Remove a package:

```bash
# Remove a specific version
dropzone remove myapp:1.0.0

# Remove all versions (interactive)
dropzone remove myapp
```

## 🗺️ Roadmap

-   [ ] **Dependency Resolution**: Declare dependencies between care packages (`dropzone.yaml`).
-   [ ] **Advanced Version Management**: "Switch" active versions of a binary without uninstalling.
-   [ ] **Multi-Architecture Support**: Build and install packages for different architectures (ARM vs x86).
-   [ ] **Rollback**: Easily revert to a previous working version of a package.
-   [ ] **Advanced Host Integration**: Support for man pages, desktop entries, and FUSE-based mounting.
-   [ ] **Security Scanning**: Integrate container scanning during the build process.
-   [ ] **Web/GUI Interface**: A visual manager for your care packages.

## 📄 License

[MIT](LICENSE)