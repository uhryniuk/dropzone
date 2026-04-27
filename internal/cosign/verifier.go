package cosign

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/tuf"
	"github.com/sigstore/sigstore-go/pkg/verify"
)

// Verifier wraps sigstore-go for image-bundle verification against a Policy.
//
// Lazily loads the Sigstore public-good TUF root on first use so processes
// that never verify (e.g., `dz install --allow-unsigned`) don't pay the
// network cost of TUF setup. The trusted root is cached on the Verifier
// for the life of the process; a long-running daemon would want to
// refresh periodically, but `dz` is a short-lived CLI so a single fetch
// per invocation is fine.
type Verifier struct {
	once     sync.Once
	verifier *verify.Verifier
	loadErr  error
}

// NewVerifier returns a Verifier ready to verify bundles. The actual TUF
// + trusted-root setup happens on first Verify() call.
func NewVerifier() *Verifier {
	return &Verifier{}
}

// Result captures the verified identity for display and storage in the
// installed package's metadata.json. Empty when Verify failed.
type Result struct {
	// Signer is the certificate's SAN (the signing identity, e.g.,
	// "https://github.com/chainguard-images/images/.github/workflows/...@refs/...").
	Signer string
	// Issuer is the OIDC issuer that minted the signing identity.
	Issuer string
}

// Verify checks a Sigstore protobuf bundle JSON against policy.
// Returns a Result on success.
//
//   - bundleJSON: raw JSON of a Sigstore protobuf bundle.
//   - imageDigest: resolved image digest (e.g. "sha256:abc..."), used
//     as the artifact digest for verification.
//   - policy: must be Validate()-able and pre-populated.
//
// For cosign legacy signatures (the format Chainguard ships today),
// callers should use VerifyLegacy instead. The legacy path constructs
// a protobundle from the four annotations and runs sigstore-go on it.
func (v *Verifier) Verify(bundleJSON []byte, imageDigest string, policy Policy) (*Result, error) {
	if err := policy.Validate(); err != nil {
		return nil, fmt.Errorf("invalid policy: %w", err)
	}
	digestBytes, err := decodeDigest(imageDigest)
	if err != nil {
		return nil, fmt.Errorf("parse image digest: %w", err)
	}
	b := &bundle.Bundle{}
	if err := b.UnmarshalJSON(bundleJSON); err != nil {
		return nil, fmt.Errorf("parse bundle: %w", err)
	}
	if err := v.ensureLoaded(); err != nil {
		return nil, err
	}

	identity, err := verify.NewShortCertificateIdentity(policy.Issuer, "", "", policy.IdentityRegex)
	if err != nil {
		return nil, fmt.Errorf("build certificate identity: %w", err)
	}
	pb := verify.NewPolicy(
		verify.WithArtifactDigest("sha256", digestBytes),
		verify.WithCertificateIdentity(identity),
	)

	res, err := v.verifier.Verify(b, pb)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrSignatureInvalid, err)
	}
	return signerFromResult(res), nil
}

// VerifyLegacy verifies a cosign legacy signature (annotation-based
// sidecar with `SignedEntryTimestamp` in the bundle annotation).
//
// The image-digest binding (the simple-signing payload's
// `critical.image.docker-manifest-digest`) is checked inside
// buildLegacyBundle before sigstore-go even sees the bundle. The
// signature itself is then verified by sigstore-go via WithArtifact,
// which hashes the supplied payload bytes and confirms they match the
// MessageDigest the bundle carries.
func (v *Verifier) VerifyLegacy(parts LegacyParts, imageDigest string, policy Policy) (*Result, error) {
	if err := policy.Validate(); err != nil {
		return nil, fmt.Errorf("invalid policy: %w", err)
	}

	pb, payload, err := buildLegacyBundle(parts, imageDigest)
	if err != nil {
		return nil, err
	}
	b, err := bundle.NewBundle(pb)
	if err != nil {
		return nil, fmt.Errorf("wrap legacy bundle: %w", err)
	}
	if err := v.ensureLoaded(); err != nil {
		return nil, err
	}

	identity, err := verify.NewShortCertificateIdentity(policy.Issuer, "", "", policy.IdentityRegex)
	if err != nil {
		return nil, fmt.Errorf("build certificate identity: %w", err)
	}
	policyBuilder := verify.NewPolicy(
		verify.WithArtifact(bytes.NewReader(payload)),
		verify.WithCertificateIdentity(identity),
	)

	res, err := v.verifier.Verify(b, policyBuilder)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrSignatureInvalid, err)
	}
	return signerFromResult(res), nil
}

// signerFromResult turns sigstore-go's verification result into our
// trimmed Result. Used by both Verify and VerifyLegacy.
func signerFromResult(res *verify.VerificationResult) *Result {
	out := &Result{}
	if res != nil && res.Signature != nil && res.Signature.Certificate != nil {
		out.Signer = res.Signature.Certificate.SubjectAlternativeName
		out.Issuer = res.Signature.Certificate.Issuer
	}
	return out
}

// ensureLoaded sets up the Sigstore public-good trusted root once per
// process, behind a sync.Once.
func (v *Verifier) ensureLoaded() error {
	v.once.Do(func() {
		liveRoot, err := root.NewLiveTrustedRoot(tuf.DefaultOptions())
		if err != nil {
			v.loadErr = fmt.Errorf("load Sigstore trusted root: %w", err)
			return
		}
		ver, err := verify.NewVerifier(liveRoot,
			verify.WithSignedCertificateTimestamps(1),
			verify.WithTransparencyLog(1),
			verify.WithObserverTimestamps(1),
		)
		if err != nil {
			v.loadErr = fmt.Errorf("build sigstore verifier: %w", err)
			return
		}
		v.verifier = ver
	})
	return v.loadErr
}

// decodeDigest accepts "sha256:hex" and returns the raw byte form needed
// by sigstore-go's WithArtifactDigest.
func decodeDigest(d string) ([]byte, error) {
	const algo = "sha256:"
	if !strings.HasPrefix(d, algo) {
		return nil, errors.New("only sha256 digests are supported")
	}
	hex := d[len(algo):]
	if len(hex) != 64 {
		return nil, fmt.Errorf("expected 64 hex chars, got %d", len(hex))
	}
	out := make([]byte, 32)
	for i := 0; i < 32; i++ {
		hi, ok1 := hexNibble(hex[2*i])
		lo, ok2 := hexNibble(hex[2*i+1])
		if !ok1 || !ok2 {
			return nil, fmt.Errorf("invalid hex digest at offset %d", 2*i)
		}
		out[i] = (hi << 4) | lo
	}
	return out, nil
}

func hexNibble(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}
