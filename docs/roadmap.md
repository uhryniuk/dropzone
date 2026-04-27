# Dropzone Build Status

This document was the implementation plan during the design pivot. All ten phases shipped. It now serves as a build history and a pointer at the commits that landed each phase.

For the current command surface and architecture, see `README.md` and `DESIGN.md`. For deferred items, see `BACKBURNER.md`.

## Ground rules that shaped the build

1. Delete before building. Out-of-scope code came out before new code went in, so there was no dead code mid-transition.
2. Prove the core end to end as early as possible. An ugly working `dz install` was worth more than three polished subsystems with no install path.
3. Security came after the install path worked. A signature-verified pull is useless without a working extract and shim, so the shim landed first and verification followed.

## Cross-cutting platform matrix

Every phase from Phase 2 onward targets Linux and macOS, on amd64 and arm64. Concretely:

* `runtime.GOOS` and `runtime.GOARCH` drive manifest-list platform selection in the registry pull and entrypoint validation in the shim builder.
* The wrapper generator has two templates (Linux, macOS) and a loader detection path that runs on Linux only. macOS uses the system `dyld` with `DYLD_FALLBACK_LIBRARY_PATH` set to the bundled rootfs.
* Shell rc setup picks `~/.zshrc` for zsh, `~/.bash_profile` (with fallback to `~/.bashrc`) for bash on macOS, and `~/.bashrc` for bash on Linux.
* `App.Initialize()` refuses to run on any `GOOS` other than `linux` or `darwin`.

## Shipped phases

### Phase 0. Scope removal `2dff49c`

Deleted `internal/controlplane/`, `internal/builder/`, `internal/download/`, and `internal/attestation/`. Stubbed every install/list/remove/update/search/tags/add-registry command behind a uniform `not yet reimplemented` error. Simplified the config schema to a placeholder and reworked the package handler test suite.

`dz version` worked; everything else returned the transition error cleanly.

### Phase 1. Config schema and registry manager `3fda768`

Rewrote the config schema around `default_registry`, `registries`, and `cosign_policy`. Pre-seeded the chainguard registry with the correct Sigstore identity pin on first run. Added `internal/registry/` with three pieces:

* `Manager` for the registry list, with `Resolve` for short-name and qualified-name reference parsing, plus `Add` / `Remove` / `Get` / `List` that mutate the underlying config and persist via a save callback.
* `Client` backed by `google/go-containerregistry` as an embedded library (no `docker pull` subprocess). `Catalog`, `Tags`, and `Digest` over `/v2/`. Catalog responses of 404, 401, 403, or 405 map to `ErrCatalogUnavailable` so the CLI can surface "use `dz tags` instead".
* `Cache` for catalog and per-image tag responses with a one-hour default TTL, slash-safe path scheme for nested image names, and silent recovery from corrupt cache files.

Tests: `Resolve` across short, qualified, nested, unknown-registry, and empty inputs; `Add` and `Remove` validation and persistence; cache miss / hit / stale / corrupt behavior; an httptest fake exercising the catalog status mapping, tags, and digest reads.

### Phase 2. Image pull with platform selection `305ef44`

Implemented `Client.Pull` end to end. For a manifest list, walk the entries and pick the one matching `runtime.GOOS` + `runtime.GOARCH`, skipping the `unknown/unknown` attestation manifests BuildKit emits. For a single-platform image, verify its declared OS and arch against the host. Either way, return a typed `ErrNoMatchingPlatform` listing the offered platforms when nothing matches.

Extraction uses `mutate.Extract` from go-containerregistry to flatten the layer stack and apply whiteouts, then a pure-Go tar reader at `internal/registry/extract.go` walks the flattened stream. Path traversal is rejected on entry names and on hardlink targets. Mode bits mask to `0o777`; setuid, setgid, and sticky bits drop (see `BACKBURNER.md`). Device and FIFO entries are skipped. UIDs and GIDs are ignored since dz runs as the user.

Added `internal/util/platform.go` with `HostOS`, `HostArch`, and a `SupportedPlatform` gate.

Phase 2 also brought a new feature outside the original roadmap: private registry login.

### Phase 2.5. Private registry login `8d3fd13`

Added a chained `authn.Keychain` that reads `~/.dropzone/auth.json` first and falls through to `authn.DefaultKeychain` (which already knows `~/.docker/config.json` and Docker credential helpers). `dz login` and `dz logout` write that file in the same format as Docker's config, mode 0600. Login does not validate credentials; a wrong password surfaces on the next install. Logout is idempotent and removes the file when it goes empty.

### Phase 3. Rootfs shim and wrapper script `063a084`

This was M1: the first end-to-end usable state.

Created `internal/shim/`:

