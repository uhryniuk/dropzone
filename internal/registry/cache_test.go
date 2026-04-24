package registry

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCacheCatalogMissHitAndStale(t *testing.T) {
	tmpDir := t.TempDir()
	c := NewCache(tmpDir, 1*time.Hour)

	// Miss
	v, hit, err := c.GetCatalog("r")
	if err != nil {
		t.Fatalf("GetCatalog miss: %v", err)
	}
	if hit || v != nil {
		t.Errorf("expected miss, got hit=%v v=%v", hit, v)
	}

	// Put + hit
	if err := c.PutCatalog("r", []string{"a", "b"}); err != nil {
		t.Fatalf("PutCatalog: %v", err)
	}
	v, hit, err = c.GetCatalog("r")
	if err != nil || !hit {
		t.Fatalf("expected hit, got hit=%v err=%v", hit, err)
	}
	if len(v) != 2 || v[0] != "a" || v[1] != "b" {
		t.Errorf("cached payload wrong: %v", v)
	}

	// Fast-forward clock past TTL → miss
	c.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	_, hit, err = c.GetCatalog("r")
	if err != nil {
		t.Fatalf("stale get: %v", err)
	}
	if hit {
		t.Error("stale entry should be treated as a miss")
	}
}

func TestCacheTagsIsolatedPerImage(t *testing.T) {
	tmpDir := t.TempDir()
	c := NewCache(tmpDir, time.Hour)

	if err := c.PutTags("r", "jq", []string{"1.7.1"}); err != nil {
		t.Fatal(err)
	}
	if err := c.PutTags("r", "yq", []string{"4.35.1"}); err != nil {
		t.Fatal(err)
	}

	v, hit, _ := c.GetTags("r", "jq")
	if !hit || len(v) != 1 || v[0] != "1.7.1" {
		t.Errorf("jq tags: hit=%v v=%v", hit, v)
	}
	v, hit, _ = c.GetTags("r", "yq")
	if !hit || len(v) != 1 || v[0] != "4.35.1" {
		t.Errorf("yq tags: hit=%v v=%v", hit, v)
	}
}

func TestCacheHandlesSlashInImageName(t *testing.T) {
	tmpDir := t.TempDir()
	c := NewCache(tmpDir, time.Hour)

	// Image names with "/" must not escape the cache directory.
	if err := c.PutTags("r", "nested/tool", []string{"v1"}); err != nil {
		t.Fatal(err)
	}
	v, hit, _ := c.GetTags("r", "nested/tool")
	if !hit || len(v) != 1 || v[0] != "v1" {
		t.Errorf("nested tag cache: hit=%v v=%v", hit, v)
	}

	// File should be escaped, not split into a subdirectory.
	escapedPath := c.tagsPath("r", "nested/tool")
	if _, err := os.Stat(escapedPath); err != nil {
		t.Errorf("expected cache file at escaped path %q: %v", escapedPath, err)
	}
}

func TestCacheClearRemovesRegistryEntries(t *testing.T) {
	tmpDir := t.TempDir()
	c := NewCache(tmpDir, time.Hour)
	_ = c.PutCatalog("r", []string{"x"})
	_ = c.PutTags("r", "jq", []string{"1"})

	if err := c.Clear("r"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(tmpDir, "r")); !os.IsNotExist(err) {
		t.Errorf("Clear should remove the registry dir, stat err=%v", err)
	}
}

func TestCacheCorruptEntryTreatedAsMiss(t *testing.T) {
	tmpDir := t.TempDir()
	c := NewCache(tmpDir, time.Hour)

	path := c.catalogPath("r")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, hit, err := c.GetCatalog("r")
	if err != nil {
		t.Fatalf("corrupt entry produced an error, want silent miss: %v", err)
	}
	if hit {
		t.Error("corrupt entry should be a miss")
	}
}
