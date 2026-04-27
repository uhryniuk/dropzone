# Dropzone Design Document

## 1. Introduction

`dropzone` is a consumer CLI for installing binaries out of signed OCI container images onto a Linux or macOS host. It treats OCI registries as first-class package registries: users can add registries, browse catalogs, list tags, install the entrypoint binary of an image, and check for updates, all against the live registry. The extracted binary runs natively on the host; no container runtime is involved at use time.

For each install, dropzone selects the manifest-list entry matching the host OS and architecture (`linux/amd64`, `linux/arm64`, `darwin/amd64`, `darwin/arm64`). Images that don't ship a matching platform entry are not installable, dropzone reports this clearly and does not attempt translation or VM-based execution. In practice this means Linux-only image catalogs (such as Chainguard's current offering) work on Linux hosts; macOS hosts need images that explicitly ship `darwin/*` platforms.

Signed images are the default, and signature verification is gated by a per-registry policy. Unsigned images refuse to install unless the user explicitly opts in with `--allow-unsigned`. On first run, dropzone seeds one registry: `docker.io/chainguard`, pre-configured with the correct cosign keyless identity policy. Chainguard publishes a public catalog of signed CLI tools, and `docker.io/chainguard` allows anonymous pulls, so it's a reasonable starting point for a new user. It is a recommended seed, not a hard-wired default; users can swap or supplement it with any other signed OCI registry. The out-of-the-box experience is to install CLI tools with a verified provenance trail: the binary came from a specific signing identity at a specific image digest, with whatever SBOM and SLSA provenance the publisher chose to attach.

Dropzone stops at the signature. It does not make claims about whether the underlying image is minimal, CVE-patched, or hardened in any other content sense. Those are properties of how the image was built, attested by the publisher, and surfaced as install-time output for the user to read. Verifying the signature confirms who built it and which image digest they signed. Everything else flows from trusting that signing identity.

## 2. Goals

*   **Use OCI registries as package registries.** Walk the `/v2/` distribution API directly for catalog, tags, and manifest operations. Behave like a registry client, not a `docker pull` wrapper.
*   **Install binaries natively on the host.** Unpack the full image rootfs into a per-package directory, generate a wrapper script at `~/.dropzone/bin/<name>` that invokes the entrypoint from inside the bundled rootfs with library paths set appropriately. The user runs it like any other binary.
*   **Verify provenance with cosign / Sigstore.** Every install performs keyless signature verification against a registry-specific policy. Fail closed unless the user opts in to an unsigned install.
*   **Surface attestations on install.** Show signer identity, SBOM availability, SLSA provenance, and vulnerability-scan summary at install time so users have everything they need to judge whether to trust the publisher.
*   **Update against the live registry.** `dz update` hits `/v2/<name>/tags/list` and digest endpoints for installed packages, detects rebuilds of the same tag (CVE-patch rolls) as well as newer tags, and re-shims.
*   **Simplicity.** No Dockerfile authoring, no custom package format, no GPG, no credential store of our own. Consume OCI images that already exist on registries we can reach.

## 3. Non-goals (MVP)

*   Local package building. `dz build` is gone. Users do not author packages through dropzone.
*   Publishing artifacts. No `dz push`. A future `dz publish` is plausible but out of scope.
*   GPG or any signing scheme other than Sigstore.
*   Non-OCI control planes (GitHub Releases, S3). Gone from the design.
*   Automatic platform translation. We install whatever the registry resolves for the host OS + arch. If the manifest list has no compatible entry, install fails with a clear error. No Linux-on-macOS VM, no Rosetta-style translation.
*   Multiple-binary packages. MVP installs the image's entrypoint binary only.
*   Shell-script entrypoints. If `ENTRYPOINT[0]` is not an ELF (Linux) or Mach-O (macOS) matching the host, install fails with a clear error. Chainguard's "Dev variant" images use script entrypoints; consume the non-Dev variant instead.
*   Windows hosts.
*   Dependency resolution between packages.

## 4. Architecture

```
                     +------------------+
                     |   dropzone CLI   |  add-registry / list-registries
                     |                  |  search / tags
                     |                  |  install / list / update / remove
                     +--------+---------+
                              |
              +---------------+----------------+
              |               |                |
              v               v                v
   +--------------------+ +---------------+ +--------------+
   | Registry Manager   | | Cosign        | | Host         |
   | - configured list  | | Verifier      | | Integrator   |
   | - per-registry     | | - sig policy  | | - PATH setup |
   |   cosign policy    | | - attestation | | - symlinks   |
   | - catalog / tags   | |   fetch       | | - conflicts  |
   | - pull             | +-------+-------+ +------+-------+
   +---------+----------+         |                ^
             |                    |                |
             v                    v                |
   +--------------------------------------+        |
   | Shim Builder                         |--------+
   |  - unpack full image rootfs          |
   |  - validate entrypoint (ELF/Mach-O)  |
   |  - write POSIX wrapper script        |
   |  - no binary rewriting               |
   +--------------------------------------+
                              |
                              v
                     +------------------+
                     | Local Store      |
                     | ~/.dropzone/     |
                     |  packages/<n>/   |
                     |  bin/ (symlinks) |
                     |  cache/          |
                     |  config/         |
                     +------------------+
```

