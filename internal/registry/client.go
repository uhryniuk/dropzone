package registry

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	gcrremote "github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
)

// Client is a thin wrapper over google/go-containerregistry providing the
// OCI distribution operations dropzone needs. Stateless — safe to share.
//
// Authentication is delegated to the user's Docker credential helpers via
// authn.DefaultKeychain, so we never store credentials ourselves.
type Client struct {
	// opts lets tests inject a custom RoundTripper via WithTransport.
	opts []gcrremote.Option
}

// NewClient builds a Client configured with the user's Docker keychain.
func NewClient() *Client {
	return &Client{
		opts: []gcrremote.Option{
			gcrremote.WithAuthFromKeychain(authn.DefaultKeychain),
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
	// "cgr.dev/chainguard"), filter and strip it so the returned names are
	// relative to the registry as configured.
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
// Populated by Pull in Phase 2.
type ImageInfo struct {
	Digest     string
	Entrypoint []string
}

// Pull is the Phase-2 operation: fetch the manifest, select the platform
// entry for the host, flatten layers into stagingDir, return the image
// config. Stubbed in Phase 1.
func (c *Client) Pull(ctx context.Context, ref *ResolvedRef, stagingDir string) (*ImageInfo, error) {
	return nil, ErrNotImplemented
}

// registryHost strips any namespace suffix from a URL (cgr.dev/chainguard → cgr.dev).
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
