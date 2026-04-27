# Feature: Attestation and Verification

## 1. Overview

Every `dz install` verifies the source image's Sigstore signature against a per-registry policy before extracting anything. Keyless verification. Failure closes the install unless the user passes `--allow-unsigned`, in which case the install proceeds and is recorded as unsigned.

Post-signature, dropzone fetches available attestations (SBOM, SLSA provenance, vulnerability scan) and surfaces a summary in the install output. Attestations are informational for MVP, not gating.

All verification is done in-process via `github.com/sigstore/sigstore-go`. There is no external `cosign` binary dependency, the dropzone binary itself ships statically.

## 2. Goals

*   **Fail closed by default.** Unsigned or policy-mismatched images refuse to install. No silent success.
*   **Per-registry policy.** Trust is scoped to the registry the image came from, not global.
*   **Surface, don't gate, rich attestations.** Users see SBOM / provenance / CVE summaries without having to dig for them. Gating on attestation content (e.g., "no criticals") is a future policy-language feature.
*   **No external tools.** Pure-Go verification via `sigstore-go`. Dropzone stays a single static binary.

## 3. Components

### 3.1. `internal/cosign/verifier.go`

*   **`Verifier`**, constructed with a `CosignPolicy` and a `sigstore-go` verifier instance (configured with the Sigstore public-good trust root by default; overridable for private Sigstore instances post-MVP).
*   **`Verify(ctx, imageRef name.Reference, digest v1.Hash) (*VerificationResult, error)`**:
    1.  Fetch the Sigstore bundle for the image digest. Bundles may be stored:
        *   As an OCI artifact in the same registry, referenced via the OCI 1.1 referrers API (preferred, modern).
        *   As a sidecar tag following the `sha256-<digest>.sig` convention (legacy, still common).

        The `registry.Client` exposes both fetch paths and tries referrers first, falls back to the sidecar tag.
    2.  Pass the bundle to `sigstore-go`'s verifier configured with a policy that requires:
        *   Certificate identity regex matching `policy.IdentityRegex`.
        *   Certificate OIDC issuer equal to `policy.Issuer`.
        *   A valid Rekor inclusion proof (Sigstore verifier enforces this by default).
    3.  On success, extract the signing identity from the certificate subject and return it.
    4.  On failure, return a typed `ErrSignatureInvalid` with the verification failure reason.

*   **`VerificationResult`**, `{SignerIdentity, Issuer, Digest, VerifiedAt}`. Persisted into package metadata.

### 3.2. `internal/cosign/attestations.go`

Attestation fetch runs after signature verification. All failures here are non-fatal; they degrade the install summary but don't block the install.

In-toto attestations ship in the same Sigstore bundle format (a DSSE envelope with a signed in-toto statement). `sigstore-go` can verify and unwrap these directly. The attestation predicate type determines what we're looking at.

*   **`FetchSBOM(ctx, imageRef, digest) (*SBOMSummary, error)`**, fetch attestations for the digest, pick the first with predicate type `https://spdx.dev/Document` or `https://cyclonedx.org/bom`. Parse the SBOM JSON, return `{Format, ComponentCount}`.
*   **`FetchProvenance(ctx, imageRef, digest) (*ProvenanceSummary, error)`**, find the attestation with predicate type `https://slsa.dev/provenance/v1` (or v0.2), extract `{BuilderID, BuildType, InvocationID}`.
*   **`FetchVulnScan(ctx, imageRef, digest) (*VulnSummary, error)`**, find the attestation with predicate type for vulnerability scans (e.g., `https://cosign.sigstore.dev/attestation/vuln/v1`), parse for `{CriticalCount, HighCount, MediumCount, LowCount, ScannedAt}`.

If a predicate type isn't present, the fetcher returns a typed `ErrAttestationNotAvailable`. The CLI surfaces this as a one-liner ("vuln scan: not available") rather than treating it as a failure.

### 3.3. `internal/cosign/policy.go`

*   **`CosignPolicy`** mirrors the config type. Validation: both fields non-empty.
*   **`ApplyTemplate(name string) CosignPolicy`**, returns the base policy for `github`, `gitlab`, `chainguard`. The caller fills in `identity_regex` for non-Chainguard templates.

### 3.4. `internal/registry/bundle.go`

Helper for fetching Sigstore bundles from the registry. Two paths, tried in order:

1.  **Referrers API** (OCI 1.1): `GET /v2/<name>/referrers/<digest>` filtered by the Sigstore artifact type. If the registry supports the API, this is the clean path.
2.  **Sidecar tag**: resolve `sha256-<digest-hex>.sig` as a tag on the same image repository. Legacy but universally supported.

Returns the bundle bytes or an `ErrBundleNotFound` if neither path yields one. `ErrBundleNotFound` surfaces to the verifier as an unsigned image.

