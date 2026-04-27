package registry

import (
	"context"
	"fmt"
	"strings"

	"github.com/uhryniuk/dropzone/internal/config"
)

// Manager owns the configured list of registries and routes discovery
// operations through the Client, layering the Cache over catalog and tag
// responses.
//
// Writes through Manager.Add / Remove mutate the underlying *config.Config
// and invoke the provided save callback so the change is persisted
// atomically from the caller's perspective.
type Manager struct {
	cfg    *config.Config
	save   func() error
	client *Client
	cache  *Cache
}

// NewManager wires together a config, a save callback, a Client, and a
// Cache. Any of client / cache may be nil for tests that don't exercise
// network or cache paths.
func NewManager(cfg *config.Config, save func() error, client *Client, cache *Cache) *Manager {
	return &Manager{cfg: cfg, save: save, client: client, cache: cache}
}

// Client returns the underlying registry client. Exposed so callers like
// packagehandler can drive Sigstore-bundle fetches that need the same
// transport and auth as everything else; not part of the everyday surface.
func (m *Manager) Client() *Client {
	return m.client
}

// List returns a copy of all configured registries.
func (m *Manager) List() []*Registry {
	out := make([]*Registry, 0, len(m.cfg.Registries))
	for i := range m.cfg.Registries {
		out = append(out, toRuntime(&m.cfg.Registries[i]))
	}
	return out
}

// Get returns the registry with the given name, or ErrRegistryNotFound.
func (m *Manager) Get(name string) (*Registry, error) {
	rc, ok := m.cfg.FindRegistry(name)
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrRegistryNotFound, name)
	}
	return toRuntime(rc), nil
}

// Default returns the registry named by config.DefaultRegistry, or error
// if that entry is missing or no default is configured.
func (m *Manager) Default() (*Registry, error) {
	if m.cfg.DefaultRegistry == "" {
		return nil, fmt.Errorf("no default registry configured")
	}
	return m.Get(m.cfg.DefaultRegistry)
}

// Add registers a new registry. Returns an error if the name is already in
// use or required fields are missing. Persists via the save callback.
func (m *Manager) Add(r Registry) error {
	if r.Name == "" {
		return fmt.Errorf("registry name is required")
	}
	if r.URL == "" {
		return fmt.Errorf("registry url is required")
	}
	if _, exists := m.cfg.FindRegistry(r.Name); exists {
		return fmt.Errorf("registry %q already exists", r.Name)
	}
	m.cfg.UpsertRegistry(toConfig(&r))
	return m.persist()
}

// Remove drops a registry by name. Returns an error if the name is not
// configured. Persists via the save callback.
func (m *Manager) Remove(name string) error {
	if !m.cfg.RemoveRegistry(name) {
		return fmt.Errorf("%w: %q", ErrRegistryNotFound, name)
	}
	return m.persist()
}

// Resolve expands a user-typed reference into a ResolvedRef carrying the
// source registry, image path, and tag.
//
// Accepted forms:
//
//	jq                                         → default registry, image "jq", tag "latest"
//	jq:1.7.1                                   → default registry, image "jq", tag "1.7.1"
//	chainguard/jq                              → registry "chainguard", image "jq"
//	chainguard/jq:1.7.1                        → registry "chainguard", image "jq", tag "1.7.1"
//	chainguard/private/tool:dev                → registry "chainguard", image "private/tool"
//	gitea.example.com/owner/repo:tag           → ephemeral registry "gitea.example.com",
//	                                             image "owner/repo", tag "tag"
//	localhost:5000/foo:latest                  → ephemeral registry "localhost:5000",
//	                                             image "foo", tag "latest"
//
// The first segment is interpreted as a configured registry name when it
// has neither a dot nor a colon (e.g., "chainguard"). Otherwise it's
// treated as a hostname-shaped ephemeral registry URL: the install can
// proceed without `dz add registry` first, but because no cosign policy
// is attached, the install requires `--allow-unsigned` (or the
// always_allow_unsigned config option).
//
// Unknown configured-name first segments → ErrRegistryNotFound.
// Empty ref → ErrEmptyRef.
func (m *Manager) Resolve(ref string) (*ResolvedRef, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, ErrEmptyRef
	}

	image, tag := splitTag(ref)

	// Short form: no "/" → use default registry.
	if !strings.Contains(image, "/") {
		def, err := m.Default()
		if err != nil {
			return nil, err
		}
		return &ResolvedRef{Registry: def, Image: image, Tag: tag}, nil
	}

	firstSlash := strings.Index(image, "/")
	head := image[:firstSlash]
	rest := image[firstSlash+1:]

	// Hostname-shaped first segment (contains a dot or a colon) means
	// the user gave us a fully-qualified URL. Materialize an ephemeral
	// Registry rather than requiring a prior `dz add registry`.
	if strings.ContainsAny(head, ".:") {
		return &ResolvedRef{
			Registry: &Registry{Name: head, URL: head},
			Image:    rest,
			Tag:      tag,
		}, nil
	}

	// Plain first segment: configured registry name lookup.
	reg, err := m.Get(head)
	if err != nil {
		return nil, err
	}
	return &ResolvedRef{Registry: reg, Image: rest, Tag: tag}, nil
}

