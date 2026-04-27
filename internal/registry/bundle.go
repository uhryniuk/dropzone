package registry

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	gcrremote "github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
)

// ErrBundleNotFound mirrors cosign.ErrBundleNotFound. Defined here so the
// registry package can return the sentinel without cross-importing cosign
// (which would cycle). Callers compare with errors.Is.
var ErrBundleNotFound = errors.New("no signature bundle for image")

// SignatureBundle carries either the modern Sigstore protobuf bundle as
// raw JSON, or the parts of a legacy cosign signature manifest (cert,
// signature, Rekor inclusion proof, signed payload). The cosign verifier
// converts the legacy form into a protobundle internally so sigstore-go
// can verify it through one code path.
//
// Exactly one of Modern or Legacy is populated per call.
type SignatureBundle struct {
	// Modern is a Sigstore protobuf bundle in JSON form, parseable by
	// bundle.UnmarshalJSON in sigstore-go. Set when the registry stored
	// a v0.* bundle directly in the layer annotation or via the OCI 1.1
	// referrers API.
	Modern []byte
	// Legacy carries the four pieces of a cosign legacy signature: the
	// Rekor inclusion proof, the base64 signature, the PEM certificate
	// chain, and the bytes the signature was computed over (the cosign
	// "simple signing" payload, which sits in the sidecar manifest's
	// layer blob). Chainguard ships this format today.
	Legacy *LegacyBundle
}

// LegacyBundle is the cosign legacy signature in its raw form. The
// cosign package converts this into a Sigstore protobuf bundle for
// verification, plus checks the simple-signing payload's image digest
// matches the user-requested digest (a binding check the protobuf
// bundle verification path doesn't do for us in this shape).
type LegacyBundle struct {
	// Bundle is the JSON of the cosign Rekor bundle annotation
	// (`dev.sigstore.cosign/bundle`). Top-level `SignedEntryTimestamp`
	// is the giveaway.
	Bundle []byte
	// SignatureB64 is the layer signature, base64-encoded
	// (`dev.cosignproject.cosign/signature`).
	SignatureB64 string
	// CertificatePEM is the PEM-encoded signing cert chain
	// (`dev.sigstore.cosign/certificate`).
	CertificatePEM string
	// Payload is the layer's blob: the cosign "simple signing" JSON
	// document that the signature was computed over. Includes the
	// `critical.image.docker-manifest-digest` we use for binding.
	Payload []byte
}

// FetchBundle locates a Sigstore signature bundle for an image digest.
//
// Sidecar tag first (cosign's `sha256-<hex>.sig`, the dominant format
// today), referrers API as fallback. Returns ErrBundleNotFound when
// neither yields anything.
func (c *Client) FetchBundle(ctx context.Context, ref ResolvedRef, imageDigest string) (SignatureBundle, error) {
	full := ref.Registry.URL + "/" + ref.Image
	repo, err := name.NewRepository(full)
	if err != nil {
		return SignatureBundle{}, fmt.Errorf("parse repo %q: %w", full, err)
	}

	if b, err := c.fetchSidecarBundle(ctx, repo, imageDigest); err == nil {
		return b, nil
	} else if !errors.Is(err, ErrBundleNotFound) {
		return SignatureBundle{}, err
	}

	if b, err := c.fetchReferrersBundle(ctx, repo, imageDigest); err == nil {
		return b, nil
	} else if !errors.Is(err, ErrBundleNotFound) {
		return SignatureBundle{}, err
	}
	return SignatureBundle{}, ErrBundleNotFound
}

// fetchSidecarBundle pulls the cosign sidecar manifest at
// sha256-<hex>.sig and extracts whichever bundle format is present.
func (c *Client) fetchSidecarBundle(ctx context.Context, repo name.Repository, imageDigest string) (SignatureBundle, error) {
	tag, err := digestToSidecarTag(imageDigest)
	if err != nil {
		return SignatureBundle{}, err
	}
	tagged := repo.Tag(tag)

	desc, err := gcrremote.Get(tagged, append(c.opts, gcrremote.WithContext(ctx))...)
	if err != nil {
		if isNotFound(err) {
			return SignatureBundle{}, ErrBundleNotFound
		}
		return SignatureBundle{}, fmt.Errorf("fetch sidecar manifest: %w", err)
	}
	img, err := desc.Image()
	if err != nil {
		return SignatureBundle{}, fmt.Errorf("read sidecar image: %w", err)
	}
	manifest, err := img.Manifest()
	if err != nil {
		return SignatureBundle{}, fmt.Errorf("read sidecar manifest body: %w", err)
	}
	layers, err := img.Layers()
	if err != nil {
		return SignatureBundle{}, fmt.Errorf("read sidecar layers: %w", err)
	}

	// Layers and manifest.Layers share an index; pair them up so we can
	// reach the layer's blob alongside the annotations on the same
	// manifest entry.
	for i, layerDesc := range manifest.Layers {
		bundleJSON, ok := layerDesc.Annotations["dev.sigstore.cosign/bundle"]
		if !ok {
			continue
		}
		bundleBytes, err := decodeAnnotation(bundleJSON)
		if err != nil {
			return SignatureBundle{}, fmt.Errorf("decode bundle annotation: %w", err)
		}

		if !looksLikeLegacyBundle(bundleBytes) {
			return SignatureBundle{Modern: bundleBytes}, nil
		}

		// Legacy: also collect the signature, cert, and payload.
		legacy := &LegacyBundle{Bundle: bundleBytes}
		if sig, ok := layerDesc.Annotations["dev.cosignproject.cosign/signature"]; ok {
			legacy.SignatureB64 = sig
		}
		if cert, ok := layerDesc.Annotations["dev.sigstore.cosign/certificate"]; ok {
			legacy.CertificatePEM = cert
		}
		if i < len(layers) {
			payload, err := readLayerBytes(layers[i])
			if err != nil {
				return SignatureBundle{}, fmt.Errorf("read sidecar layer payload: %w", err)
			}
			legacy.Payload = payload
		}
		if legacy.SignatureB64 == "" || legacy.CertificatePEM == "" || len(legacy.Payload) == 0 {
			return SignatureBundle{}, fmt.Errorf("legacy cosign signature is missing required parts")
		}
		return SignatureBundle{Legacy: legacy}, nil
	}
	return SignatureBundle{}, ErrBundleNotFound
}

