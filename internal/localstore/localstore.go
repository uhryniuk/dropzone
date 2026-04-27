// Package localstore manages dropzone's on-disk state under ~/.dropzone/.
//
// Layout:
//
//	~/.dropzone/
//	├── bin/                          # per-package wrapper scripts
//	├── packages/
//	│   └── <name>/
//	│       ├── current               # symlink → active digest dir
//	│       └── <digest-dir>/
//	│           ├── rootfs/           # full unpacked image
//	│           └── metadata.json
//	├── cache/                        # registry catalog/tag cache (see internal/registry)
//	└── config/
//	    └── config.yaml
//
// The `current` symlink is the integration seam: the wrapper script at
// ~/.dropzone/bin/<name> references `<name>/current/rootfs/<entrypoint>`,
// so `dz update` atomically flips packages to a new digest by swapping the
// symlink with no other state to change.
package localstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/uhryniuk/dropzone/internal/cosign"
	"github.com/uhryniuk/dropzone/internal/util"
)

// PackageMetadata is the per-installation record stored at
// <digest-dir>/metadata.json. Each installed digest keeps its own file so
// updates don't overwrite history and rollback can read a previous
// digest's metadata if we ever retain multiple digest dirs per package.
type PackageMetadata struct {
	Name              string                `json:"name"`
	Tag               string                `json:"tag"`
	Digest            string                `json:"digest"`
	Registry          string                `json:"registry"`
	Entrypoint        []string              `json:"entrypoint"`
	Platform          string                `json:"platform"`
	InstalledAt       time.Time             `json:"installed_at"`
	SignatureVerified bool                  `json:"signature_verified"`
	Signer            string                `json:"signer,omitempty"`
	Attestations      *cosign.Attestations  `json:"attestations,omitempty"`
}

// ErrNotInstalled is returned by lookup methods for a package with no
// `current` symlink.
var ErrNotInstalled = errors.New("package not installed")

// LocalStore manages the filesystem layout. Methods are safe for
// concurrent use within a single process.
type LocalStore struct {
	basePath string
	mu       sync.RWMutex
}

// New constructs a LocalStore rooted at basePath (typically ~/.dropzone).
func New(basePath string) *LocalStore {
	return &LocalStore{basePath: basePath}
}

// Init ensures the base directory structure exists. Idempotent.
func (s *LocalStore) Init() error {
	for _, dir := range []string{s.basePath, s.BinPath(), s.PackagesPath(), s.ConfigDir(), s.CacheDir()} {
		if err := util.CreateDirIfNotExist(dir); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}
	return nil
}

// Path helpers.

func (s *LocalStore) BasePath() string     { return s.basePath }
func (s *LocalStore) BinPath() string      { return filepath.Join(s.basePath, "bin") }
func (s *LocalStore) PackagesPath() string { return filepath.Join(s.basePath, "packages") }
func (s *LocalStore) ConfigDir() string    { return filepath.Join(s.basePath, "config") }
func (s *LocalStore) CacheDir() string     { return filepath.Join(s.basePath, "cache") }

// PackageDir returns the directory for a package by name (parent of digest
// subdirectories). This is the unit of removal on `dz remove`.
func (s *LocalStore) PackageDir(name string) string {
	return filepath.Join(s.PackagesPath(), name)
}

// CurrentSymlinkPath returns the path of the `current` symlink for a
// package. The symlink's target is a digest-directory name relative to
// the package dir (so the link stays valid regardless of where the local
// store is mounted).
func (s *LocalStore) CurrentSymlinkPath(name string) string {
	return filepath.Join(s.PackageDir(name), "current")
}

// DigestDirPath returns the directory for a specific digest-dir name.
// Callers compute the digest-dir name via the shim package's
// digestToDirName helper (colons replaced with dashes) so paths are
// filesystem-safe.
func (s *LocalStore) DigestDirPath(name, digestDirName string) string {
	return filepath.Join(s.PackageDir(name), digestDirName)
}

// SetCurrent atomically points <name>/current at the given digest-dir
// name. The digest directory must already exist. Uses symlink-then-rename
// so the flip appears atomic to any concurrent reader.
func (s *LocalStore) SetCurrent(name, digestDirName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	pkgDir := s.PackageDir(name)
	targetDir := filepath.Join(pkgDir, digestDirName)
	if _, err := os.Stat(targetDir); err != nil {
		return fmt.Errorf("digest dir does not exist: %w", err)
	}

	linkPath := s.CurrentSymlinkPath(name)
	tmpLink := linkPath + ".new"

	// os.Remove is a no-op if the .new side doesn't exist yet; we tolerate
	// stale tmp links from a crashed prior attempt.
	_ = os.Remove(tmpLink)
	if err := os.Symlink(digestDirName, tmpLink); err != nil {
		return fmt.Errorf("create tmp symlink: %w", err)
	}
	if err := os.Rename(tmpLink, linkPath); err != nil {
		_ = os.Remove(tmpLink)
		return fmt.Errorf("swap symlink: %w", err)
	}
	return nil
}

