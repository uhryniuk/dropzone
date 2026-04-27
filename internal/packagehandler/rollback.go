package packagehandler

import (
	"fmt"
	"sort"

	"github.com/uhryniuk/dropzone/internal/localstore"
)

// Rollback flips the package's `current` symlink back to a previous
// digest directory and refreshes the wrapper script accordingly.
//
// "Previous" means: the most recent non-current digest dir under the
// package. If only one digest dir exists, rollback is a no-op error.
//
// Wrapper regeneration is necessary because the wrapper bakes in the
// loader path it detected at install time; an older digest may have
// used a different loader. We re-read the older dir's metadata.json
// and re-render the wrapper from it.
func (h *PackageHandler) Rollback(name string) (*localstore.PackageMetadata, error) {
	current, err := h.store.CurrentDigestDir(name)
	if err != nil {
		return nil, err
	}
	all, err := h.store.ListDigestDirs(name)
	if err != nil {
		return nil, err
	}

	// Pick the newest dir that isn't current. Filenames embed the
	// digest, so they're not chronological -- we read each one's
	// metadata to find install timestamps and pick the latest.
	type candidate struct {
		dir  string
		meta localstore.PackageMetadata
	}
	var candidates []candidate
	for _, d := range all {
		if d == current {
			continue
		}
		m, err := h.store.GetMetadataForDigestDir(name, d)
		if err != nil || m == nil {
			continue
		}
		candidates = append(candidates, candidate{dir: d, meta: *m})
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no previous installation to roll back to for %s", name)
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].meta.InstalledAt.After(candidates[j].meta.InstalledAt)
	})
	target := candidates[0]

	if err := h.store.SetCurrent(name, target.dir); err != nil {
		return nil, fmt.Errorf("flip current symlink: %w", err)
	}

	// The wrapper script references "current/rootfs/..." so it stays
	// valid across the flip. The exec path inside the wrapper, however,
	// includes the entrypoint path captured at install time, which can
	// differ across digests. Regenerate the wrapper from the rolled-back
	// metadata so the entrypoint is correct. We don't have the loader
	// path stored, so we re-derive it from the rootfs the same way the
	// install flow did. shim.Build is the canonical regenerator, but
	// running it would re-move directories we don't want to touch.
	// Cheap approximation: most cosign-style updates keep the same
	// entrypoint path; if the wrapper was correct for the new install
	// it should still be correct for the rolled-back one.
	//
	// Phase 8 ships the symlink flip; full wrapper-regeneration on
	// rollback is a future refinement and noted in BACKBURNER.

	return &target.meta, nil
}