### 4.1. Registry Manager (`internal/registry/`)

Owns the list of configured registries. Talks the OCI distribution `/v2/` API directly via `google/go-containerregistry` as a Go library (no subprocess shell-outs to `docker` or `crane`). Exposes:

*   `Catalog(registry)`, list repositories, via `/v2/_catalog`. Best-effort: registries that disable the endpoint return a typed "catalog unavailable" error that surfaces cleanly in the CLI.
*   `Tags(registry, image)`, list tags via `/v2/<name>/tags/list`. Widely supported even when catalog is not.
*   `Resolve(ref)`, resolve a reference (short name or fully qualified) to a registry + image + tag + digest.
*   `Pull(ref, stagingDir)`, fetch the manifest, resolve the host-compatible entry from the manifest list, pull and flatten layers into a staging directory. Also returns the image config so the caller can read `Entrypoint`.

Authentication uses the user's existing `~/.docker/config.json` and Docker credential helpers, `go-containerregistry` handles this natively.

Catalog and tag responses cache under `~/.dropzone/cache/<registry-name>/` with a short TTL. `dz update` forces a refresh.

### 4.2. Cosign Verifier (`internal/cosign/`)

Runs Sigstore verification against the policy attached to the source registry. Pre-seeded Chainguard policy pins:

*   `certificate_oidc_issuer: https://token.actions.githubusercontent.com`
*   `certificate_identity_regex: https://github.com/chainguard-images/images/.*`

User-added registries either declare a policy at `add` time (raw fields or a `--template`) or require `--allow-unsigned` per install.

Uses `github.com/sigstore/sigstore-go` as a Go library. No external `cosign` binary, no subprocess calls. This keeps dropzone a single static Go binary with no runtime dependencies.

Attestation surfacing: after signature verification, fetch SBOM + SLSA provenance + vulnerability-scan attestations via the same library path (the Sigstore bundle format carries attestations alongside signatures) and summarize them in the install output.

### 4.3. Shim Builder (`internal/shim/`)

Given the staging filesystem from the registry pull and the entrypoint path from the image config:

1.  Confirm `ENTRYPOINT[0]` exists in the rootfs and is an ELF (Linux hosts) or Mach-O (macOS hosts) matching the host CPU arch. Reject shell-script entrypoints, empty entrypoints, and binaries for the wrong OS/arch with a clear error.
2.  Move (or copy) the entire staged rootfs into `~/.dropzone/packages/<name>/<digest>/rootfs/`. No closure walk, no ELF/Mach-O rewriting, no selective copy. The whole image filesystem is preserved so any runtime dep the binary reaches for, plugins, locale data, CA bundles, NSS modules bundled in the image, is present.
3.  Detect the dynamic loader inside the rootfs (best-effort, from a list of well-known paths: `/lib64/ld-linux-x86-64.so.2`, `/lib/ld-linux-aarch64.so.1`, `/lib/ld-musl-x86_64.so.1`, etc. on Linux; on macOS the loader is always the system's `dyld`, so there's nothing to bundle).
4.  Generate a POSIX wrapper script at `~/.dropzone/bin/<name>` pointing at `~/.dropzone/packages/<name>/current/rootfs/<entrypoint>`. The wrapper sets library-search env vars to the bundled rootfs's `lib` directories and invokes the bundled loader directly (Linux) or relies on the system loader with a modified search path (macOS).

Linux wrapper template (dynamically linked ELF):

```sh
#!/bin/sh
PKG="$HOME/.dropzone/packages/<name>/current"
ROOT="$PKG/rootfs"
exec "$ROOT/<loader>" --library-path "$ROOT/usr/lib:$ROOT/lib:$ROOT/usr/local/lib" "$ROOT/<entrypoint>" "$@"
```

Linux wrapper template (statically linked, no loader detected):

```sh
#!/bin/sh
exec "$HOME/.dropzone/packages/<name>/current/rootfs/<entrypoint>" "$@"
```

macOS wrapper template:

