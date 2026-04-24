# Feature: Update Flow

## 1. Overview

`dz update` is the feature that makes the registry feel like a real registry. For each installed package, it queries the source registry's tag list and current-digest-for-tag, compares against what's installed, and reports two kinds of update:

*   **Same tag, new digest** — the tag has been rebuilt upstream. Typical cause: a CVE-patch roll of `:latest`, `:3.7`, etc. This is the case generic package managers miss.
*   **New tag available** — a semantically newer version exists. The user decides whether to move.

On confirmation, `dz update` re-runs the install flow (with verification and shim rebuild) and atomically replaces the old package directory.

Digest-pinned tracking is what makes this work. At install time, `dz install` records the resolved digest in metadata; `dz update` asks the registry "what digest does this tag point to *now*?" and compares.

## 2. Goals

*   **Catch silent rebuilds.** A CVE-patched `latest` is the whole point of consuming Chainguard images; `dz update` must surface it.
*   **Keep the upgrade path user-gated.** No automatic updates in MVP. The command reports what's available and applies on confirmation.
*   **One pass for all installed packages.** `dz update` with no arg checks everything; `dz update <name>` scopes to one.
*   **Fail partial-safe.** If the registry is unreachable for one package, the others still get checked.

## 3. Components

### 3.1. `internal/packagehandler/update.go`

*   **`CheckUpdates(ctx) ([]UpdateCandidate, error)`** — per-package scan, returns:

```go
type UpdateCandidate struct {
    Name           string
    InstalledTag   string
    InstalledDigest string

    // Populated if the installed tag's current digest differs from installed
    SameTagRebuild *TagRebuild

    // Populated if newer tags exist on the registry
    NewerTags      []string
}

type TagRebuild struct {
    NewDigest       string
    AttestationDiff *AttestationDiff // optional, from re-fetching attestations
}
```

*   **`ApplyUpdate(ctx, name string, target UpdateTarget) error`** — re-runs the install flow for the target ref, unshims the old package, atomically replaces the package directory (or promotes a new digest-dir inside the package if we adopt the digest-as-dir layout).

### 3.2. Registry interactions

Reuses `registry.Client`:

*   `Tags(ctx, r, image)` for the "newer tag available" detection.
*   `Digest(ctx, r, image, tag)` to resolve the installed tag's current digest.
*   `Pull(...)` and everything downstream to apply.

Tag-ordering for "newer" detection is pragmatic:

*   If all tags parse as semver, use semver comparison.
*   Otherwise, string-sort and report any tag lexicographically greater than the installed one. Crude but usable for `YYYY-MM-DD`-style tags and monotonic version strings.
*   Tags like `latest` are never reported as "newer" — only their digest-rebuild is relevant.

### 3.3. Attestation diff (optional output)

On a same-tag rebuild, we re-fetch the new digest's vulnerability-scan attestation and compare to the stored one:

```
jq (sha256:abc → sha256:def, same tag 1.7.1)
  Vulnerabilities: 0C/2H/5M/7L → 0C/0H/2M/7L
  3 high / 3 medium resolved since your install
```

This is the killer UX for "why should I update?" MVP should include it; the parsing is cheap.

## 4. CLI integration

### `dz update` (no arg)

Checks every installed package. Prints a summary table:

```
PACKAGE     INSTALLED           AVAILABLE                  REASON
jq          1.7.1 (sha256:abc)  1.7.1 (sha256:def)         same-tag rebuild (CVE patches)
ripgrep     13.0.0              14.0.0, 14.0.1              newer tag
yq          4.35.1              up to date                  —
kubectl     <unreachable>       —                          registry error: 503
```

Footer: `3 updates available. Run 'dz update <name>' to apply, or 'dz update --all' to apply all.`

### `dz update <name>`

Applies the update for one package. Prompts for confirmation (unless `--yes`). Under the hood, runs the full install flow for the new ref, then atomically replaces.

### `dz update --all`

Applies all available updates. Confirms once up front with the full list.

### Flags

*   `--yes` / `-y` — skip confirmations.
*   `--check` — report only; don't apply even if named.
*   `--allow-unsigned` — required if any candidate's source registry has no policy. Refused otherwise.

## 5. Atomicity

Two options, covered in install_flow.md §8 open questions:

*   **(a) Tag-as-directory (`packages/jq/1.7.1/`):** on update, create `packages/jq/1.7.1.new/`, do the shim build, then rename over. The window where the symlink points at a half-written file is minimized but non-zero.
*   **(c) Digest-as-directory with tag symlink:** `packages/jq/sha256-def.../` next to `packages/jq/sha256-abc.../`, and `packages/jq/current` is a symlink to the active digest. Updates create a new digest dir, then flip the symlink. Rollback is a symlink flip. This is the intended MVP choice.

With (c), `HostIntegrator` symlinks into `~/.dropzone/bin/jq` → `~/.dropzone/packages/jq/current/bin/jq`. Updates flip `current`; bin-level symlinks never need to change.

## 6. Testing

### 6.1. Unit

*   `CheckUpdates` with a mock registry client:
    *   Same-tag + same-digest → no update.
    *   Same-tag + different-digest → `SameTagRebuild` populated.
    *   New semver-higher tag → `NewerTags` populated.
    *   Unreachable registry → error per-package, other packages still scanned.
*   Tag-ordering logic for semver, date, and lexicographic fallback.
*   Attestation diff formatting.

### 6.2. Integration

*   Install `jq:latest`. Mutate the local metadata to record a fake older digest. Run `dz update`, verify it reports a same-tag rebuild. Apply, verify the new digest is recorded.
*   Install `jq:1.7.0` (or similar older tag). Run `dz update`, verify it reports `1.7.1` as a newer tag. Apply, verify the package is re-shimmed at the new tag and the old digest-dir is removed (or retained for rollback if we ship that).
*   Install from a registry, take the registry offline, run `dz update`, verify the one package reports the error while a second installed-from-a-reachable-registry package reports normally.

## 7. Technical details

*   **Concurrency:** registry checks run in parallel with a bounded worker pool (e.g., 4). Sequential apply.
*   **Metadata write on apply:** the new metadata file is written *before* the symlink flip, so a crash mid-apply leaves the new package dir and metadata but the `current` symlink still points at the old digest. `dz update --resume` could reconcile; not MVP.
*   **Attestation re-fetch:** runs in parallel with the digest check. Cached for the duration of the `dz update` invocation so `--all` doesn't re-hit cosign per package.

## 8. Open questions

*   **Auto-update hooks.** A `dz update --all --yes` in `cron` is plausible and tempting but dangerous (rolls without user attention). Out of MVP; reference it as a caution in docs.
*   **Rollback command.** With the digest-as-directory layout, rollback is "flip the symlink to the previous digest dir." Worth shipping as `dz rollback <name>` in MVP — cost is tiny.
*   **Pruning old digest directories.** After an update, we have two digest dirs. Keep N most recent? Prune immediately? Leaning keep-one-previous to enable rollback.
*   **`--notify-only`** mode that just produces the table and exits — useful for shell prompts or status lines. Trivial post-MVP.
