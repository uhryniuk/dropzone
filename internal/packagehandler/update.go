package packagehandler

import (
	"context"
	"fmt"
	"sort"

	"github.com/uhryniuk/dropzone/internal/localstore"
	"github.com/uhryniuk/dropzone/internal/registry"
)

// UpdateInfo describes the gap between an installed package and what
// its source registry currently advertises.
//
// SameTagDigestDrift fires when the installed tag's digest has moved
// upstream — typically a CVE-patch rebuild of "latest" or a fixed
// version tag. NewerTags lists tags lexicographically/semver-greater
// than the installed tag (best effort; we don't refuse to install
// non-semver tags). Either or both may be set; both empty means no
// update.
type UpdateInfo struct {
	Name             string
	Registry         string
	InstalledTag     string
	InstalledDigest  string
	CurrentDigest    string   // digest the registry currently serves for InstalledTag
	NewerTags        []string // tags greater than InstalledTag
	UnreachableError error    // populated when the registry couldn't be queried
}

// HasUpdate reports whether anything noteworthy changed upstream.
func (u UpdateInfo) HasUpdate() bool {
	return u.SameTagRebuild() || len(u.NewerTags) > 0
}

// SameTagRebuild reports whether the installed tag now points at a
// different digest. This is the headline signal for security-driven
// updates: same tag, new contents, typically because a CVE was patched.
func (u UpdateInfo) SameTagRebuild() bool {
	return u.CurrentDigest != "" && u.CurrentDigest != u.InstalledDigest
}

// CheckUpdates queries the source registry for every installed package
// and reports drift. Per-package errors are recorded on the returned
// UpdateInfo (UnreachableError) instead of aborting the whole scan, so
// one offline registry doesn't hide updates available elsewhere.
func (h *PackageHandler) CheckUpdates(ctx context.Context) ([]UpdateInfo, error) {
	pkgs, err := h.store.ListInstalled()
	if err != nil {
		return nil, fmt.Errorf("list installed: %w", err)
	}
	out := make([]UpdateInfo, 0, len(pkgs))
	for _, p := range pkgs {
		out = append(out, h.checkOne(ctx, p))
	}
	return out, nil
}

// CheckUpdate scopes the scan to a single installed package by name.
func (h *PackageHandler) CheckUpdate(ctx context.Context, name string) (UpdateInfo, error) {
	p, err := h.store.GetMetadata(name)
	if err != nil {
		return UpdateInfo{}, err
	}
	return h.checkOne(ctx, *p), nil
}

func (h *PackageHandler) checkOne(ctx context.Context, p localstore.PackageMetadata) UpdateInfo {
	u := UpdateInfo{
		Name:            p.Name,
		Registry:        p.Registry,
		InstalledTag:    p.Tag,
		InstalledDigest: p.Digest,
	}
	reg, err := h.registries.Get(p.Registry)
	if err != nil {
		u.UnreachableError = err
		return u
	}

	digest, err := h.registries.Client().Digest(ctx, reg, p.Name, p.Tag)
	if err != nil {
		u.UnreachableError = fmt.Errorf("digest lookup: %w", err)
		return u
	}
	u.CurrentDigest = digest

	tags, err := h.registries.Tags(ctx, p.Registry, p.Name, true)
	if err != nil {
		// Tag listing failure shouldn't poison digest-drift detection;
		// record but keep going.
		u.UnreachableError = fmt.Errorf("tag list: %w", err)
		return u
	}
	u.NewerTags = newerTagsThan(p.Tag, tags)
	return u
}

// newerTagsThan picks tags lexicographically greater than current. We
// deliberately don't try to be clever about semver: most registries
// (Chainguard included) ship a mix of date-stamped and semver tags, and
// a careful comparison would need per-package knowledge. The rough
// "greater than current string" heuristic surfaces obviously newer
// versions without false negatives.
//
// Floating tags ("latest", "stable", "edge", etc.) are excluded -- they
// don't describe a version, just a current pointer, so they're not
// "newer" than anything.
func newerTagsThan(current string, tags []string) []string {
	floating := map[string]bool{
		"latest": true, "stable": true, "edge": true, "main": true,
		"master": true, "dev": true,
	}
	var out []string
	for _, t := range tags {
		if floating[t] {
			continue
		}
		if t == current {
			continue
		}
		if t > current {
			out = append(out, t)
		}
	}
	sort.Strings(out)
	return out
}

// ApplyUpdate re-runs the install flow at the requested target ref for
// an already-installed package. Tag may be the same as currently
// installed (rebuild case) or a different tag (newer-tag case).
//
// The previous digest directory is retained alongside the new one so a
// later `dz rollback` can flip current back. Phase 8 surfaces this; the
// retention policy here just means we don't delete anything besides
// the current symlink target swap.
func (h *PackageHandler) ApplyUpdate(ctx context.Context, name, targetTag string, opts InstallOptions) (*InstallResult, error) {
	existing, err := h.store.GetMetadata(name)
	if err != nil {
		return nil, err
	}
	ref := existing.Registry + "/" + name
	if targetTag != "" {
		ref += ":" + targetTag
	}
	return h.InstallPackage(ctx, ref, opts)
}

// resolveRegistryFromName is a small shim used by tests when they need
// to construct a ResolvedRef without the manager. Not used by prod code.
func (h *PackageHandler) resolveRegistryFromName(p localstore.PackageMetadata) (*registry.Registry, error) {
	return h.registries.Get(p.Registry)
}
