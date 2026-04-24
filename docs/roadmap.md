# Dropzone Implementation Roadmap

## Purpose

This doc is the sequenced build plan for the pivot described in `DESIGN.md`. It assumes the design docs in `docs/features/` are the specification and translates them into a phased ordering where every phase ends at a demonstrable state.

Three rules shape the ordering:

1.  **Delete before building.** Out-of-scope code gets removed before new code replaces it, so there's no dead code mid-transition.
2.  **Prove the core end-to-end as early as possible.** An ugly but working `dz install jq` is worth more than three polished subsystems.
3.  **Security last among the user-visible features, not first.** Cosign verification is critical, but a working extract+shim flow without verification is testable; a signature-verified pull without a working shim isn't useful.

## Cross-cutting: platform matrix

Every phase from here on targets Linux and macOS simultaneously, on both amd64 and arm64. Practical consequences:

*   `runtime.GOOS` and `runtime.GOARCH` are consulted for manifest-list platform selection (Phase 2) and entrypoint format validation (Phase 3).
*   The Shim Builder's wrapper generator has two templates (Linux, macOS) and a loader detection path that's Linux-only.
*   CI matrix: `{linux, darwin} × {amd64, arm64}`. macOS runners are a cost but unavoidable for confidence.
*   Shell-rc setup picks different defaults per OS (bash uses `~/.bash_profile` on macOS, `~/.bashrc` on Linux; zsh uses `~/.zshrc` everywhere).
*   `App.Initialize()` refuses to run on any `GOOS` other than `linux` or `darwin`.

## Phase 0 — Scope removal

**Goal:** The repo no longer contains code for features we've removed from scope. Codebase compiles; nothing that's gone is still wired.

**Work:**

1.  Delete `internal/controlplane/github/`.
2.  Delete `internal/builder/`.
3.  Delete `internal/download/` (replaced in Phase 1 by the `registry` client).
4.  Delete `internal/attestation/` (replaced in Phase 4 by `internal/cosign/`).
5.  Remove the `build` command and its wiring in `internal/app/commands.go`.
6.  Drop the credential fields (`Password`, `Token`, `AccessKey`, `SecretKey`) from `internal/config/config.go`.
7.  Remove all references to the deleted packages from `internal/app/dropzone.go` and `internal/packagehandler/packagehandler.go`. The `InstallPackage` function becomes a stub that errors "not yet reimplemented"; `BuildPackage` is removed.
8.  Update `go.mod` / `go.sum` via `go mod tidy`.

**Exit criteria:** `go build ./...` succeeds. `go test ./...` passes (tests for deleted features are removed alongside them). `dz version` runs; everything else returns a clear "not implemented" error.

**Est. effort:** Small. ~half a day.

---

## Phase 1 — Config schema + Registry Manager skeleton

**Goal:** Config reflects the new schema and the registry manager can talk to an OCI registry for catalog, tags, and manifest operations.

**Depends on:** Phase 0.

**Work:**

1.  Rewrite `internal/config/config.go` to the new schema (`DefaultRegistry`, `Registries`, `CosignPolicy`). Seed the default `chainguard` entry on first load.
2.  Create `internal/registry/registry.go` with the `Registry` and `CosignPolicy` types.
3.  Create `internal/registry/manager.go` with `List`, `Get`, `Add`, `Remove`, `Resolve`.
4.  Create `internal/registry/client.go` wrapping `google/go-containerregistry`:
    *   `Catalog`, `Tags`, `Digest`. Add the typed `ErrCatalogUnavailable`.
    *   Stub `Pull` — returns an error "not yet implemented." Phase 2 implements it.
5.  Wire `RegistryManager` into `App.Initialize()`. Remove all remaining references to the old `controlplane` package.
6.  Delete `internal/controlplane/` entirely once the rename is done.
7.  Add `internal/registry/cache.go` for catalog/tag response caching under `~/.dropzone/cache/`.

**Exit criteria:** Unit tests cover `Resolve`, `Catalog` (including the 404/401/405 → ErrCatalogUnavailable mapping), `Tags`, `Digest`. Integration test against a local `registry:2` container passes.

