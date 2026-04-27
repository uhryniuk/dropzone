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

import "errors"

// ErrSignatureInvalid is returned when verification fails. The wrapped
// error from sigstore-go carries the specific reason (cert chain bad,
// identity mismatch, Rekor entry missing, etc.).
var ErrSignatureInvalid = errors.New("signature verification failed")

// ErrBundleNotFound is returned when no signature bundle could be located
// for an image digest. Distinct from "signature invalid" so callers can
// surface it as "image is not signed" rather than "signature is bad".
var ErrBundleNotFound = errors.New("no signature bundle for image")
