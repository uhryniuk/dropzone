package cosign

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"

	protobundle "github.com/sigstore/protobuf-specs/gen/pb-go/bundle/v1"
	protocommon "github.com/sigstore/protobuf-specs/gen/pb-go/common/v1"
	prototlog "github.com/sigstore/protobuf-specs/gen/pb-go/rekor/v1"
)

// LegacyParts is the cosign legacy signature in its raw form. Mirrors
// registry.LegacyBundle so cosign doesn't have to import registry.
//
// The cosign verifier converts these parts into a Sigstore protobuf
// bundle and runs sigstore-go verification on the result.
type LegacyParts struct {
	Bundle         []byte // dev.sigstore.cosign/bundle (Rekor inclusion proof)
	SignatureB64   string // dev.cosignproject.cosign/signature
	CertificatePEM string // dev.sigstore.cosign/certificate
	Payload        []byte // simple-signing JSON, the bytes the signature was computed over
}

// rekorBundleAnnotation is the cosign legacy bundle JSON shape. Capital
// field names are intentional, that's what cosign wrote.
type rekorBundleAnnotation struct {
	SignedEntryTimestamp string `json:"SignedEntryTimestamp"`
	Payload              struct {
		Body           string `json:"body"`
		IntegratedTime int64  `json:"integratedTime"`
		LogIndex       int64  `json:"logIndex"`
		LogID          string `json:"logID"`
	} `json:"Payload"`
}

// simpleSigningPayload is the cosign-signed payload that lives in the
// sidecar manifest's layer. The image digest binding lives in
// `critical.image.docker-manifest-digest`.
type simpleSigningPayload struct {
	Critical struct {
		Identity struct {
			DockerReference string `json:"docker-reference"`
		} `json:"identity"`
		Image struct {
			DockerManifestDigest string `json:"docker-manifest-digest"`
		} `json:"image"`
		Type string `json:"type"`
	} `json:"critical"`
}

// rekorBodyHeader pulls just the kind + apiVersion off a base64-decoded
// Rekor entry body. The full entry has more fields but we only need
// these to populate the protobundle's KindVersion.
type rekorBodyHeader struct {
	Kind       string `json:"kind"`
	APIVersion string `json:"apiVersion"`
}

// buildLegacyBundle assembles a Sigstore protobuf bundle from the four
// pieces of a cosign legacy signature, plus runs the image-digest
// binding check (the simple-signing payload's
// `critical.image.docker-manifest-digest` must equal expectedImageDigest).
//
// Returns the bundle and the artifact bytes the verifier should hash
// (the simple-signing payload). The verifier needs the artifact, not
// the image digest, because the protobuf bundle's MessageDigest is
// `sha256(payload)` rather than `sha256(image manifest)`. The image
// digest binding is checked here, before the bundle is handed to
// sigstore-go.
func buildLegacyBundle(parts LegacyParts, expectedImageDigest string) (*protobundle.Bundle, []byte, error) {
	if len(parts.Bundle) == 0 || parts.SignatureB64 == "" || parts.CertificatePEM == "" || len(parts.Payload) == 0 {
		return nil, nil, errors.New("legacy bundle is missing required parts")
	}

	// Binding check: confirm the signed payload references the image
	// the user wanted to install. Without this, anyone could attach a
	// valid signature for a different image to the sidecar tag.
	var ssp simpleSigningPayload
	if err := json.Unmarshal(parts.Payload, &ssp); err != nil {
		return nil, nil, fmt.Errorf("parse simple-signing payload: %w", err)
	}
	if ssp.Critical.Image.DockerManifestDigest == "" {
		return nil, nil, errors.New("simple-signing payload missing docker-manifest-digest")
	}
	if ssp.Critical.Image.DockerManifestDigest != expectedImageDigest {
		return nil, nil, fmt.Errorf(
			"signed payload binds to %s, expected %s",
			ssp.Critical.Image.DockerManifestDigest, expectedImageDigest,
		)
	}

	// Parse the cert (first PEM block; chains beyond the leaf are
	// re-derivable from Fulcio's intermediates).
	block, _ := pem.Decode([]byte(parts.CertificatePEM))
	if block == nil {
		return nil, nil, errors.New("certificate PEM has no block")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("parse certificate: %w", err)
	}

	sigBytes, err := base64.StdEncoding.DecodeString(parts.SignatureB64)
	if err != nil {
		return nil, nil, fmt.Errorf("decode signature: %w", err)
	}

	var rb rekorBundleAnnotation
	if err := json.Unmarshal(parts.Bundle, &rb); err != nil {
		return nil, nil, fmt.Errorf("parse rekor bundle: %w", err)
	}
	setBytes, err := base64.StdEncoding.DecodeString(rb.SignedEntryTimestamp)
	if err != nil {
		return nil, nil, fmt.Errorf("decode SET: %w", err)
	}
	body, err := base64.StdEncoding.DecodeString(rb.Payload.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("decode rekor body: %w", err)
	}
	logID, err := hex.DecodeString(rb.Payload.LogID)
	if err != nil {
		return nil, nil, fmt.Errorf("decode log id: %w", err)
	}

	// Pick kind/version off the body so we don't hard-code "hashedrekord
	// v0.0.1" for entry types that aren't that.
	kind := "hashedrekord"
	version := "0.0.1"
	var hdr rekorBodyHeader
	if err := json.Unmarshal(body, &hdr); err == nil {
		if hdr.Kind != "" {
			kind = hdr.Kind
		}
		if hdr.APIVersion != "" {
			version = hdr.APIVersion
		}
	}

	// MessageDigest is sha256 of the simple-signing payload (the bytes
	// the signature was computed over). The verifier confirms this
	// matches the artifact we hand it.
	sum := sha256.Sum256(parts.Payload)

	// v0.1 media type because that's the version whose validation
	// accepts an inclusion promise (Signed Entry Timestamp) without an
	// inclusion proof. v0.2+ requires the proof, which cosign legacy
	// signatures don't carry. The SET alone is what cosign-legacy +
	// Rekor have always supplied.
	bundle := &protobundle.Bundle{
		MediaType: "application/vnd.dev.sigstore.bundle+json;version=0.1",
		VerificationMaterial: &protobundle.VerificationMaterial{
			Content: &protobundle.VerificationMaterial_Certificate{
				Certificate: &protocommon.X509Certificate{RawBytes: cert.Raw},
			},
			TlogEntries: []*prototlog.TransparencyLogEntry{
				{
					LogIndex:       rb.Payload.LogIndex,
					LogId:          &protocommon.LogId{KeyId: logID},
					IntegratedTime: rb.Payload.IntegratedTime,
					InclusionPromise: &prototlog.InclusionPromise{
						SignedEntryTimestamp: setBytes,
					},
					CanonicalizedBody: body,
					KindVersion: &prototlog.KindVersion{
						Kind:    kind,
						Version: version,
					},
				},
			},
		},
		Content: &protobundle.Bundle_MessageSignature{
			MessageSignature: &protocommon.MessageSignature{
				MessageDigest: &protocommon.HashOutput{
					Algorithm: protocommon.HashAlgorithm_SHA2_256,
					Digest:    sum[:],
				},
				Signature: sigBytes,
			},
		},
	}
	return bundle, parts.Payload, nil
}