**Est. effort:** Medium. 2–3 days.

---

## Phase 2 — Image pull + entrypoint identification

**Goal:** Registry Manager can pull an image, flatten layers into a staging directory, and return the image config.

**Depends on:** Phase 1.

**Work:**

1.  Implement `registry.Client.Pull`:
    *   Resolve manifest (supports manifest lists → pick `linux/<GOARCH>`).
    *   Download and flatten layers into a caller-provided staging dir.
    *   Parse the image config; return `ImageConfig{Entrypoint []string}`.
2.  Add host arch/OS detection helper in `internal/util/`.
3.  Integration test: pull `cgr.dev/chainguard/jq:latest` into a tmpdir, confirm `Entrypoint[0]` exists inside the staging root and is an ELF.

**Exit criteria:** `Pull` works end-to-end against Chainguard. Layer dedup and cleanup correct under failure.

**Est. effort:** Medium. ~2 days.

---

## Phase 3 — Shim Builder (rootfs unpack + wrapper script)

**Goal:** `dz install` produces a runnable, self-contained package via rootfs unpack plus wrapper script, *without* signature verification. This is the earliest end-to-end `dz install` we can run.

**Depends on:** Phase 2.

**Work:**

1.  Create `internal/shim/unpack.go`: pure-Go tar extraction for OCI layers with whiteout handling, using stdlib `archive/tar`.
2.  Create `internal/shim/entrypoint.go`: `Identify` — resolve `ENTRYPOINT[0]` inside the rootfs, validate it's an ELF (Linux host) or Mach-O (macOS host) matching the host arch. Uses stdlib `debug/elf` and `debug/macho`.
3.  Create `internal/shim/loader.go`: `FindLoader` — Linux-only; locate the bundled dynamic loader from well-known paths. No-op on macOS.
4.  Create `internal/shim/wrapper.go`: `Generate` — emit the POSIX wrapper script content based on host OS, loader presence, and entrypoint + baked args.
5.  Adapt `internal/hostintegration/` to write the generated wrapper to `~/.dropzone/bin/<name>` (instead of a symlink). Implement the dropzone-written marker so `RemoveWrapper` knows what it owns.
6.  Reimplement `packagehandler.InstallPackage` to chain: resolve → pull + unpack → identify → move rootfs into package dir → write wrapper → write metadata → flip `current` symlink. **Skip signature verification for this phase.** Log a clear warning on every install: "signature verification not yet enabled (Phase 4)."

**Exit criteria:** `dz install jq` against the pre-seeded Chainguard registry on Linux produces a shimmed `jq`; `jq --version` runs from the host shell. Repeat on macOS against a registry that ships `darwin/*` (or verify the "no matching platform" error path if no such image is available at test time). Smoke-test integration test covers the full flow on both OSes.

**Est. effort:** Medium. 2–3 days. The rootfs-unpack approach is substantially simpler than the closure walk it replaces.

---

## Phase 4 — Sigstore verification

**Goal:** Install fails closed on unverified images unless `--allow-unsigned` is passed. Security story complete.

**Depends on:** Phase 3.

**Work:**

1.  Add `github.com/sigstore/sigstore-go` as a dependency. Pure Go, no cgo.
2.  Create `internal/cosign/verifier.go` — builds a Sigstore verifier configured against the registry's policy (issuer + identity regex). Fetches the signature bundle via the OCI referrers API (with sidecar-tag fallback; see `internal/registry/bundle.go`) and runs verification in-process.
3.  Create `internal/cosign/policy.go` — policy struct + `ApplyTemplate` for `github` / `gitlab` / `chainguard` shortcuts.
4.  Create `internal/registry/bundle.go` — helper that fetches Sigstore bundles from the registry, trying the OCI 1.1 referrers API first, falling back to the `sha256-<digest>.sig` sidecar tag.
5.  Wire verification into `InstallPackage` between the digest fetch and the pull. Fail closed unless `--allow-unsigned` is passed AND the registry has no policy. Verification failures with a policy are never bypassable.
6.  Add the `--allow-unsigned` flag to `dz install`.
7.  Persist `signature_verified`, `signer_identity`, `issuer` in package metadata.

