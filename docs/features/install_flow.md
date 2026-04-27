# Feature: Install Flow

## 1. Overview

`dz install <ref>` is the end-to-end composition of every other feature. This doc describes the orchestration, how the Registry Manager, Cosign Verifier, Shim Builder, Host Integrator, and Local Store are wired together for a single install.

## 2. Goals

*   One clear sequential flow; each step has a single responsibility.
*   Fail closed at signature verification by default.
*   Deterministic output: same image digest → identical package directory.
*   Clean rollback: if any step after signature verification fails, the staging directory is removed and the local store is untouched.

## 3. Flow

```
┌──────────────┐
│ dz install X │
└──────┬───────┘
       │
       ▼
┌────────────────────────────────┐
│ 1. Resolve ref                 │    RegistryManager.Resolve
│   short name → default reg     │
│   validate fully-qualified     │
└──────┬─────────────────────────┘
       │
       ▼
┌────────────────────────────────┐
│ 2. Select platform             │    manifest list, match host OS+arch
│   linux/amd64, linux/arm64,    │    reject if no match
│   darwin/amd64, darwin/arm64   │
└──────┬─────────────────────────┘
       │
       ▼
┌────────────────────────────────┐
│ 3. Fetch manifest + config     │    go-containerregistry
│   extract Entrypoint           │
└──────┬─────────────────────────┘
       │
       ▼
┌────────────────────────────────┐
│ 4. Verify signature            │    sigstore-go
│   fail closed unless           │
│   --allow-unsigned             │
└──────┬─────────────────────────┘
       │
       ▼
┌────────────────────────────────┐
│ 5. Fetch attestations          │    sigstore-go (SBOM/Prov/Vuln)
│   best-effort, concurrent      │
│   failures degrade summary     │
└──────┬─────────────────────────┘
       │
       ▼
┌────────────────────────────────┐
│ 6. Pull + unpack rootfs        │    shim.UnpackRootfs
│   apply layers with whiteouts  │
│   into staging dir             │
└──────┬─────────────────────────┘
       │
       ▼
┌────────────────────────────────┐
│ 7. Identify + validate         │    shim.Identify
│   ENTRYPOINT[0] resolves       │
│   must be ELF (Linux) or       │
│   Mach-O (macOS) for host arch │
└──────┬─────────────────────────┘
       │
       ▼
┌────────────────────────────────┐
│ 8. Move rootfs to package dir  │    packages/<name>/<digest>/rootfs/
│   and write wrapper script     │    shim.Wrapper
└──────┬─────────────────────────┘
       │
       ▼
┌────────────────────────────────┐
│ 9. Write metadata              │    LocalStore.StorePackageMetadata
└──────┬─────────────────────────┘
       │
       ▼
┌────────────────────────────────┐
│ 10. Flip `current` symlink     │    packages/<name>/current → <digest>
│    install wrapper in bin/     │    conflict detection
└──────┬─────────────────────────┘
       │
       ▼
┌────────────────────────────────┐
│ 11. Print summary              │    signer, attestations, wrapper path
└────────────────────────────────┘
```

## 4. Components

### 4.1. `internal/packagehandler/install.go`

*   **`InstallPackage(ctx, ref string, opts InstallOptions) (*InstallResult, error)`**, implements the flow above.

```go
type InstallOptions struct {
    AllowUnsigned bool
    SmokeTest     bool // run `<bin> --version` after install
}

type InstallResult struct {
    Name              string
    Tag               string
    Digest            string
    SourceRegistry    string
    SignatureVerified bool
    Signer            string      // empty if unsigned
    Attestations      Attestations
    BinaryPath        string      // ~/.dropzone/bin/<name>
}
```

### 4.2. Staging and cleanup

*   Staging directory created via `os.MkdirTemp("", "dz-install-*")`.
*   `defer os.RemoveAll(stagingDir)` runs unconditionally, the Shim Builder copies what it needs into `~/.dropzone/packages/<name>/<version>/` before returning.
*   If any step 3-10 fails, the staging directory is cleaned up and the package directory (if partially created) is removed. Steps 1-2 don't touch disk.

### 4.3. Name conflict on install