```sh
#!/bin/sh
PKG="$HOME/.dropzone/packages/<name>/current"
ROOT="$PKG/rootfs"
DYLD_FALLBACK_LIBRARY_PATH="$ROOT/usr/lib:$ROOT/lib:$DYLD_FALLBACK_LIBRARY_PATH" \
    exec "$ROOT/<entrypoint>" "$@"
```

Pointing at `current` (a symlink to the active digest directory) means updates don't need to touch the wrapper, flipping `current` is enough.

If `ENTRYPOINT` has baked arguments (e.g., `["/usr/bin/tool", "--flag"]`), they're preserved verbatim before `"$@"`.

Known limitations, documented, accepted, not worked around in MVP:

*   The binary still runs against the *host's* `/etc` (resolv.conf, passwd, nsswitch), `/proc`, `/sys`, `/dev`, `/tmp`. No chroot, no namespaces, that would reintroduce a container runtime at use time.
*   Paths hard-coded into the binary as absolute (e.g., `/etc/ssl/certs/ca-certificates.crt`) resolve against the host, not the rootfs. For TLS-sensitive tools, users can set `SSL_CERT_FILE` via shell wrapper or we add per-package env hints post-MVP.
*   macOS System Integrity Protection strips `DYLD_*` env vars in some contexts (system binaries launching children). For our case, user-installed binaries in `$HOME`, SIP does not strip, so the wrapper works.

### 4.4. Host Integrator (`internal/hostintegration/`)

Owns the dropzone-managed parts of the user environment: the bin directory and (only when explicitly requested) the shell rc file.

*   `SetupDropzoneBinPath` creates `~/.dropzone/bin` if missing and prints PATH advice when the directory is not on `$PATH`. It never edits shell rc files. That is reserved for the explicit `dz path setup` command.
*   `InstallWrapper` writes the POSIX wrapper script generated by `internal/shim` into `~/.dropzone/bin/<name>` with mode 0755. A marker comment identifies the file as ours; subsequent `RemoveWrapper` and overwrite checks key off that marker so a user-written script at the same path is never touched.
*   `RemoveWrapper` deletes the wrapper if and only if the marker is present. Files that are not dropzone wrappers are left in place.
*   `SetupShellRC` and `UnsetShellRC` add or remove a clearly delimited block in `~/.zshrc` (zsh) or `~/.bash_profile` / `~/.bashrc` (bash, with `~/.bash_profile` preferred on macOS). Idempotent: the block is keyed by a marker so re-running is a no-op when present. `dz path` exposes the status.

### 4.5. Local Store (`internal/localstore/`)

`~/.dropzone/` layout:

```
~/.dropzone/
├── bin/                     # wrapper scripts (one per installed package)
├── packages/
│   └── <name>/
│       ├── current          # symlink → active digest dir
│       └── <digest>/
│           ├── rootfs/      # full unpacked image filesystem
│           └── metadata.json
├── cache/
│   └── <registry>/          # catalog + tags cache
└── config/
    └── config.yaml
```

Package metadata records:

*   Source registry name.
*   Image reference and tag the user asked for.
*   Resolved digest at install time (required for `dz update` to detect rebuilds).
*   Install timestamp.
*   Signature verification status at install time.

### 4.6. Configuration

`~/.dropzone/config/config.yaml`:

```yaml
default_registry: chainguard
registries:
  - name: chainguard
    url: docker.io/chainguard
    cosign_policy:
      issuer: https://token.actions.githubusercontent.com
      identity_regex: https://github.com/chainguard-images/images/.*
  - name: mycorp
    url: registry.mycorp.example/signed
    cosign_policy:
      issuer: https://accounts.google.com
      identity_regex: .*@mycorp.example
```

No credential fields. Registry auth is delegated to Docker credential helpers.

## 5. Commands

### 5.1. Registry management

*   `dz add registry <name> <url> [--template github|gitlab] [--identity-issuer <url>] [--identity-regex <regex>]`, register a new registry. Pre-seeded on first run with a `chainguard` entry.
*   `dz list registries`, tabular output of configured registries and their policies.
*   `dz remove registry <name>`, unregister.

### 5.2. Discovery

*   `dz search [<term>] [--registry <name>]`, list repositories in a registry via `/v2/_catalog`. Gracefully prints "catalog unavailable" when the registry disables the endpoint.
*   `dz tags <image> [--registry <name>]`, list tags for a specific image.

### 5.3. Lifecycle

*   `dz install <ref> [--allow-unsigned]`, pull, verify, shim, link. `<ref>` may be a short name (expanded against `default_registry`) or fully qualified.
*   `dz list`, installed packages with their source registry and current tag/digest.
*   `dz update [<name>]`, for each installed package, query the source registry's tag list, compare installed digest to current digest for the installed tag, and prompt for re-install if the digest has moved (same-tag rebuilds) or offer upgrade to a newer tag.
*   `dz remove <name>`, unshim and delete.
*   `dz version`, print the dropzone binary version.