// fetchReferrersBundle queries the OCI 1.1 referrers API for a Sigstore
// bundle. Newer registries advertise signatures here.
func (c *Client) fetchReferrersBundle(ctx context.Context, repo name.Repository, imageDigest string) (SignatureBundle, error) {
	digest, err := name.NewDigest(repo.String() + "@" + imageDigest)
	if err != nil {
		return SignatureBundle{}, fmt.Errorf("parse digest: %w", err)
	}

	idx, err := gcrremote.Referrers(digest, append(c.opts, gcrremote.WithContext(ctx))...)
	if err != nil {
		if isNotFound(err) {
			return SignatureBundle{}, ErrBundleNotFound
		}
		return SignatureBundle{}, fmt.Errorf("query referrers: %w", err)
	}
	manifest, err := idx.IndexManifest()
	if err != nil {
		return SignatureBundle{}, fmt.Errorf("read referrers index: %w", err)
	}

	for _, m := range manifest.Manifests {
		if !isSigstoreBundleMediaType(m.ArtifactType) {
			continue
		}
		bundleRef, err := name.NewDigest(repo.String() + "@" + m.Digest.String())
		if err != nil {
			continue
		}
		bundleDesc, err := gcrremote.Get(bundleRef, append(c.opts, gcrremote.WithContext(ctx))...)
		if err != nil {
			continue
		}
		img, err := bundleDesc.Image()
		if err != nil {
			continue
		}
		layers, err := img.Layers()
		if err != nil || len(layers) == 0 {
			continue
		}
		buf, err := readLayerBytes(layers[0])
		if err != nil {
			continue
		}
		if looksLikeJSON(string(buf)) {
			return SignatureBundle{Modern: buf}, nil
		}
	}
	return SignatureBundle{}, ErrBundleNotFound
}

// decodeAnnotation handles the two shapes cosign uses for the bundle
// annotation: either raw JSON or a base64-wrapped JSON blob.
func decodeAnnotation(s string) ([]byte, error) {
	if looksLikeJSON(s) {
		return []byte(s), nil
	}
	decoded, err := base64.StdEncoding.DecodeString(s)
	if err == nil && looksLikeJSON(string(decoded)) {
		return decoded, nil
	}
	return nil, fmt.Errorf("annotation is neither JSON nor base64-encoded JSON")
}

// readLayerBytes pulls the uncompressed layer content into memory.
// Cosign signature payloads are tiny (low single-digit KB).
func readLayerBytes(layer v1.Layer) ([]byte, error) {
	rc, err := layer.Uncompressed()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, rc); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// looksLikeLegacyBundle sniffs whether bundleJSON is the cosign legacy
// RekorBundle shape rather than a Sigstore protobuf bundle. The
// distinguishing field is the capitalized `SignedEntryTimestamp` at top
// level; protobuf bundles never carry it.
func looksLikeLegacyBundle(bundleJSON []byte) bool {
	var probe struct {
		SignedEntryTimestamp string `json:"SignedEntryTimestamp"`
	}
	if err := json.Unmarshal(bundleJSON, &probe); err != nil {
		return false
	}
	return probe.SignedEntryTimestamp != ""
}

// digestToSidecarTag returns the cosign sidecar tag for an image digest
// (e.g., "sha256:abc..." becomes "sha256-abc.sig"). Tags can't contain
// colons.
func digestToSidecarTag(d string) (string, error) {
	if !strings.HasPrefix(d, "sha256:") {
		return "", fmt.Errorf("expected sha256: digest, got %q", d)
	}
	return "sha256-" + d[len("sha256:"):] + ".sig", nil
}

// isSigstoreBundleMediaType reports whether an OCI artifact type belongs
// to Sigstore's bundle namespace. We accept any v0.* of the bundle
// media type; the spec hasn't broken backwards compatibility yet.
func isSigstoreBundleMediaType(mt string) bool {
	return strings.HasPrefix(mt, "application/vnd.dev.sigstore.bundle")
}

// looksLikeJSON is a fast pre-parse check: a Sigstore bundle JSON object
// always starts with '{'. Cheaper than a full unmarshal when we just
// need to choose between base64 and raw forms.
func looksLikeJSON(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			continue
		}
		return c == '{'
	}
	return false
}

// isNotFound matches the various 404-shaped responses we want to treat
// as "no signature here, try the other path".
func isNotFound(err error) bool {
	var te *transport.Error
	if errors.As(err, &te) {
		return te.StatusCode == 404
	}
	return false
}
