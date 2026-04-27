package packagehandler

import (
	"fmt"
	"os"
	"path/filepath"
)

// DoctorReport summarizes drift between dropzone's expected state and
// what's actually on disk. Each field captures one class of orphan;
// callers render them as a checklist for the user.
//
// `dz doctor` is read-only by default; if the user wants automatic
// reconciliation we expose --fix at the CLI level which acts on each
// orphan class one at a time.
type DoctorReport struct {
	// PackagesWithoutCurrent: packages/<name>/ exists but `current`
	// symlink is missing or broken. Likely a partial install or a
	// ranged-out digest dir. Reconciliation: pick the latest
	// installable digest dir and re-link, or remove the package dir.
	PackagesWithoutCurrent []string
	// CurrentSymlinkBroken: `current` exists but points at a
	// nonexistent digest dir. Same fix path as PackagesWithoutCurrent.
	CurrentSymlinkBroken map[string]string // package -> dangling target
	// WrapperWithoutPackage: ~/.dropzone/bin/<name> exists but no
	// package dir of the same name does. Reconciliation: remove the
	// wrapper.
	WrapperWithoutPackage []string
	// PackageWithoutWrapper: package + current symlink exist but the
	// wrapper at bin/<name> is missing. Reconciliation: regenerate the
	// wrapper. Phase 8 reports; doesn't auto-fix this without a flag
	// since regenerating requires re-running shim.Build.
	PackageWithoutWrapper []string
	// PathNotConfigured: ~/.dropzone/bin not on $PATH. Suggests
	// running `dz path setup`.
	PathNotConfigured bool
}

// HasIssues reports whether anything in the report needs attention.
func (r DoctorReport) HasIssues() bool {
	return len(r.PackagesWithoutCurrent) > 0 ||
		len(r.CurrentSymlinkBroken) > 0 ||
		len(r.WrapperWithoutPackage) > 0 ||
		len(r.PackageWithoutWrapper) > 0 ||
		r.PathNotConfigured
}

// Doctor inspects on-disk state and produces a DoctorReport. Always
// returns a non-nil report; an error here means the inspection itself
// failed (filesystem unreadable), not that issues were found.
func (h *PackageHandler) Doctor() (*DoctorReport, error) {
	r := &DoctorReport{
		CurrentSymlinkBroken: map[string]string{},
	}

	pkgNames, err := h.store.ListPackageNames()
	if err != nil {
		return nil, fmt.Errorf("list packages: %w", err)
	}

	// Build a set of expected wrappers from package state.
	expectedWrappers := map[string]bool{}
	for _, name := range pkgNames {
		current, err := h.store.CurrentDigestDir(name)
		if err != nil {
			r.PackagesWithoutCurrent = append(r.PackagesWithoutCurrent, name)
			continue
		}
		// CurrentDigestDir returns the symlink target, but doesn't
		// confirm the target exists. Stat it to detect dangling links.
		target := filepath.Join(h.store.PackageDir(name), current)
		if _, err := os.Stat(target); err != nil && os.IsNotExist(err) {
			r.CurrentSymlinkBroken[name] = current
			continue
		}
		expectedWrappers[name] = true
	}

	// Inventory wrappers under bin/.
	binEntries, err := os.ReadDir(h.integrator.BinPath())
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("list bin: %w", err)
	}
	wrapperPresent := map[string]bool{}
	for _, e := range binEntries {
		if e.IsDir() {
			continue
		}
		wrapperPresent[e.Name()] = true
	}

	for w := range wrapperPresent {
		if !expectedWrappers[w] {
			r.WrapperWithoutPackage = append(r.WrapperWithoutPackage, w)
		}
	}
	for w := range expectedWrappers {
		if !wrapperPresent[w] {
			r.PackageWithoutWrapper = append(r.PackageWithoutWrapper, w)
		}
	}

	if !pathOnEnv(h.integrator.BinPath()) {
		r.PathNotConfigured = true
	}
	return r, nil
}

// pathOnEnv mirrors hostintegration.OnPath; duplicated here to avoid an
// import cycle since hostintegration is a peer package and we don't
// currently import it from packagehandler. Tiny enough that the
// duplication is fine.
func pathOnEnv(dir string) bool {
	for _, p := range filepath.SplitList(os.Getenv("PATH")) {
		if p == dir {
			return true
		}
	}
	return false
}

// FixDoctor applies safe, reversible fixes for the report's findings.
// Specifically:
//
//   - WrapperWithoutPackage: removes the orphan wrapper (it's a
//     dropzone-marked file with no backing package; safe to delete).
//   - CurrentSymlinkBroken: removes the broken symlink. The user
//     re-installs to recover; we don't try to guess which digest dir
//     to re-link without metadata.
//
// Fixes that need human input or destructive operations (purging
// packages, regenerating wrappers) stay opt-in via separate commands.
func (h *PackageHandler) FixDoctor(r *DoctorReport) (*DoctorReport, error) {
	for _, w := range r.WrapperWithoutPackage {
		_ = h.integrator.RemoveWrapper(w)
	}
	for name := range r.CurrentSymlinkBroken {
		_ = os.Remove(filepath.Join(h.store.PackageDir(name), "current"))
	}
	// Re-scan after fixes so the caller can show what's left.
	return h.Doctor()
}