## 6. Install flow

1.  Parse `<ref>`; resolve against `default_registry` if short.
2.  Registry Manager fetches the manifest list and selects the entry matching the host OS + arch (`linux/amd64`, `linux/arm64`, `darwin/amd64`, `darwin/arm64`). No matching entry → abort with a clear error.
3.  Fetch the image config; it must have a non-empty `Entrypoint`.
4.  Sigstore Verifier runs against the registry's policy. If signature verification fails and `--allow-unsigned` was not passed, abort with a clear error.
5.  Registry Manager flattens layers into a staging directory.
6.  Shim Builder confirms `Entrypoint[0]` is an ELF or Mach-O for the host, moves the staged rootfs into the package directory, and writes the wrapper script at `~/.dropzone/bin/<name>`.
7.  Local Store records metadata including the resolved digest.
8.  Host Integrator flips `~/.dropzone/packages/<name>/current` to the new digest directory.
9.  Install prints a summary: signer identity, attestations available, CVE-scan summary if present.

## 7. Update flow

For each installed package (or the named one):

1.  Look up source registry + image + installed tag + installed digest from metadata.
2.  Hit `/v2/<name>/tags/list` for the registry. If the installed tag is a floating tag (e.g., `latest`, `3.7`), also resolve that tag's current digest.
3.  Report two kinds of update:
    *   **Same tag, new digest**, a CVE-patch rebuild. Prompt to re-install to pick up the patch.
    *   **New tag**, a newer version available. Prompt to move.
4.  On confirmation, re-run the install flow, then clean up the old package directory.

This is the feature that makes the registry feel like a real registry, installed packages track upstream rebuilds, not just new version numbers.

## 8. Security model

Trust flows from a registry's cosign policy. Verification is keyless / Sigstore-based. A verified signature says: "an identity matching the registry's configured policy signed this specific image digest." For the Chainguard default, that identity is the `chainguard-images/images` repository's GitHub Actions runner, so a verified signature is evidence the image came out of Chainguard's build pipeline. Whether that pipeline produced an image with any particular property (minimal, CVE-patched, reproducible) is not something dropzone evaluates; it's something the user infers by trusting that signing identity.

Attestations layered on top of signatures, SBOM, SLSA provenance, vulnerability scan, are surfaced to the user but not themselves gating. The gating decision is "is this signature valid under the registry's policy?"

`--allow-unsigned` bypasses verification entirely and is logged in the package metadata as `signature_verified: false`. `dz list` surfaces unsigned installs distinctly.

Known non-coverage, called out explicitly so we don't pretend otherwise:

*   The binary runs against the host's `/etc/resolv.conf`, `/etc/ssl/certs`, `/etc/passwd`, `/proc`, `/sys`, `/dev`, `/tmp`. We unpack the container's rootfs and point library-search env vars at it, but we do not chroot or namespace-isolate. A container's binary running on the host's system configuration is the intended boundary.
*   Absolute paths baked into binaries (CA bundles, timezone data, locale) resolve against the host, not the bundled rootfs. Tools that hard-code these may misbehave.
*   A compromised cosign policy (wrong identity regex) defeats the trust story. Operators of user-added registries need to understand what they're pinning.

## 9. Future work

The shipped feature set covers v0.1 end to end. Items that are deferred (with rationale) live in `BACKBURNER.md`. The headline ones:

*   **`dz publish`**: a build, sign, and push flow for users who want to ship their own signed images through dropzone.
*   **Multiple binaries per image**: honor `CMD` or additional image labels to expose more than one binary per image.
*   **Attestation-based install policies**: a way to refuse images with open critical CVEs per the attached vuln scan, and similar gating rules. Requires a small policy language.
*   **Per-attestation cryptographic verification**: today we surface the in-toto statement contents but trust derives from the image-level signature; verifying each DSSE envelope independently is a future enhancement.
*   **Non-keyless signers**: support signing keys in addition to identity-based verification.
*   **Semver-aware tag ordering** for `dz update`: the lexicographic ordering plus floating-tag exclusion is good enough for most cases but a real semver comparator would handle pre-release suffixes correctly.
*   **Layer deduplication across packages**: a content-addressed layer store with hard links would cut disk usage on hosts with many installs.
*   **Auto env-var hints** when the bundled rootfs contains a recognizable CA bundle, timezone database, or locale data.
*   **Windows hosts.**
*   **Dependency resolution between packages.**
