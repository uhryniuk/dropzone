package registry

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

// DefaultCacheTTL is the default staleness window for catalog and tag
// entries. dz update ignores the cache and forces a refresh.
const DefaultCacheTTL = time.Hour

// Cache stores catalog and tag responses per registry under baseDir.
//
// Layout on disk:
//
//	<baseDir>/<registry-name>/catalog.json
//	<baseDir>/<registry-name>/tags/<escaped-image>.json
//
// Image names are URL-path-escaped to flatten the "/" that naturally appears
// in OCI image names (e.g., "chainguard-images/jq" → "chainguard-images%2Fjq").
type Cache struct {
	baseDir string
	ttl     time.Duration
	now     func() time.Time // injectable clock for tests
}

// NewCache builds a Cache rooted at baseDir with the given TTL.
func NewCache(baseDir string, ttl time.Duration) *Cache {
	if ttl <= 0 {
		ttl = DefaultCacheTTL
	}
	return &Cache{
		baseDir: baseDir,
		ttl:     ttl,
		now:     time.Now,
	}
}

type cachedEntry struct {
	StoredAt time.Time       `json:"stored_at"`
	Data     json.RawMessage `json:"data"`
}

func (c *Cache) registryDir(registry string) string {
	return filepath.Join(c.baseDir, registry)
}

func (c *Cache) catalogPath(registry string) string {
	return filepath.Join(c.registryDir(registry), "catalog.json")
}

func (c *Cache) tagsPath(registry, image string) string {
	return filepath.Join(c.registryDir(registry), "tags", url.PathEscape(image)+".json")
}

// GetCatalog returns (value, hit, err). A miss returns (nil, false, nil).
// A stale entry returns (nil, false, nil) and is treated as a miss.
func (c *Cache) GetCatalog(registry string) ([]string, bool, error) {
	var out []string
	hit, err := c.read(c.catalogPath(registry), &out)
	if err != nil {
		return nil, false, err
	}
	if !hit {
		return nil, false, nil
	}
	return out, true, nil
}

// PutCatalog writes a catalog response to disk with a fresh timestamp.
func (c *Cache) PutCatalog(registry string, names []string) error {
	return c.write(c.catalogPath(registry), names)
}

// GetTags returns (value, hit, err) for a single image under registry.
func (c *Cache) GetTags(registry, image string) ([]string, bool, error) {
	var out []string
	hit, err := c.read(c.tagsPath(registry, image), &out)
	if err != nil {
		return nil, false, err
	}
	if !hit {
		return nil, false, nil
	}
	return out, true, nil
}

// PutTags writes a tags response to disk with a fresh timestamp.
func (c *Cache) PutTags(registry, image string, tags []string) error {
	return c.write(c.tagsPath(registry, image), tags)
}

// Clear removes all cached entries for a registry.
func (c *Cache) Clear(registry string) error {
	return os.RemoveAll(c.registryDir(registry))
}

func (c *Cache) read(path string, into any) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("read cache: %w", err)
	}
	var entry cachedEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		// Corrupt cache entry is treated as a miss, not an error — the caller
		// just re-fetches. Delete it opportunistically.
		_ = os.Remove(path)
		return false, nil
	}
	if c.now().Sub(entry.StoredAt) > c.ttl {
		return false, nil
	}
	if err := json.Unmarshal(entry.Data, into); err != nil {
		return false, fmt.Errorf("decode cache payload: %w", err)
	}
	return true, nil
}

func (c *Cache) write(path string, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode cache payload: %w", err)
	}
	entry := cachedEntry{StoredAt: c.now(), Data: payload}
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("encode cache entry: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create cache dir: %w", err)
	}
	return os.WriteFile(path, data, 0o644)
}
