package registry

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"

	gcrremote "github.com/google/go-containerregistry/pkg/v1/remote"
)

// ErrBundleNotFound mirrors cosign.ErrBundleNotFound. Defined here so the
// registry package can return the sentinel without cross-importing cosign
// (which would cycle). Callers compare with errors.Is.
var ErrBundleNotFound = errors.New("no signature bundle for image")

// SignatureBundle is the raw JSON bytes of a Sigstore protobuf bundle,
// ready to hand to sigstore-go's bundle.UnmarshalJSON. We don't parse it
// here — the cosign package owns the verification logic and parsing.
type SignatureBundle []byte

// FetchBundle locates a Sigstore signature bundle for an image digest.
//
// Two paths, tried in order:
//
//  1. **Embedded bundle on the cosign sidecar tag.** Cosign publishes a
//     manifest at <repo>:sha256-<hex>.sig where each layer's
//     annotations carry `dev.sigstore.cosign/bundle` — the JSON bundle
//     bytes we want. This is what Chainguard's images use today.
//
//  2. **OCI 1.1 referrers API.** A registry that supports referrers
//     advertises Sigstore bundles as artifact-type
//     `application/vnd.dev.sigstore.bundle.v0.3+json`. The referrer
//     points at a blob containing the bundle bytes directly.
//
// Returns ErrBundleNotFound if neither path yields a bundle.
//
// We pick the sidecar path first because it's the dominant format
// today; the referrers path is the modern future and we keep it as a
// fallback so we work with both styles of registry.
func (c *Client) FetchBundle(ctx context.Context, ref ResolvedRef, imageDigest string) (SignatureBundle, error) {
	full := ref.Registry.URL + "/" + ref.Image
	repo, err := name.NewRepository(full)
	if err != nil {
		return nil, fmt.Errorf("parse repo %q: %w", full, err)
	}

	if b, err := c.fetchSidecarBundle(ctx, repo, imageDigest); err == nil {
		return b, nil
	} else if !errors.Is(err, ErrBundleNotFound) {
		return nil, err
	}

	if b, err := c.fetchReferrersBundle(ctx, repo, imageDigest); err == nil {
		return b, nil
	} else if !errors.Is(err, ErrBundleNotFound) {
		return nil, err
	}
	return nil, ErrBundleNotFound
}

// fetchSidecarBundle pulls the cosign-style sidecar tag and extracts a
// bundle from the layer annotations. Tag scheme: sha256-<hex>.sig.
func (c *Client) fetchSidecarBundle(ctx context.Context, repo name.Repository, imageDigest string) (SignatureBundle, error) {
	tag, err := digestToSidecarTag(imageDigest)
	if err != nil {
		return nil, err
	}
	tagged := repo.Tag(tag)

	desc, err := gcrremote.Get(tagged, append(c.opts, gcrremote.WithContext(ctx))...)
	if err != nil {
		if isNotFound(err) {
			return nil, ErrBundleNotFound
		}
		return nil, fmt.Errorf("fetch sidecar manifest: %w", err)
	}

	img, err := desc.Image()
	if err != nil {
		return nil, fmt.Errorf("read sidecar image: %w", err)
	}
	manifest, err := img.Manifest()
	if err != nil {
		return nil, fmt.Errorf("read sidecar manifest body: %w", err)
	}

	for _, layer := range manifest.Layers {
		// The bundle annotation is the modern, complete artifact: a full
		// Sigstore protobuf bundle in JSON form, including the cert,
		// signature, and Rekor proof. Older cosign signatures published
		// the cert + signature in separate annotations and we'd need to
		// reconstruct a bundle. For Phase 4 we accept only the
		// annotation form; reconstruction lands later if needed.
		bundleJSON, ok := layer.Annotations["dev.sigstore.cosign/bundle"]
		if !ok {
			continue
		}
		// Cosign sometimes base64-encodes annotations. Sniff for that
		// before assuming JSON.
		if looksLikeJSON(bundleJSON) {
			return []byte(bundleJSON), nil
		}
		decoded, err := base64.StdEncoding.DecodeString(bundleJSON)
		if err == nil && looksLikeJSON(string(decoded)) {
			return decoded, nil
		}
		return nil, fmt.Errorf("bundle annotation present but not parseable")
	}

	return nil, ErrBundleNotFound
}

// fetchReferrersBundle queries the OCI 1.1 referrers API for a Sigstore
// bundle. Newer registries advertise signatures here; older ones don't
// support the endpoint at all and return 404.
func (c *Client) fetchReferrersBundle(ctx context.Context, repo name.Repository, imageDigest string) (SignatureBundle, error) {
	digest, err := name.NewDigest(repo.String() + "@" + imageDigest)
	if err != nil {
		return nil, fmt.Errorf("parse digest: %w", err)
	}

	idx, err := gcrremote.Referrers(digest, append(c.opts, gcrremote.WithContext(ctx))...)
	if err != nil {
		if isNotFound(err) {
			return nil, ErrBundleNotFound
		}
		return nil, fmt.Errorf("query referrers: %w", err)
	}
	manifest, err := idx.IndexManifest()
	if err != nil {
		return nil, fmt.Errorf("read referrers index: %w", err)
	}

	for _, m := range manifest.Manifests {
		if !isSigstoreBundleMediaType(m.ArtifactType) {
			continue
		}
		// The referrer's manifest points at one or more blobs; for a
		// Sigstore bundle artifact, the first layer is the bundle JSON.
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
		rc, err := layers[0].Uncompressed()
		if err != nil {
			continue
		}
		buf := make([]byte, 0, 4096)
		tmp := make([]byte, 4096)
		for {
			n, err := rc.Read(tmp)
			buf = append(buf, tmp[:n]...)
			if err != nil {
				break
			}
		}
		_ = rc.Close()
		if looksLikeJSON(string(buf)) {
			return buf, nil
		}
	}
	return nil, ErrBundleNotFound
}

// digestToSidecarTag returns the cosign sidecar tag for an image digest,
// e.g. "sha256:abc..." → "sha256-abc.sig". Tags can't contain colons.
func digestToSidecarTag(d string) (string, error) {
	if !strings.HasPrefix(d, "sha256:") {
		return "", fmt.Errorf("expected sha256: digest, got %q", d)
	}
	return "sha256-" + d[len("sha256:"):] + ".sig", nil
}

// isSigstoreBundleMediaType reports whether an OCI artifact type belongs
// to Sigstore's bundle namespace. Cosign has cycled through several
// versions; we accept any v0.* of the bundle media type.
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
	// Some registries return MANIFEST_UNKNOWN as a body code with 404;
	// covered by transport.Error path above. Defensive default: don't
	// swallow non-404 errors as bundle-not-found.
	return false
}

// digestFromManifest is a small helper for tests that build manifests
// in-process and want to compute their digest the same way the registry
// does. Not used by production code.
func digestFromManifest(_ v1.Manifest, raw []byte) (string, error) {
	if !json.Valid(raw) {
		return "", errors.New("manifest is not valid JSON")
	}
	return "", errors.New("not implemented")
}