The "package name" is derived from the image name (last path segment of the image reference). If a package with the same name is already installed:

*   Same digest: no-op with a message ("already installed").
*   Different digest: prompt for reinstall (unless `--yes`); replaces atomically by renaming the new package dir into place and unshimming+reshimming.

### 4.4. Partial-failure semantics

Each step is either atomic or has a cleanup path:

*   Steps 1-5 (network): failure → immediate abort, no disk state.
*   Step 6 (unpack): failure → staging dir is removed.
*   Steps 7-8 (identify + install): failure → staging dir + partial package dir removed.
*   Step 9 (metadata): failure → package dir removed.
*   Step 10 (current flip + wrapper): failure → package dir retained (user can retry `dz` internals to re-link), error shown.

Step 10 failing is the one non-transactional case, we keep the package on disk but report the broken wrapper / symlink state. A `dz link <name>` repair command is cheap to add if this proves annoying.

## 5. CLI integration

### `dz install <ref> [flags]`

*   `--allow-unsigned`, see attestation doc.
*   `--yes` / `-y`, skip confirmation on reinstall.
*   `--smoke-test`, run the binary with `--version` after install.

Typical output:

```
Resolving chainguard/jq...
  → cgr.dev/chainguard/jq@sha256:abc123... (linux/amd64)
Verifying signature...
  ✓ Signed by https://github.com/chainguard-images/images/.github/workflows/release.yaml@refs/heads/main
  Attestations: SBOM (SPDX, 142), Provenance (github-actions/chainguard-images/images), Vuln (0C/0H/2M/7L)
Pulling image (3 layers, 24 MB)...
Unpacking rootfs...
  entrypoint: /usr/bin/jq (ELF x86_64, dynamically linked)
  loader:     /lib64/ld-linux-x86-64.so.2
Installing to ~/.dropzone/packages/jq/sha256-abc123.../rootfs
Writing wrapper ~/.dropzone/bin/jq...

✓ Installed jq 1.7.1 (sha256:abc123...)
  Run `jq --version` to verify.
```

## 6. Testing

### 6.1. Unit

*   Each step isolated behind an interface (already true from the component design); `InstallPackage` tested with mocks for every component.
*   Failure-path tests: inject errors at each step, verify the cleanup contract.
*   Reinstall-same-digest no-op.
*   Reinstall-different-digest atomic replacement.

### 6.2. Integration

*   `dz install jq` against Chainguard default registry → shimmed binary runs, metadata recorded, signature verified.
*   Mutate the cosign policy so verification fails, verify install aborts with staging + package dir cleaned up.
*   `dz install` with a registry that has no policy, without `--allow-unsigned`, verify the abort message.
*   Same install with `--allow-unsigned`, verify metadata records `signature_verified: false`.
*   Install two different packages, verify both coexist in `~/.dropzone/`.

## 7. Technical details

*   **Concurrency:** steps 5 (attestation fetch) and 6 (pull + unpack) can overlap. MVP can ship serial first and parallelize later if install latency becomes a UX issue.
*   **Progress output:** layer download progress via go-containerregistry's remote options. Attestation fetches are fast enough to not need progress.
*   **Platform mismatch error:** if no manifest-list entry matches `runtime.GOOS`/`runtime.GOARCH`, the error names the platforms the image *did* offer: `"chainguard/jq has no darwin/arm64 build available. Platforms offered: linux/amd64, linux/arm64."`

## 8. Open questions

*   **Reinstall semantics for a moving tag** (e.g., `latest`): if `dz install jq:latest` is re-run and the digest changed, that's effectively an update. Should we detect this and route to the update flow, or treat it as a plain reinstall? Plain reinstall is simpler for MVP.
*   **Version directory naming.** Options: (a) the tag literally (`~/.dropzone/packages/jq/1.7.1/`), (b) the digest (`~/.dropzone/packages/jq/sha256-abc123.../`), (c) both (tag is a symlink to digest dir). (c) is cleanest; makes rollback trivial. Leaning (c) for MVP.
*   **SBOM attachment to install result:** we summarize it in output, but should we also save the raw SBOM file into the package dir? Small cost, potentially useful for future policy work.