* `IdentifyEntrypoint` resolves `ENTRYPOINT[0]` inside the rootfs, follows symlinks (rejecting any chain that escapes), checks ELF/Mach-O magic against the host OS, and verifies `e_machine` or Mach-O CPU type against `GOARCH`. Empty entrypoints, shell scripts, and wrong-arch binaries all produce typed errors the CLI surfaces cleanly.
* `FindLoader` reads `PT_INTERP` from the entrypoint ELF and confirms the loader exists inside the rootfs. Static binaries return an empty path with no error; broken images (dynamic binary, missing loader) are rejected.
* `GenerateWrapper` emits POSIX shell scripts: Linux dynamic uses the bundled loader with `--library-path`, Linux static execs the binary directly, and macOS sets `DYLD_FALLBACK_LIBRARY_PATH` and execs. Baked entrypoint args are shell-escaped, including embedded single quotes. A marker comment identifies dropzone-written wrappers for safe later removal.
* `Build` orchestrates the above and moves the staging rootfs into `packages/<name>/<digest-dir>/rootfs/`. Stale partial installs at the destination are nuked first. Cross-filesystem rename failure falls back to recursive copy.

Localstore was rewritten for the digest-directory layout with per-package `current` symlinks. `SetCurrent` is a symlink-then-rename atomic flip so concurrent readers see consistent state. Hostintegration swapped symlink-based binary linking for wrapper-script writing, with a marker check before any removal so user-written scripts at the same path are never touched.

Phase 3 also shipped `dz path` / `dz path setup` / `dz path unset` for shell rc management. This is the only path through which dropzone touches shell rc files; install never does.

The end-to-end install test pushes an image whose entrypoint is the running test binary (a guaranteed valid ELF or Mach-O for the host) into an in-process registry, runs `InstallPackage`, and verifies every on-disk artifact: wrapper file, rootfs layout, metadata.json, current symlink, ListInstalled, clean remove.

### Phase 4. Sigstore signature verification `6f919ad`

This was M2 stage one.

Added `internal/cosign/` with a `Policy` (cached compiled regex, `Validate`, `Match`) and a `Verifier` that lazy-loads the Sigstore public-good TUF root via `sigstore-go` on first use. Input validation (policy, digest, bundle JSON) runs before the TUF fetch so bad calls fail fast without paying the network cost. The `Verifier` returns the signing identity (SAN) and OIDC issuer for display and metadata.

Templates for `github`, `gitlab`, `google`, and `chainguard` are pre-baked. `chainguard` ships fully formed; the others want `--identity-regex` from the caller.

Bundle fetch landed in `internal/registry/bundle.go`. Two paths, sidecar tag first (`sha256-<hex>.sig` with the `dev.sigstore.cosign/bundle` annotation, which is what Chainguard ships today) and OCI 1.1 referrers as fallback. `ErrBundleNotFound` when neither yields one. The registry client's transport and auth carry through.

Verification happens between the digest fetch and the shim build:

* Registry has a policy and the bundle verifies: install proceeds, metadata records signer and issuer.
* Registry has a policy but no bundle is found: hard stop. `--allow-unsigned` does not bypass.
* Registry has a policy and verification fails: hard stop. `--allow-unsigned` does not bypass.
* Registry has no policy and `--allow-unsigned` is passed: install proceeds, recorded as unsigned.
* Registry has no policy and the flag is absent: abort with a message that points at fixing the policy or passing the flag.

### Phase 5. Attestation surfacing `8c8236e`

This was M2 stage two: security story complete.

`internal/registry/attestations.go` pulls the cosign sidecar manifest at `sha256-<hex>.att` and decodes each layer as a DSSE envelope, returning `[]cosign.RawAttestation` keyed by predicate type. Best effort: a missing sidecar is `ErrAttestationsNotFound`, not a hard error.

`internal/cosign/attestations.go` summarizes raw in-toto statements:

* SPDX 2.x `packages` array and SPDX 3.x `@graph` elements (counting types containing `Package`).
* CycloneDX `components` array.
* SLSA v1 `buildDefinition` plus `runDetails`, and SLSA v0.2 `buildType` plus `builder`.
* Cosign vuln predicate, with severity bucketing across `results.vulnerabilities` and the flat `vulnerabilities` shape, case-insensitive matching, and `ScanFinishedOn` (or `ScanStartedOn`) for the scan timestamp.

Each summarizer is independent. A malformed SBOM does not suppress the provenance summary. Unknown predicate types are silently skipped. `DecodeDSSE` handles both base64-encoded and raw payload variants the ecosystem produces.

Attestation fetch runs after signature verification, only for verified images (an unverified image has no trust root for its attestations either). Fetch failures degrade the install summary; they never block the install.

`PackageMetadata.Attestations` persists the summary. The CLI renders an attestations block under a verified signature:

```
Attestations:
  SBOM:        SPDX (142 components)
  Provenance:  https://github.com/.../runners
  Vuln scan:   0C / 0H / 2M / 7L (scanned 2026-04-22)
```

### Phases 6, 7, and 8. Registries, updates, polish `c7510c0`

This was M3: feature-complete v0.1.

**Phase 6 (registry management commands).**

