// Package cosign verifies Sigstore-signed OCI images against a per-registry
// policy. Verification runs in-process via github.com/sigstore/sigstore-go;
// no external cosign binary is required.
//
// The two pieces are:
//
//   - Policy: the {issuer, identity_regex} pin attached to a registry,
//     plus template helpers for common providers.
//   - Verifier: takes a Policy and a fetched Sigstore bundle, runs
//     verification, returns the signing identity for display + storage.
//
// Bundle fetch lives next door in internal/registry (so go-containerregistry
// is the single OCI client) and is plumbed through this package via a
// small interface.
package cosign

import (
	"errors"
	"time"
)

// ErrSignatureInvalid is returned when verification fails. The wrapped
// error from sigstore-go carries the specific reason (cert chain bad,
// identity mismatch, Rekor entry missing, etc.).
var ErrSignatureInvalid = errors.New("signature verification failed")

// ErrBundleNotFound is returned when no signature bundle could be located
// for an image digest. Distinct from "signature invalid" so callers can
// surface it as "image is not signed" rather than "signature is bad".
var ErrBundleNotFound = errors.New("no signature bundle for image")

// Attestations is the user-visible summary of in-toto attestations
// attached to a verified image. Phase 5 surfaces these for trust signal
// without enforcing them as policy; Phase 6+ may add gating ("refuse if
// criticals > 0", etc.).
//
// Fields are pointers so absent attestations are distinguishable from
// "attestation present but empty" and the metadata.json output stays
// terse via omitempty.
type Attestations struct {
	SBOM       *SBOMSummary       `json:"sbom,omitempty"`
	Provenance *ProvenanceSummary `json:"provenance,omitempty"`
	VulnScan   *VulnSummary       `json:"vuln_scan,omitempty"`
}

// SBOMSummary captures what we extract from an SPDX or CycloneDX SBOM
// attestation: the format and the rough size of the bill of materials.
// We don't echo the SBOM itself; users who want the full doc can pull
// the attestation manifest directly with cosign or skopeo.
type SBOMSummary struct {
	Format         string `json:"format"` // "spdx" or "cyclonedx"
	ComponentCount int    `json:"component_count"`
}

// ProvenanceSummary captures a SLSA provenance attestation's signal:
// who built it, how, and which run.
type ProvenanceSummary struct {
	BuilderID    string `json:"builder_id,omitempty"`
	BuildType    string `json:"build_type,omitempty"`
	InvocationID string `json:"invocation_id,omitempty"`
}

// VulnSummary captures the cosign vulnerability-scan attestation's
// counts. Severity buckets follow the standard CVSS-style labels.
type VulnSummary struct {
	Critical  int       `json:"critical"`
	High      int       `json:"high"`
	Medium    int       `json:"medium"`
	Low       int       `json:"low"`
	ScannedAt time.Time `json:"scanned_at,omitzero"`
}

// HasAny reports whether at least one attestation summary was extracted.
// Used by callers to decide whether to render the attestations block at
// all in install output.
func (a *Attestations) HasAny() bool {
	if a == nil {
		return false
	}
	return a.SBOM != nil || a.Provenance != nil || a.VulnScan != nil
}
