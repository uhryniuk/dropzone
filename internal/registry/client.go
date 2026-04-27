package registry

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	gcrremote "github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
	"github.com/uhryniuk/dropzone/internal/util"
)

// Client is a thin wrapper over google/go-containerregistry providing the
// OCI distribution operations dropzone needs. Stateless — safe to share.
//
// Authentication uses a chained keychain: dropzone's own ~/.dropzone/auth.json
// is checked first, and misses fall through to authn.DefaultKeychain which
// reads ~/.docker/config.json and any Docker credential helpers the user
// already has configured. Users can choose either path (or both).
type Client struct {
	// opts lets tests inject a custom RoundTripper via WithTransport.
	opts []gcrremote.Option
}

// NewClient builds a Client whose keychain reads credentials from
// authFilePath first, then from the Docker default keychain. An empty
// authFilePath disables the dropzone tier; tests pass "" so they only
// exercise anonymous access against their local test registries.
func NewClient(authFilePath string) *Client {
	return &Client{
		opts: []gcrremote.Option{
			gcrremote.WithAuthFromKeychain(NewChainedKeychain(authFilePath)),
		},
	}
}

// WithTransport replaces the default HTTP transport on the Client. Intended
// for test doubles (httptest.Server) — production code should leave it alone.
func (c *Client) WithTransport(rt http.RoundTripper) *Client {
	newOpts := append([]gcrremote.Option(nil), c.opts...)
	newOpts = append(newOpts, gcrremote.WithTransport(rt))
	return &Client{opts: newOpts}
}

// Catalog lists repositories advertised by the registry's /v2/_catalog
// endpoint. Registries that disable the endpoint (404, 401, or 405) return
// ErrCatalogUnavailable so the CLI can surface a clear "use `dz tags`
// instead" message.
func (c *Client) Catalog(ctx context.Context, r *Registry) ([]string, error) {
	reg, err := name.NewRegistry(registryHost(r.URL))
	if err != nil {
		return nil, fmt.Errorf("invalid registry URL %q: %w", r.URL, err)
	}

	names, err := gcrremote.Catalog(ctx, reg, c.opts...)
	if err != nil {
		if isCatalogUnavailable(err) {
			return nil, ErrCatalogUnavailable
		}
		return nil, fmt.Errorf("catalog %s: %w", r.Name, err)
	}

	// If the registry's URL includes a namespace prefix (e.g.,
	// "docker.io/chainguard"), filter and strip it so the returned names
	// are relative to the registry as configured.
	prefix := namespacePrefix(r.URL)
	if prefix == "" {
		return names, nil
	}
	var out []string
	for _, n := range names {
		if strings.HasPrefix(n, prefix+"/") {
			out = append(out, strings.TrimPrefix(n, prefix+"/"))
		}
	}
	return out, nil
}

// Tags lists tags for an image within a registry.
func (c *Client) Tags(ctx context.Context, r *Registry, image string) ([]string, error) {
	ref := r.URL + "/" + image
	repo, err := name.NewRepository(ref)
	if err != nil {
		return nil, fmt.Errorf("invalid repository %q: %w", ref, err)
	}
	tags, err := gcrremote.List(repo, append(c.opts, gcrremote.WithContext(ctx))...)
	if err != nil {
		return nil, fmt.Errorf("list tags for %s: %w", ref, err)
	}
	return tags, nil
}

// Digest resolves an image+tag to its current digest.
func (c *Client) Digest(ctx context.Context, r *Registry, image, tag string) (string, error) {
	full := r.URL + "/" + image + ":" + tag
	ref, err := name.ParseReference(full)
	if err != nil {
		return "", fmt.Errorf("invalid reference %q: %w", full, err)
	}
	desc, err := gcrremote.Get(ref, append(c.opts, gcrremote.WithContext(ctx))...)
	if err != nil {
		return "", fmt.Errorf("get manifest for %s: %w", full, err)
	}
	return desc.Digest.String(), nil
}

// ImageInfo is the subset of image metadata dropzone needs post-pull.
type ImageInfo struct {
	// Digest is the resolved sha256 of the image manifest. For a manifest
	// list this is the digest of the platform-specific entry that matched
	// the host, not the index digest.
	Digest string
	// Entrypoint is the image config's Entrypoint[]. Empty if the image
	// declared none.
	Entrypoint []string
	// Platform is the "os/arch" of the pulled image. Always matches the
	// host platform by construction.
	Platform string
}

// ErrNoMatchingPlatform means the manifest list did not contain an entry
// matching the host's OS and architecture. The error message names the
// platforms the image did offer so users know what went wrong.
var ErrNoMatchingPlatform = errors.New("image has no matching platform for host")

