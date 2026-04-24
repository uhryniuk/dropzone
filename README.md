# Dropzone

**Dropzone** is a consumer CLI that installs binaries from signed OCI container images directly onto your Linux or macOS host. It treats container registries as first-class package registries: add a registry, browse it, install the entrypoint binary, and keep it up-to-date with CVE-patched rebuilds — all with Sigstore-verified provenance.

No container runtime at use time. The extracted binary runs natively against its bundled libraries.

## Concept

The idea is simple: **hardened container images already exist — use them as your source of truth for binaries.**

- **Pull** a signed OCI image from any registry (default: `cgr.dev/chainguard`).
- **Verify** the signature with cosign against a per-registry identity policy.
- **Shim** the entrypoint binary plus its dynamic library closure onto your `PATH`.
- **Update** directly against the registry to pick up CVE-patch rebuilds of the same tag.

Default registry is Chainguard, so `dz install jq` gives you a hardened, continuously-rebuilt `jq` with a cryptographic receipt. Any OCI registry is supported; unsigned images require `--allow-unsigned`.

## Features

*   **Registries as package registries** — `dz add registry`, `dz list registries`, `dz search`, `dz tags`. Walks the OCI distribution `/v2/` API directly.
*   **Cosign verification by default** — per-registry policies pin the signer identity. Pre-seeded with the correct Chainguard policy.
*   **Attestation surfacing** — install output shows signer identity, SBOM availability, SLSA provenance, and vulnerability-scan summary when present.
*   **Digest-pinned updates** — `dz update` detects same-tag rebuilds (CVE patches) as well as new tags, not just version bumps.
*   **Native execution** — the image's rootfs is unpacked and a wrapper script invokes the entrypoint with library paths pointing into the bundled rootfs. No container at runtime, no binary rewriting.
*   **Conflict resolution** — overwrites other dropzone-managed binaries with a warning; skips system-installed binaries with a warning and explanation.

## Installation

### Prerequisites

-   **Linux** (x86_64 or aarch64) or **macOS** (x86_64 or arm64)
-   A **POSIX shell** at `/bin/sh` (used by per-package wrapper scripts)
-   **Go** (1.23+) if building from source

No runtime dependencies. Dropzone ships as a single Go binary — cosign and patchelf are **not** required on your host. Signature verification uses `sigstore-go` as an embedded library; the OCI registry client is `go-containerregistry`, also embedded. Install time is a tar-unpack plus a wrapper-script write; no binary rewriting.

> **Platform note:** dropzone installs whatever platform entry the registry resolves for your host. If an image ships only `linux/*` platforms, it is not installable on macOS. Chainguard's current catalog is Linux-only; hardened macOS images are a separate and emerging space.

### Build from Source

```bash
git clone https://github.com/uhryniuk/dropzone.git
cd dropzone
CGO_ENABLED=0 go build -o dz ./cmd/dropzone
sudo mv dz /usr/local/bin/
```

`CGO_ENABLED=0` produces a fully static binary on Linux. On macOS the result links against system-provided frameworks only (no cgo), which is the standard way to ship a dependency-free macOS tool.

On first run, dropzone creates `~/.dropzone/` and prompts you to add `~/.dropzone/bin` to your `PATH`. The default `chainguard` registry is pre-configured.

## Usage

### Install a package

```bash
# Short name — expanded against the default registry
dz install jq

# Fully qualified registry + image + tag
dz install chainguard/jq:latest

# Install from a registry without a configured signing policy
dz install mycorp/internal-tool --allow-unsigned
```

### Manage registries

```bash
dz list registries

# Add your company's hardened registry with a GitHub Actions signing policy
dz add registry mycorp registry.mycorp.example/hardened \
  --identity-issuer https://token.actions.githubusercontent.com \
  --identity-regex 'https://github.com/mycorp/.*'

dz remove registry mycorp
```

### Browse

```bash
# List images in a registry (when /v2/_catalog is available)
dz search --registry chainguard

# List available tags for a specific image
dz tags jq
```

### List, update, remove

```bash
dz list                  # installed packages
dz update                # check every installed package against its source registry
dz update jq             # just one package
dz remove jq
```

## How it works

1. **Resolve** the reference to a registry + image + tag, then to a concrete digest, selecting the manifest entry for your host OS + arch.
2. **Verify** the image signature via Sigstore against the registry's policy. Fail closed unless `--allow-unsigned`.
3. **Unpack** the image's full rootfs into `~/.dropzone/packages/<name>/<digest>/rootfs/`. The container's libraries, loader, and bundled resources all come along.
4. **Generate a wrapper script** at `~/.dropzone/bin/<name>` that invokes the entrypoint from the bundled rootfs with library-search env vars pointing at the rootfs's `lib` directories. No binary rewriting.
5. **Record** the resolved digest in package metadata, so `dz update` can detect upstream rebuilds of the same tag.

## Roadmap

Deferred from MVP, in rough priority order:

-   `dz publish` — build + sign + push a hardened image for your own tools.
-   **Rollback** — restore the previous version after an update.
-   **Multiple binaries per image** — expose more than the entrypoint.
-   **Attestation-based install policies** — refuse images with open critical CVEs.
-   **Non-keyless signers** — support key-based cosign signatures.
-   **Non-Linux hosts.**

## License

[MIT](LICENSE)
