package packagehandler

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

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
		// The package was installed via a hostname-qualified ref
		// (e.g., gitea.example.com/owner/repo) against an ephemeral
		// registry that was never persisted in config. Materialize it
		// the same way registry.Manager.Resolve does on the install
		// path so update can still reach the source registry.
		if strings.ContainsAny(p.Registry, ".:") {
			reg = &registry.Registry{Name: p.Registry, URL: p.Registry}
		} else {
			u.UnreachableError = err
			return u
		}
	}

	// imagePath is the full path within the registry, used for both
	// digest lookup and tag listing. For metadata written by older
	// dropzone versions Image will be empty; fall back to Name (the
	// basename) so the lookup still works for short-name installs
	// like `python`.
	imagePath := p.Image
	if imagePath == "" {
		imagePath = p.Name
	}

	digest, err := h.registries.Client().Digest(ctx, reg, imagePath, p.Tag)
	if err != nil {
		u.UnreachableError = fmt.Errorf("digest lookup: %w", err)
		return u
	}
	u.CurrentDigest = digest

	tags, err := h.registries.Client().Tags(ctx, reg, imagePath)
	if err != nil {
		// Tag listing failure shouldn't poison digest-drift detection;
		// record but keep going.
		u.UnreachableError = fmt.Errorf("tag list: %w", err)
		return u
	}
	u.NewerTags = newerTagsThan(p.Tag, tags)
	return u
}

// cosignSidecarTagPattern matches the tag scheme cosign uses for the
// per-image signature, attestation, and SBOM artifacts that live on the
// same repo as the image: sha256-<hex>[.ext] (.sig, .att, .sbom, etc.).
// Real human-facing tags never follow this pattern, so a simple regex
// suffices to filter all of them out of update output without false
// positives.
var cosignSidecarTagPattern = regexp.MustCompile(`^sha256-[0-9a-f]{64}(\.[a-z0-9]+)?$`)

// IsCosignSidecarTag reports whether tag is a cosign sidecar tag (a
// signature, attestation, or SBOM artifact pushed alongside an image).
// Exported so commands like `dz tags` can apply the same noise filter
// as `dz update` without duplicating the regex.
func IsCosignSidecarTag(tag string) bool {
	return cosignSidecarTagPattern.MatchString(tag)
}

// newerTagsThan picks tags lexicographically greater than current. We
// deliberately don't try to be clever about semver: most registries
// (Chainguard included) ship a mix of date-stamped and semver tags, and
// a careful comparison would need per-package knowledge. The rough
// "greater than current string" heuristic surfaces obviously newer
// versions without false negatives.
//
// Tags that don't describe a version are filtered:
//
//   - Floating tags (latest, stable, edge, main, master, dev): these
//     are current-pointers, not versions.
//   - Cosign sidecar tags (sha256-<hex>.sig|.att|.sbom etc.): these
//     are signature, attestation, and SBOM artifacts that cosign
//     pushes alongside the real image. Any signed image has hundreds.
//   - The installed tag's `-suffix` variants (e.g., latest-dev when
//     latest is installed): these are sibling builds, not successors.
func newerTagsThan(current string, tags []string) []string {
	floating := map[string]bool{
		"latest": true, "stable": true, "edge": true, "main": true,
		"master": true, "dev": true,
	}
	variantPrefix := current + "-"
	var out []string
	for _, t := range tags {
		if floating[t] {
			continue
		}
		if t == current {
			continue
		}
		if cosignSidecarTagPattern.MatchString(t) {
			continue
		}
		if strings.HasPrefix(t, variantPrefix) {
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
	// Reconstruct the install ref from metadata. Image carries the
	// full path within the registry; older metadata may not have it
	// in which case Name (the basename) is the fallback.
	imagePath := existing.Image
	if imagePath == "" {
		imagePath = name
	}
	ref := existing.Registry + "/" + imagePath
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
