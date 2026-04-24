// Package registry is dropzone's client for OCI distribution registries.
//
// The Manager owns the configured list of registries and answers catalog,
// tags, digest, and pull requests against them. The Client talks the /v2/
// API via google/go-containerregistry directly; the Cache layers short-TTL
// caching over catalog and tag responses.
//
// Phase 1 implements discovery (catalog, tags, digest) and the registry
// management surface. Pull is stubbed here and lands in Phase 2.
package registry

import "errors"

// Registry is the runtime representation of a configured registry.
type Registry struct {
	Name         string
	URL          string
	CosignPolicy *CosignPolicy
}

// CosignPolicy pins the Sigstore signer for a registry.
type CosignPolicy struct {
	Issuer        string
	IdentityRegex string
}

// ResolvedRef is the output of Manager.Resolve. It identifies the source
// registry, the image path within that registry, and the requested tag.
type ResolvedRef struct {
	Registry *Registry
	Image    string
	Tag      string
}

// FullReference returns the reference string suitable for passing to
// go-containerregistry (e.g., "cgr.dev/chainguard/jq:latest").
func (r ResolvedRef) FullReference() string {
	return r.Registry.URL + "/" + r.Image + ":" + r.Tag
}

// Errors returned from the registry package.
var (
	// ErrCatalogUnavailable means the registry responded to /v2/_catalog
	// with a status that indicates the endpoint is disabled or not
	// supported (404, 401, 405). The correct CLI response is to tell the
	// user to use `dz tags <image>` for a known image instead.
	ErrCatalogUnavailable = errors.New("registry does not expose /v2/_catalog")

	// ErrNotImplemented is returned by Pull in Phase 1. Phase 2 replaces
	// it with the real implementation.
	ErrNotImplemented = errors.New("not yet implemented")

	// ErrRegistryNotFound means a reference named a registry that is not
	// configured.
	ErrRegistryNotFound = errors.New("registry not configured")

	// ErrEmptyRef is returned by Manager.Resolve for an empty string.
	ErrEmptyRef = errors.New("empty reference")
)