## 4. Install-time flow

In `packagehandler.InstallPackage`, after resolving the digest and fetching the image config:

1.  Look up the source registry's policy.
2.  If policy is nil:
    *   If `--allow-unsigned` was passed: skip verification, mark `SignatureVerified: false` in metadata, print a prominent warning.
    *   Otherwise: abort with `"registry '<name>' has no cosign policy configured. Add one with 'dz add registry --identity-issuer ...' or pass --allow-unsigned."`.
3.  If policy is set: call `Verifier.Verify(ref, digest)`. On failure, abort with the verification reason attached. Do not fall through to `--allow-unsigned`; that flag is for *missing policies*, not failed verifications.
4.  On success, fetch SBOM / provenance / vulnerability scan concurrently. Collect into an `Attestations` struct on the install result.

## 5. Install-time output

Example output for a successful Chainguard install:

```
✓ Signature verified
  Signed by: https://github.com/chainguard-images/images/.github/workflows/release.yaml@refs/heads/main
  Issuer:    https://token.actions.githubusercontent.com
  Attestations:
    SBOM:        SPDX (142 components)
    Provenance:  github-actions/chainguard-images/images
    Vuln scan:   0 critical / 0 high / 2 medium / 7 low (scanned 2026-04-22)
```

Example for `--allow-unsigned`:

```
⚠ Installing unsigned image (--allow-unsigned)
  Registry:  mycorp (no cosign policy configured)
  This install has no cryptographic provenance. Installed as signed=no.
```

## 6. Metadata persistence

Each installed package's `metadata.json` records:

```json
{
  "signature_verified": true,
  "signer_identity": "...",
  "issuer": "...",
  "verified_at": "2026-04-23T14:32:11Z",
  "attestations": {
    "sbom": { "format": "spdx", "component_count": 142 },
    "provenance": { "builder_id": "..." },
    "vuln_scan": { "critical": 0, "high": 0, "medium": 2, "low": 7 }
  }
}
```

`dz list` surfaces the `signature_verified` flag.

## 7. Testing

### 7.1. Unit

*   Policy template expansion.
*   Bundle fetch: mock a registry serving a Sigstore bundle via (a) referrers API and (b) sidecar tag. Verify both paths work and preference ordering is correct.
*   Verifier against a canned Sigstore bundle with known identity; mutate the policy to test identity-regex mismatch, issuer mismatch, expired certificate paths.
*   Attestation fetchers with canned in-toto statements covering SPDX SBOM, SLSA provenance, and cosign vuln predicate formats.

### 7.2. Integration

*   Verify a real Chainguard image (`cgr.dev/chainguard/jq:latest`). Expect success and Chainguard-identity in the result.
*   Verify with a mutated policy (wrong identity regex). Expect failure.
*   Install a registry image with no policy and `--allow-unsigned`. Verify the unsigned warning appears and metadata records `signature_verified: false`.
*   Install with no policy and no `--allow-unsigned`. Verify the abort message is clear.

## 8. Technical details

*   **Library:** `github.com/sigstore/sigstore-go` (not the larger `sigstore/cosign` module, which carries heavier dependencies). `sigstore-go` is explicitly designed for embedding and is pure Go.
*   **Trust root:** Sigstore public-good TUF root, loaded at startup. The library handles TUF updates; we don't vendor the root statically. A `--sigstore-root` flag (post-MVP) could point at a private TUF root for enterprise Sigstore deployments.
*   **Concurrency:** signature verify is serial (blocking the install). Attestation fetches run in parallel with a short deadline (~5s total); timeouts degrade the summary rather than failing the install.
*   **Offline behavior:** Sigstore verification requires network for TUF refresh + Rekor log checks. Offline installs fail at verification unless `--allow-unsigned`. An `--offline` mode post-MVP could trust a previously-cached TUF root and skip Rekor, but this weakens the trust story and isn't MVP.
*   **Static build:** `CGO_ENABLED=0 go build` produces a self-contained binary. `sigstore-go`, `go-containerregistry`, and the stdlib `debug/elf` are all pure Go.

## 9. Open questions

*   **Key-based cosign signatures.** Some registries use a key pair instead of keyless. Out of MVP; policy schema would need a `public_key:` field and the verifier path would skip Fulcio / Rekor checks.
*   **Policy language for gating on attestations.** "Refuse if vuln_scan.critical > 0" is a clear next step but requires a policy DSL. Deferred.
*   **Rekor transparency log entry display.** Surfacing the Rekor entry UUID in the install output is trivial, `sigstore-go` already has it after verification. Include in MVP.
*   **Private Sigstore instances.** Enterprises sometimes run their own Fulcio + Rekor. `sigstore-go` supports pointing at a custom TUF root. Post-MVP via config, probably per-registry.
