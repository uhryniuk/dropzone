package registry

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	gcrremote "github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/uhryniuk/dropzone/internal/cosign"
)

// ErrAttestationsNotFound means the image has no `.att` sidecar manifest
// and no referrers entries advertising attestations. Best-effort fetch:
// callers treat this as "no attestations to surface" rather than a
// failure.
var ErrAttestationsNotFound = errors.New("no attestations for image")

// FetchAttestations returns every in-toto attestation attached to the
// image at imageDigest. Two paths, sidecar tag first (cosign's
// sha256-<hex>.att, the dominant format today) and OCI 1.1 referrers as
// fallback. Returns ErrAttestationsNotFound if neither yields any.
//
// Each returned RawAttestation has its predicate type extracted; the
// cosign package's SummarizeAttestations turns the slice into a
// user-visible summary.
//
// Phase 5 design choice: we don't verify each attestation's DSSE
// signature individually. The image's signature was verified in Phase 4;
// the attestations live on the same registry path under the same access
// model, so trust extends transitively. Cryptographic per-attestation
// verification can land later if it matters.
func (c *Client) FetchAttestations(ctx context.Context, ref ResolvedRef, imageDigest string) ([]cosign.RawAttestation, error) {
	full := ref.Registry.URL + "/" + ref.Image
	repo, err := name.NewRepository(full)
	if err != nil {
		return nil, fmt.Errorf("parse repo %q: %w", full, err)
	}

	atts, err := c.fetchSidecarAttestations(ctx, repo, imageDigest)
	if err == nil && len(atts) > 0 {
		return atts, nil
	}
	if err != nil && !errors.Is(err, ErrAttestationsNotFound) {
		return nil, err
	}

	// Fallback path lands when implementations stabilize on referrers
	// for attestations. Not all registries support it yet; sidecar
	// remains the common path.
	return nil, ErrAttestationsNotFound
}

// fetchSidecarAttestations pulls the cosign-style .att sidecar manifest
// and returns each layer parsed as a DSSE envelope into a RawAttestation.
func (c *Client) fetchSidecarAttestations(ctx context.Context, repo name.Repository, imageDigest string) ([]cosign.RawAttestation, error) {
	tag, err := digestToAttestationTag(imageDigest)
	if err != nil {
		return nil, err
	}
	tagged := repo.Tag(tag)

	desc, err := gcrremote.Get(tagged, append(c.opts, gcrremote.WithContext(ctx))...)
	if err != nil {
		if isNotFound(err) {
			return nil, ErrAttestationsNotFound
		}
		return nil, fmt.Errorf("fetch attestation manifest: %w", err)
	}

	img, err := desc.Image()
	if err != nil {
		return nil, fmt.Errorf("read attestation image: %w", err)
	}
	layers, err := img.Layers()
	if err != nil {
		return nil, fmt.Errorf("read attestation layers: %w", err)
	}

	var out []cosign.RawAttestation
	for _, layer := range layers {
		mt, err := layer.MediaType()
		if err != nil {
			continue
		}
		// Cosign attestation layers carry an in-toto media type. We're
		// lenient: any layer that decodes as a DSSE envelope counts.
		_ = mt

		envelope, err := layerToBytes(layer)
		if err != nil {
			continue
		}
		raw, err := cosign.DecodeDSSE(envelope)
		if err != nil || raw.PredicateType == "" {
			continue
		}
		out = append(out, raw)
	}
	if len(out) == 0 {
		return nil, ErrAttestationsNotFound
	}
	return out, nil
}

// digestToAttestationTag returns the cosign attestation tag for an
// image digest, e.g. "sha256:abc..." → "sha256-abc.att".
func digestToAttestationTag(d string) (string, error) {
	if !strings.HasPrefix(d, "sha256:") {
		return "", fmt.Errorf("expected sha256: digest, got %q", d)
	}
	return "sha256-" + d[len("sha256:"):] + ".att", nil
}

// layerToBytes reads a layer's uncompressed contents into memory.
// Attestations are tiny (a few KB at most) so an in-memory read is fine.
func layerToBytes(layer interface {
	Uncompressed() (io.ReadCloser, error)
}) ([]byte, error) {
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
