# Feature: Binary Shimming

## 1. Overview

Turns a pulled OCI image into a runnable, self-contained package on the host. The approach is deliberately minimal:

1.  Unpack the full image rootfs into the package directory.
2.  Write a small POSIX wrapper script at `~/.dropzone/bin/<name>` that invokes the entrypoint from inside the bundled rootfs with appropriate library-search environment variables.

No ELF or Mach-O rewriting. No `patchelf`. No external tools. Pure-Go tar extraction and string-templated wrapper generation. This is what keeps dropzone a single self-contained binary with no runtime dependencies.

The trade-off: a package's on-disk footprint is the full image rootfs rather than a minimal closure. For Chainguard-style minimal base images this is small (typically a few MB to tens of MB). For larger "dev variant" images it would be bigger — but we don't target those (§3 below).

## 2. Goals

*   **One simple algorithm.** Unpack tar; write wrapper. Works for Linux and macOS.
*   **Handle the full range of runtime deps.** Locale data, CA bundles, plugin directories, anything the binary might `dlopen` — all bundled because the whole rootfs is present.
*   **No host library contamination.** Library-search env vars in the wrapper point only at the bundled rootfs.
*   **No binary rewriting.** The extracted binary is byte-identical to what was signed. Signatures remain valid, code integrity is preserved (relevant for macOS signed binaries).
*   **Cross-platform.** Same algorithm on Linux and macOS. The differences are confined to wrapper template and loader detection.

## 3. Scope

**In:**
*   ELF entrypoints on Linux (glibc or musl dynamic linking; static also supported).
*   Mach-O entrypoints on macOS (both `x86_64` and `arm64e`).
*   Entrypoints with baked arguments (e.g., `["/usr/bin/tool", "--default-flag"]`).

**Out (explicit rejections at install time):**
*   Empty `Entrypoint`. The image doesn't declare what to run.
*   Shell-script entrypoints (`ENTRYPOINT[0]` is `/bin/sh`, `/usr/bin/env`, or any non-ELF/non-Mach-O file). Chainguard's "Dev variant" images use script entrypoints; consume the non-Dev variant instead.
*   Platform mismatch. If the binary's ELF/Mach-O header says `x86_64` but the host is `arm64`, fail rather than try to translate.

## 4. Components

### 4.1. `internal/shim/unpack.go`

Pure-Go tar extraction.

*   **`UnpackRootfs(layers []v1.Layer, dest string) error`** — given the ordered layers from `go-containerregistry`, apply them on top of each other into `dest`. Each layer is a tar; later layers overlay earlier ones. Handle whiteout files (`.wh.*` entries) per the OCI image spec to preserve deletions.
*   Preserves file modes and symlinks. Refuses to write outside `dest` (defends against path-traversal entries in a malicious tar).
*   No setuid/setgid preservation for MVP — we're unpacking as the user, and a setuid bit on a user-writable file is a noisy footgun we don't need.

### 4.2. `internal/shim/entrypoint.go`

Identifies and validates the entrypoint from the unpacked rootfs.

*   **`Identify(rootfs string, imageConfig *ImageConfig, hostOS, hostArch string) (*Entrypoint, error)`** —
    1.  Read `ENTRYPOINT` from the image config. If empty → return `ErrNoEntrypoint`.
    2.  Resolve `ENTRYPOINT[0]` inside the rootfs. Follow symlinks within the rootfs (not outside it).
    3.  Read the binary header:
        *   On Linux hosts: expect ELF, check `e_machine` against host arch (`EM_X86_64` ↔ `amd64`, `EM_AARCH64` ↔ `arm64`).
        *   On macOS hosts: expect Mach-O, check CPU type against host arch (`CPU_TYPE_X86_64` ↔ `amd64`, `CPU_TYPE_ARM64` ↔ `arm64`).
    4.  On wrong format or mismatch, return a typed `ErrEntrypointIncompatible` with a clear message.
    5.  Return `{Path, BakedArgs, Format}` — path relative to rootfs, any `ENTRYPOINT[1:]` baked args, and the format (for wrapper generation).

Uses `debug/elf` and `debug/macho` from the Go stdlib. No external tools.

### 4.3. `internal/shim/loader.go` (Linux only)

Detects the bundled dynamic loader inside the rootfs. macOS uses the system `dyld`, so this is no-op there.

*   **`FindLoader(rootfs string) (string, error)`** — check well-known paths in order:
    *   `/lib64/ld-linux-x86-64.so.2` (glibc on amd64)
    *   `/lib/ld-linux-aarch64.so.1` (glibc on arm64)
    *   `/lib/ld-musl-x86_64.so.1` (musl on amd64)
    *   `/lib/ld-musl-aarch64.so.1` (musl on arm64)

    Returns the first match as a path relative to `rootfs`, or `ErrLoaderNotFound` if none. In that case the binary is assumed static and the wrapper skips the loader-invocation path.

### 4.4. `internal/shim/wrapper.go`

Generates the POSIX wrapper script at `~/.dropzone/bin/<name>`.

Two templates based on host and binary format.

**Linux, dynamically linked, loader present:**

```sh
#!/bin/sh
PKG="$HOME/.dropzone/packages/<name>/current"
ROOT="$PKG/rootfs"
exec "$ROOT/<loader>" \
    --library-path "$ROOT/usr/lib:$ROOT/lib:$ROOT/usr/local/lib:$ROOT/usr/lib64:$ROOT/lib64" \
    "$ROOT/<entrypoint>" <baked-args> "$@"
```

**Linux, static binary (no loader detected):**

```sh
#!/bin/sh
exec "$HOME/.dropzone/packages/<name>/current/rootfs/<entrypoint>" <baked-args> "$@"
```