* `dz add registry <name> <url> [--template] [--identity-issuer] [--identity-regex] [--default]`. Templates pre-fill the issuer; identity-regex is required for `github`, `gitlab`, `google`. `chainguard` is fully formed. `--default` flips `default_registry` in the same call. Registries without a policy are saved with no `cosign_policy` block and require `--allow-unsigned` at install time.
* `dz list registries` shows name, URL, policy (chainguard, custom, or none), and a default marker.
* `dz remove registry <name>` refuses if installed packages still reference the registry; `--force` overrides. Removing the default clears the default pointer.
* `dz search [<term>] [--registry <name>]` queries `/v2/_catalog`, optionally substring-filters by term, and falls back to a "use `dz tags`" message when the registry disables the endpoint.
* `dz tags <image> [--registry <name>]` lists tags via `/v2/<name>/tags/list`. Image names can be qualified by a configured registry prefix.

**Phase 7 (update flow).**

`packagehandler.CheckUpdates` and `CheckUpdate` query each installed package's source registry for the current digest of the installed tag and the list of newer tags. The result captures:

* Same-tag rebuild: the installed tag's digest moved upstream. This is the headline signal for security-driven updates and the reason the registry-as-package-registry pitch holds.
* Newer tags: lexicographically greater than the installed tag, with floating tags (`latest`, `stable`, `edge`, `main`, `master`, `dev`) excluded. Semver-aware ordering is in `BACKBURNER.md`.
* Per-package `UnreachableError`: a registry failure on one package never hides updates available elsewhere.

`ApplyUpdate` re-runs the install flow at the target tag. The old digest directory is retained alongside the new one so a later `dz rollback` can flip back. The CLI exposes `dz update` for status, `dz update <name>` for check + prompt, `dz update --all` for batch apply, plus `--check`, `--yes`, and `--allow-unsigned`.

**Phase 8 (polish).**

* `dz rollback <name>` flips `current` to the most recent prior digest directory (sorted by metadata `InstalledAt`). Free with the digest-dir layout.
* `dz doctor [--fix]` scans for orphan wrappers (bin entry with no matching package), broken `current` symlinks, packages without wrappers, and PATH not configured. `--fix` applies the safe remediations (orphan wrapper removal and broken symlink cleanup).
* `dz purge [--yes]` rm -rfs `~/.dropzone/`. Refuses `/` and any non-absolute path. Shell PATH edits are not removed (run `dz path unset` first if you want a clean teardown).
* `dz list --json` for scripting.
* `dz list` gained a SIGNED column so verification state is visible at a glance.
* Cobra's `completion` subcommand is enabled, so `dz completion zsh > ~/.zsh/_dz` works.

## Tests

Final coverage breakdown:

* `internal/config`: schema seeding, save/load round-trip, malformed-YAML rejection, missing-field backfill, the older-config migration.
* `internal/util`: filesystem helpers and platform gating.
* `internal/registry`: `Resolve` across all reference shapes, `Add`/`Remove` validation, cache behavior, fake-registry catalog/tags/digest, manifest-list platform selection (matching, mismatch, attestation-entry skip, no-match error), tar extraction (regular files, symlinks, hardlinks, path traversal, hardlink escape, mode masking, special-file skip), bundle fetch via sidecar tag (success and not-found), attestation fetch via sidecar tag.
* `internal/cosign`: policy validate / match / template expansion, verifier input validation, digest decoder, attestation summarizers across SPDX 2.x, SPDX 3.x graph, CycloneDX, SLSA v1, SLSA v0.2, vuln severity bucketing, mixed predicate sets, unknown-type skip, DSSE decode in both base64 and raw payload shapes.
* `internal/shim`: entrypoint validation including symlink follow and escape rejection, ELF arch mismatch via in-place `e_machine` rewrite, loader detection (with skip when the test binary is statically linked), wrapper generation across linux dynamic / linux static / linux baked args / macOS / unsupported OS, shell-escape correctness, build move with stale-dir cleanup.
* `internal/hostintegration`: wrapper install with marker check (write, overwrite same wrapper, refuse non-dropzone file), wrapper remove safety, shell rc setup idempotency on zsh and bash with the macOS bash-profile preference, unset preserving surrounding user content, unsupported shells, PATH status reflection.
* `internal/app`: install end to end against an in-process registry (push image whose entrypoint is the test binary, run install, verify every artifact, list, remove), login flow (flags, `--password-stdin`, interactive prompt), logout (present and absent entries), version smoke, registry CLI add (signed, unsigned, duplicate, default flag), list registries, remove registry (success, unknown, blocked-by-installed-packages), update CLI (same-tag drift, newer-tag detection, floating-tag exclusion, unreachable-registry per-package error).

## What lives in BACKBURNER

Per-attestation cryptographic verification. Attestation-based install policies. Wrapper regeneration on rollback for cross-version cases. Semver-aware tag ordering. Old digest-directory pruning. Auto env-var hints for `SSL_CERT_FILE`, `TZDIR`, `LOCPATH`. Layer deduplication across packages. `dz publish` for the producer side. Windows host support. See `BACKBURNER.md` for the full list with rationales.