// Pull fetches an image for the host platform, flattens its layers into
// stagingDir as a full rootfs, and returns the image's digest + entrypoint.
//
// Manifest list behavior: inspect the list, select the entry matching
// runtime.GOOS + runtime.GOARCH, pull that specific image. If no entry
// matches, return ErrNoMatchingPlatform with the offered platforms listed.
//
// Single-manifest behavior: verify the image's declared OS/arch matches
// the host; pull directly. Mismatch is reported through the same error.
//
// Layer extraction uses go-containerregistry's mutate.Extract, which
// applies overlayfs-style whiteouts. The result is a flat filesystem the
// shim builder can mark as the package's rootfs without further processing.
//
// On failure, partial files may remain under stagingDir. Callers are
// expected to own that directory and clean it up on error; Pull does not.
func (c *Client) Pull(ctx context.Context, ref *ResolvedRef, stagingDir string) (*ImageInfo, error) {
	if err := util.SupportedPlatform(); err != nil {
		return nil, err
	}

	full := ref.FullReference()
	parsed, err := name.ParseReference(full)
	if err != nil {
		return nil, fmt.Errorf("parse reference %q: %w", full, err)
	}

	opts := append([]gcrremote.Option(nil), c.opts...)
	opts = append(opts, gcrremote.WithContext(ctx))

	desc, err := gcrremote.Get(parsed, opts...)
	if err != nil {
		return nil, fmt.Errorf("fetch descriptor %s: %w", full, err)
	}

	img, platform, err := c.resolveImage(desc, opts)
	if err != nil {
		return nil, err
	}

	rc := mutate.Extract(img)
	defer rc.Close()
	if err := extractTar(rc, stagingDir); err != nil {
		return nil, fmt.Errorf("extract rootfs: %w", err)
	}

	digest, err := img.Digest()
	if err != nil {
		return nil, fmt.Errorf("image digest: %w", err)
	}
	cfg, err := img.ConfigFile()
	if err != nil {
		return nil, fmt.Errorf("image config: %w", err)
	}

	return &ImageInfo{
		Digest:     digest.String(),
		Entrypoint: cfg.Config.Entrypoint,
		Platform:   platform,
	}, nil
}

// resolveImage turns a descriptor (which may be a manifest list or a
// single-platform image) into a v1.Image matching the host platform, along
// with the platform string we selected.
func (c *Client) resolveImage(desc *gcrremote.Descriptor, opts []gcrremote.Option) (v1.Image, string, error) {
	hostOS, hostArch := util.HostOS(), util.HostArch()

	if desc.MediaType.IsIndex() {
		idx, err := desc.ImageIndex()
		if err != nil {
			return nil, "", fmt.Errorf("read manifest list: %w", err)
		}
		manifest, err := idx.IndexManifest()
		if err != nil {
			return nil, "", fmt.Errorf("read manifest list body: %w", err)
		}

		var (
			match     *v1.Hash
			available []string
		)
		for i := range manifest.Manifests {
			m := manifest.Manifests[i]
			if m.Platform == nil {
				continue
			}
			// Skip attestation-only entries (Docker/BuildKit emits
			// "unknown/unknown" attestation manifests alongside real
			// images). These are not installable rootfs images.
			if m.Platform.OS == "unknown" || m.Platform.Architecture == "unknown" {
				continue
			}
			available = append(available, m.Platform.OS+"/"+m.Platform.Architecture)
			if m.Platform.OS == hostOS && m.Platform.Architecture == hostArch {
				d := m.Digest
				match = &d
				break
			}
		}
		if match == nil {
			return nil, "", fmt.Errorf("%w: host %s/%s, image offers: %s",
				ErrNoMatchingPlatform, hostOS, hostArch, strings.Join(available, ", "))
		}
		img, err := idx.Image(*match)
		if err != nil {
			return nil, "", fmt.Errorf("fetch platform image: %w", err)
		}
		return img, hostOS + "/" + hostArch, nil
	}

	// Single-platform image: verify platform and return.
	img, err := desc.Image()
	if err != nil {
		return nil, "", fmt.Errorf("read image: %w", err)
	}
	cfg, err := img.ConfigFile()
	if err != nil {
		return nil, "", fmt.Errorf("read image config: %w", err)
	}
	if cfg.OS != hostOS || cfg.Architecture != hostArch {
		return nil, "", fmt.Errorf("%w: host %s/%s, image is %s/%s",
			ErrNoMatchingPlatform, hostOS, hostArch, cfg.OS, cfg.Architecture)
	}
	return img, cfg.OS + "/" + cfg.Architecture, nil
}

// registryHost strips any namespace suffix from a URL (docker.io/chainguard → docker.io).
func registryHost(url string) string {
	if i := strings.Index(url, "/"); i >= 0 {
		return url[:i]
	}
	return url
}

// namespacePrefix returns the portion of the URL after the host, or empty.
func namespacePrefix(url string) string {
	if i := strings.Index(url, "/"); i >= 0 {
		return strings.TrimSuffix(url[i+1:], "/")
	}
	return ""
}

// isCatalogUnavailable maps registry responses that mean "no catalog here"
// to a single sentinel. 404 is the common modern answer (Docker Hub, GHCR),
// 401/403 happen on registries that gate the endpoint behind a scope the
// default token doesn't have, and 405 shows up on some proxy setups.
func isCatalogUnavailable(err error) bool {
	var te *transport.Error
	if errors.As(err, &te) {
		switch te.StatusCode {
		case http.StatusNotFound, http.StatusUnauthorized, http.StatusForbidden, http.StatusMethodNotAllowed:
			return true
		}
	}
	return false
}