**macOS:**

```sh
#!/bin/sh
PKG="$HOME/.dropzone/packages/<name>/current"
ROOT="$PKG/rootfs"
DYLD_FALLBACK_LIBRARY_PATH="$ROOT/usr/lib:$ROOT/lib:$DYLD_FALLBACK_LIBRARY_PATH" \
    exec "$ROOT/<entrypoint>" <baked-args> "$@"
```

Wrapper file mode `0755`. The `current` symlink target means updates don't rewrite the wrapper.

### 4.5. Platform selection

Platform selection happens upstream in `registry.Client.Pull` (see `registry_management.md`), matching the host's `runtime.GOOS` and `runtime.GOARCH` against the manifest list. By the time the Shim Builder sees the staging rootfs, the platform is already correct for the host. The ELF/Mach-O header check in `Identify` is a belt-and-suspenders validation.

## 5. Known limitations

Not bugs; design trade-offs. Documented here so we don't pretend otherwise.

1.  **Host `/etc` / `/proc` / `/sys` bleed-through.** The shimmed binary reads `/etc/resolv.conf`, `/etc/ssl/certs`, `/etc/passwd`, `/etc/nsswitch.conf` from the *host*, not the bundled rootfs. We don't chroot or namespace-isolate — that would reintroduce a container runtime at use time. For CLI tools this is typically fine; for security-sensitive tools with custom CA stores, users can set `SSL_CERT_FILE` explicitly.
2.  **Hard-coded absolute paths in binaries.** If a binary has `/etc/ssl/certs/ca-certificates.crt` baked in at compile time, the wrapper's library-path tricks don't help — it's not a library lookup. Host value wins. Chainguard generally compiles things portably, but this is a real limit.
3.  **macOS SIP + DYLD vars.** System Integrity Protection strips `DYLD_*` environment variables when launching system-owned binaries (under `/System`, `/usr/bin`, etc.). For binaries we install under `$HOME/.dropzone/`, SIP does not strip — the wrapper works. This does mean dropzone shimmed binaries cannot launch system binaries with `DYLD_*` propagated; that's a macOS policy, not our issue.
4.  **Large images inflate disk usage.** Unpacking the full rootfs means one mid-sized image = one mid-sized directory under `~/.dropzone/packages/`. For Chainguard's minimal images this is negligible. For heavier images it adds up. Future: layer sharing via hard links across packages. Not MVP.
5.  **No setuid preservation.** Binaries inside the rootfs that rely on setuid (rare in CLI tools, common in system binaries) won't get elevated privileges because we don't preserve the bit. Acceptable for MVP; this is a CLI-tools installer, not a system package manager.

## 6. Technical details

*   **Tar extraction:** `archive/tar` from the Go stdlib. Handles all the file types we care about (regular, dir, symlink, hard link, whiteout markers). No external tar binary.
*   **Whiteout handling:** per the OCI image spec, a file named `.wh.<name>` in a layer deletes `<name>` from the underlying rootfs at that layer. We process these during apply.
*   **ELF parsing:** `debug/elf` stdlib for the header machine check.
*   **Mach-O parsing:** `debug/macho` stdlib for the CPU type check. Handles both single-arch and universal (fat) binaries — though Chainguard-style single-platform-per-image means we shouldn't see universal binaries inside an image whose manifest entry declared a specific platform.
*   **Wrapper escaping:** entrypoint paths, baked args, and names are shell-escaped via a small helper (single-quote, `'\''` for embedded single quotes). Defends against a malicious image with a crafted entrypoint path.
*   **Static binary:** `CGO_ENABLED=0 go build` for Linux. On macOS, no cgo means dynamic linking only against system frameworks.

## 7. Testing

### 7.1. Unit

*   Tar extraction fixture set: single-layer, multi-layer with overlays, whiteout deletions, symlink traversal attempts (should refuse).
*   `Identify` on: ELF amd64, ELF arm64, Mach-O amd64, Mach-O arm64, shell script, empty entrypoint, entrypoint that doesn't exist in rootfs — each mapping to the expected outcome.
*   `FindLoader` on rootfs fixtures with each of the well-known loader paths, plus one with no loader (static).
*   Wrapper template rendering with various combinations of baked args, loader/no-loader, Linux/macOS.

### 7.2. Integration

*   Full install of `cgr.dev/chainguard/jq:latest` on Linux. Run the shimmed `jq --version` from the host shell; verify output.
*   Same on macOS, against whichever registry is shipping `darwin/*` images we can test with. If no darwin image is available at test time, verify the "no matching platform" error path.
*   Install an image whose entrypoint is a shell script; verify the clear error message ("script entrypoints are not supported; install the non-Dev variant").
*   Install an image built for the wrong arch (amd64 image on an arm64 host, with `--platform` hackery in the registry); verify the clear error at entrypoint validation.

## 8. Open questions

*   **Layer deduplication.** Two installed packages sharing Wolfi base layers each have their own copy on disk. Could hard-link across packages using a content-addressed layer store. Real win on a host with many installed packages; not MVP.
*   **CA bundle hint.** If the rootfs has a recognizable CA bundle (`/etc/ssl/certs/ca-certificates.crt`, `/etc/ssl/cert.pem`), the wrapper could auto-set `SSL_CERT_FILE` pointing at the bundled one. Low-risk enhancement; plausibly MVP.
*   **Locale data hint.** Similar — if `LC_ALL=C.UTF-8` resolves inside the rootfs, set it. Plausibly MVP.
*   **Static binary fast-path.** If `ENTRYPOINT[0]` is a static ELF with zero runtime deps, we technically don't need to unpack the whole rootfs. Detect and skip the unpack? Optimization; not MVP.