// Catalog fetches repositories from the registry, caching the result.
// Passing forceRefresh=true skips the cache read but still writes the
// response back on success.
func (m *Manager) Catalog(ctx context.Context, regName string, forceRefresh bool) ([]string, error) {
	reg, err := m.Get(regName)
	if err != nil {
		return nil, err
	}
	if m.cache != nil && !forceRefresh {
		if cached, hit, err := m.cache.GetCatalog(regName); err == nil && hit {
			return cached, nil
		}
	}
	names, err := m.client.Catalog(ctx, reg)
	if err != nil {
		return nil, err
	}
	if m.cache != nil {
		_ = m.cache.PutCatalog(regName, names)
	}
	return names, nil
}

// Tags fetches tags for an image within a registry, caching the result.
func (m *Manager) Tags(ctx context.Context, regName, image string, forceRefresh bool) ([]string, error) {
	reg, err := m.Get(regName)
	if err != nil {
		return nil, err
	}
	if m.cache != nil && !forceRefresh {
		if cached, hit, err := m.cache.GetTags(regName, image); err == nil && hit {
			return cached, nil
		}
	}
	tags, err := m.client.Tags(ctx, reg, image)
	if err != nil {
		return nil, err
	}
	if m.cache != nil {
		_ = m.cache.PutTags(regName, image, tags)
	}
	return tags, nil
}

// Digest resolves an image+tag to its current digest. Always bypasses the
// cache — digest freshness is the reason `dz update` exists.
func (m *Manager) Digest(ctx context.Context, regName, image, tag string) (string, error) {
	reg, err := m.Get(regName)
	if err != nil {
		return "", err
	}
	return m.client.Digest(ctx, reg, image, tag)
}

// Pull is Phase 2 work; wired here for discoverability and stubbed via the
// Client layer for now.
func (m *Manager) Pull(ctx context.Context, ref *ResolvedRef, stagingDir string) (*ImageInfo, error) {
	return m.client.Pull(ctx, ref, stagingDir)
}

func (m *Manager) persist() error {
	if m.save == nil {
		return nil
	}
	return m.save()
}

// splitTag separates "name:tag" into (name, tag). Empty tag defaults to
// "latest". Handles absent colons and tolerates a colon inside a digest
// spec ("image@sha256:abc") by preferring the last ':' only when it sits
// after a '/' or at the start.
func splitTag(ref string) (string, string) {
	// Digest form: leave it alone (caller currently doesn't use it but
	// ResolvedRef.FullReference should still emit a valid string).
	if at := strings.Index(ref, "@"); at >= 0 {
		return ref[:at], ref[at+1:]
	}
	colon := strings.LastIndex(ref, ":")
	slash := strings.LastIndex(ref, "/")
	if colon <= slash || colon == -1 {
		return ref, "latest"
	}
	return ref[:colon], ref[colon+1:]
}

func toRuntime(c *config.RegistryConfig) *Registry {
	r := &Registry{Name: c.Name, URL: c.URL}
	if c.CosignPolicy != nil {
		r.CosignPolicy = &CosignPolicy{
			Issuer:        c.CosignPolicy.Issuer,
			IdentityRegex: c.CosignPolicy.IdentityRegex,
		}
	}
	return r
}

func toConfig(r *Registry) config.RegistryConfig {
	out := config.RegistryConfig{Name: r.Name, URL: r.URL}
	if r.CosignPolicy != nil {
		out.CosignPolicy = &config.CosignPolicy{
			Issuer:        r.CosignPolicy.Issuer,
			IdentityRegex: r.CosignPolicy.IdentityRegex,
		}
	}
	return out
}