**Exit criteria:** Install from a mutated policy fails with a clear error and no on-disk side effects. Install from a registry with no policy fails without `--allow-unsigned`, succeeds with it (and records unsigned status). Chainguard default install prints signer identity on success. The `dz` binary remains pure-Go with no external tool dependencies.

**Est. effort:** Small–medium. 1–2 days. No external subprocess means the integration surface is smaller than shelling out to cosign would have been.

---

## Phase 5 — Attestation surfacing

**Goal:** Install output shows SBOM, provenance, and vulnerability-scan summaries when attestations are attached.

**Depends on:** Phase 4.

**Work:**

1.  Create `internal/cosign/attestations.go` with `FetchSBOM`, `FetchProvenance`, `FetchVulnScan`. All in-process via `sigstore-go` — attestations are DSSE envelopes in Sigstore bundles, same fetch path as signatures, different predicate type filter.
2.  Parse SPDX and CycloneDX SBOMs for component count; SLSA provenance v0.2 / v1 for builder identity; cosign vuln predicate for severity counts.
3.  Run the three fetches concurrently after signature verification with a bounded deadline. Failures (including "predicate type not attached") degrade the summary, don't block the install.
4.  Wire the summary into the install output format (see attestation_and_verification.md §5).
5.  Persist attestation summaries in metadata (not raw files, just the parsed summary).

**Exit criteria:** `dz install jq` against Chainguard prints an install summary matching the design doc example, including counts from the vuln scan.

**Est. effort:** Small. ~1 day. All the work is parsing in-toto statements.

---

## Phase 6 — Registry management commands

**Goal:** Users can add, list, and remove registries. Policy templates work. Search and tags commands work.

**Depends on:** Phase 5 (so the full trust story is active for any newly-added registry).

**Work:**

1.  Add the `dz add registry <name> <url> [flags]` command.
2.  Add `dz list registries`.
3.  Add `dz remove registry <name>` with the "installed packages from this registry" safety check (`--force` override).
4.  Add `dz search [<term>] [--registry <name>]` using `registry.Client.Catalog`. Handle `ErrCatalogUnavailable` with the fallback message.
5.  Add `dz tags <image> [--registry <name>]`.
6.  Fully qualified install refs (`dz install mycorp/jq:1.7.1`) that route to the right registry by config lookup.

**Exit criteria:** End-to-end flow on a non-Chainguard registry: add registry with a policy template, `dz search` degrades correctly if catalog disabled, `dz tags` works, `dz install mycorp/image` verifies and shims.

**Est. effort:** Medium. 2 days.

---

## Phase 7 — Update flow (the killer feature)

**Goal:** `dz update` detects same-tag rebuilds and newer tags against the live registry, applies updates atomically via the digest-as-directory layout.

**Depends on:** Phase 6.

**Work:**