// CurrentDigestDir returns the digest-dir name that <name>/current points
// at, or ErrNotInstalled if the symlink does not exist.
func (s *LocalStore) CurrentDigestDir(name string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	target, err := os.Readlink(s.CurrentSymlinkPath(name))
	if err != nil {
		if os.IsNotExist(err) {
			return "", ErrNotInstalled
		}
		return "", err
	}
	return target, nil
}

// StoreMetadata writes metadata.json inside the digest directory for this
// installation. The digest directory must already exist (shim.Build
// creates it). Overwrites any existing file.
func (s *LocalStore) StoreMetadata(meta PackageMetadata, digestDirName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	dir := s.DigestDirPath(meta.Name, digestDirName)
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("digest dir does not exist: %w", err)
	}
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}
	path := filepath.Join(dir, "metadata.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write metadata: %w", err)
	}
	return nil
}

// GetMetadata reads metadata.json for the currently-active installation
// of a package. Returns ErrNotInstalled if no `current` symlink exists.
func (s *LocalStore) GetMetadata(name string) (*PackageMetadata, error) {
	digestDir, err := s.CurrentDigestDir(name)
	if err != nil {
		return nil, err
	}
	return s.GetMetadataForDigestDir(name, digestDir)
}

// GetMetadataForDigestDir reads metadata.json from a specific digest
// directory of a package, regardless of which digest is currently active.
// Used by update flows to compare old and new digests.
func (s *LocalStore) GetMetadataForDigestDir(name, digestDirName string) (*PackageMetadata, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	path := filepath.Join(s.DigestDirPath(name, digestDirName), "metadata.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotInstalled
		}
		return nil, fmt.Errorf("read metadata: %w", err)
	}
	var meta PackageMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("parse metadata: %w", err)
	}
	return &meta, nil
}

// ListInstalled returns metadata for every package with an active
// `current` symlink. Packages whose current symlink points at a missing
// or corrupt digest dir are skipped (not a hard error; `dz doctor` will
// reconcile them later).
func (s *LocalStore) ListInstalled() ([]PackageMetadata, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	root := s.PackagesPath()
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read packages dir: %w", err)
	}

	var out []PackageMetadata
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		linkTarget, err := os.Readlink(filepath.Join(root, name, "current"))
		if err != nil {
			continue // no current symlink → not a completed install
		}
		metaPath := filepath.Join(root, name, linkTarget, "metadata.json")
		data, err := os.ReadFile(metaPath)
		if err != nil {
			continue
		}
		var meta PackageMetadata
		if err := json.Unmarshal(data, &meta); err != nil {
			continue
		}
		out = append(out, meta)
	}
	return out, nil
}

// RemovePackage deletes the entire packages/<name>/ tree. Callers should
// unshim (remove the wrapper at bin/<name>) before calling this so a
// broken remove doesn't leave a wrapper pointing at an absent directory.
func (s *LocalStore) RemovePackage(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return os.RemoveAll(s.PackageDir(name))
}

// ListDigestDirs returns every digest-directory name present under a
// package, excluding the `current` symlink. Used by rollback (to find
// the previous digest) and by doctor (to surface orphans).
func (s *LocalStore) ListDigestDirs(name string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entries, err := os.ReadDir(s.PackageDir(name))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.Name() == "current" {
			continue
		}
		if !e.IsDir() {
			continue
		}
		out = append(out, e.Name())
	}
	return out, nil
}

// ListPackageNames returns every directory under packages/, regardless
// of whether it has a current symlink. doctor() uses this to detect
// orphaned package dirs.
func (s *LocalStore) ListPackageNames() ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entries, err := os.ReadDir(s.PackagesPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// RemoveDigestDir removes a single digest directory under a package.
// Used by doctor when reaping stale prior installs after a rollback
// limit is exceeded; not exposed as a CLI directly.
func (s *LocalStore) RemoveDigestDir(name, digestDirName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return os.RemoveAll(s.DigestDirPath(name, digestDirName))
}