1.  Create `internal/packagehandler/update.go` with `CheckUpdates` and `ApplyUpdate`.
2.  Implement the atomic update via symlink flip on `~/.dropzone/packages/<name>/current`. Old digest dir retained (to enable future rollback).
3.  Add attestation diff for same-tag rebuilds (compare stored vuln summary to the new digest's vuln attestation).
4.  Add `dz update`, `dz update <name>`, `dz update --all`, `--yes`, `--check`.
5.  Semver parsing + lexicographic fallback for "newer tag" detection.
6.  Parallel registry checks with a bounded worker pool. Per-package errors don't abort the whole run.

**Exit criteria:** Integration test: install `jq`, mutate metadata to simulate an older digest, `dz update` reports the same-tag rebuild with a before/after vuln diff, `dz update jq` applies.

**Est. effort:** Medium. 2–3 days. The tag-ordering and atomic-flip logic are the non-trivial bits.

---

## Phase 8 — Polish

**Goal:** The things that make the tool feel complete without adding surface area.

**Depends on:** Phase 7.

**Work (pick-and-choose, not all required for v0.1):**

*   `dz doctor` — checks `~/.dropzone/bin` on PATH, reconciles broken wrappers, missing `current` symlinks, and orphan package dirs.
*   `dz rollback <name>` — flip `current` symlink back to the previous digest dir. Essentially free with the layout.
*   `dz path` — prints the PATH export snippet for unsupported shells.
*   `dz list --json` for scripting.
*   Shell completion for bash / zsh.
*   Smoke-test option (`--smoke-test` on install) that runs `<binary> --version` after shim.
*   Error messages audit: every abort path has a concrete, actionable message.
*   `dz purge` — wipe `~/.dropzone/`.

**Est. effort:** Each item is small (hours to a day). Pick based on user feedback from dogfooding.

---

## Critical dependencies graph

```
Phase 0 (scope removal)
   │
   ▼
Phase 1 (config + registry manager skeleton)
   │
   ▼
Phase 2 (image pull)
   │
   ▼
Phase 3 (shim builder + unverified install)  ◄─── highest risk
   │
   ▼
Phase 4 (cosign verification)
   │
   ▼
Phase 5 (attestation surfacing)
   │
   ▼
Phase 6 (registry commands + fq installs)
   │
   ▼
Phase 7 (update flow)
   │
   ▼
Phase 8 (polish)
```

Phases 1–3 must run strictly sequentially. Phases 4 and 5 could swap if needed but 4-then-5 is the natural order (security first among those two). Phase 6 depends on 5 only so the full trust story is active for user-added registries.

## Milestones

*   **M1 (end of Phase 3):** `dz install jq` works end-to-end against Chainguard with a warning that signatures aren't verified yet. This is when dropzone is first usable by a human.
*   **M2 (end of Phase 5):** Security story complete. Install output is what it should be: signer, attestations, CVE summary.
*   **M3 (end of Phase 7):** The full registry-as-package-registry experience. Ship v0.1.

## Risks

1.  **Host-vs-rootfs boundary surprises.** Unpacking the rootfs fixes the dynamic library problem but doesn't fix the fact that binaries still read `/etc` from the host. Some tools will behave in ways users don't expect (CA bundles, locales, NSS). Mitigation: document limits clearly; add auto-set env-var hints (`SSL_CERT_FILE`, `LC_ALL`) as a low-effort post-MVP enhancement.
2.  **Chainguard Sigstore policy drift.** If Chainguard's signing identity changes, our pre-seeded default breaks. Mitigation: make the default policy easy to override via config, and treat the seed as versioned — future dropzone releases may ship updated defaults.
3.  **`/v2/_catalog` variance across registries.** Graceful degradation is designed in; the risk is underestimating how many registries disable the endpoint. Mitigation: `dz tags` is always the reliable path.
4.  **Sigstore bundle format fragmentation.** Some images are signed with the legacy `sha256-<digest>.sig` sidecar, others with the OCI 1.1 referrers API. We support both but must keep the fallback paths working as the ecosystem migrates. Mitigation: integration tests against a range of real images.
5.  **macOS image availability.** Chainguard and most hardened-image vendors ship Linux-only today. macOS is supported by dropzone as a host but the available software is correspondingly thinner. Mitigation: ship macOS support anyway; the value prop grows as the ecosystem does.
6.  **`sigstore-go` API surface.** Library is newer than `cosign`'s CLI and the public API is still evolving. Mitigation: pin a specific version; budget time in each minor upgrade for API adjustments.
7.  **go-containerregistry API surface.** We're depending on a library for multiple features. If it drops support for something we need, we have to pivot. Low risk; library is actively maintained.

## What "done" looks like for v0.1

A user runs:

```
dz install jq
```

They get:

*   A progress summary of pulling + verifying + shimming.
*   `jq --version` working from their shell.
*   A sense that this jq came from somewhere trustworthy: signer identity visible, vuln scan shown.

A week later, Chainguard patches a CVE in jq. The user runs:

```
dz update
```

And sees:

```
jq  1.7.1 (sha256:abc)  1.7.1 (sha256:def)  same-tag rebuild (2 high / 1 medium resolved)
```

They run `dz update jq`, and the new shimmed binary is in place. That's the v0.1 experience.
