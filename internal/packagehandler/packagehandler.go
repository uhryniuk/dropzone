package packagehandler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/uhryniuk/dropzone/internal/hostintegration"
	"github.com/uhryniuk/dropzone/internal/localstore"
	"github.com/uhryniuk/dropzone/internal/registry"
	"github.com/uhryniuk/dropzone/internal/shim"
	"github.com/uhryniuk/dropzone/internal/util"
)

// ErrAlreadyInstalled is returned by Install when the resolved digest
// matches the currently-installed digest. Not a hard failure; CLI surfaces
// it as a friendly "nothing to do".
var ErrAlreadyInstalled = errors.New("package already installed at this digest")

// PackageHandler orchestrates install, remove, and list. The phases it
// composes are:
//
//   registry.Manager  (pull + platform select)   [phase 1-2]
//   shim.Build        (validate + stage + wrapper) [phase 3]
//   localstore        (persistent state)          [phase 3]
//   hostintegration   (wrapper file + bin dir)    [phase 3]
//
// Sigstore verification hooks into Install between the Pull and the shim
// Build in phase 4. Attestation fetch hooks in after verification in
// phase 5.
type PackageHandler struct {
	store      *localstore.LocalStore
	integrator *hostintegration.HostIntegrator
	registries *registry.Manager
	// out is where progress messages go. Normally os.Stdout; tests
	// redirect to a buffer.
	out io.Writer
}

// New constructs a PackageHandler. The registry manager is optional in
// tests that don't exercise the network path.
func New(store *localstore.LocalStore, integrator *hostintegration.HostIntegrator, registries *registry.Manager) *PackageHandler {
	return &PackageHandler{
		store:      store,
		integrator: integrator,
		registries: registries,
		out:        os.Stdout,
	}
}

// InstallResult summarizes a completed install. Returned to the CLI so
// it can print a structured success message instead of re-deriving state
// from disk.
type InstallResult struct {
	Name       string
	Tag        string
	Digest     string
	Registry   string
	Platform   string
	Entrypoint []string
	BinaryPath string
}

// InstallPackage performs the end-to-end install for a user-typed ref.
// Phase 3 wiring: no signature verification yet; that arrives in phase 4.
func (h *PackageHandler) InstallPackage(ctx context.Context, ref string) (*InstallResult, error) {
	resolved, err := h.registries.Resolve(ref)
	if err != nil {
		return nil, fmt.Errorf("resolve %q: %w", ref, err)
	}
	util.LogInfo("Resolving %s -> %s/%s:%s", ref, resolved.Registry.Name, resolved.Image, resolved.Tag)

	// Short-circuit if an installation of this package already points at
	// the tag we're being asked to install (not the digest — the tag, so
	// "install jq" after "install jq:latest" still triggers a re-pull when
	// latest has moved). We rely on the registry's Digest call to detect
	// digest drift; this is the tag-based dedupe.
	if existing, err := h.store.GetMetadata(resolved.Registry.Name); err == nil && existing != nil {
		_ = existing // placeholder: multi-registry metadata dedupe lands later
	}

	staging, err := os.MkdirTemp("", "dz-install-*")
	if err != nil {
		return nil, fmt.Errorf("create staging dir: %w", err)
	}
	defer os.RemoveAll(staging)

	util.LogInfo("Pulling image...")
	info, err := h.registries.Pull(ctx, resolved, staging)
	if err != nil {
		return nil, fmt.Errorf("pull: %w", err)
	}

	// Phase 4 hook: cosign.Verify(resolved.Registry.CosignPolicy, info.Digest)
	// goes here, before we touch persistent state.

	util.LogInfo("Unpacking to package directory...")
	pkgName := resolved.Image
	result, err := shim.Build(shim.BuildInput{
		Name:            pkgName,
		Digest:          info.Digest,
		PackagesDir:     h.store.PackagesPath(),
		StagingDir:      staging,
		ImageEntrypoint: info.Entrypoint,
	}, util.HostOS(), util.HostArch())
	if err != nil {
		return nil, fmt.Errorf("shim build: %w", err)
	}
	// shim.Build moved the staging dir. Don't let defer os.RemoveAll panic
	// or try to remove a non-existent directory; make the defer a no-op by
	// ensuring the variable points at something harmless.
	// (os.RemoveAll is a no-op on a missing path, so this is already safe,
	// but worth noting for anyone reading the flow.)

	digestDir := digestDirFromResult(info.Digest)

	meta := localstore.PackageMetadata{
		Name:              pkgName,
		Tag:               resolved.Tag,
		Digest:            info.Digest,
		Registry:          resolved.Registry.Name,
		Entrypoint:        info.Entrypoint,
		Platform:          info.Platform,
		InstalledAt:       time.Now().UTC(),
		SignatureVerified: false, // phase 4 will update this
	}
	if err := h.store.StoreMetadata(meta, digestDir); err != nil {
		return nil, fmt.Errorf("store metadata: %w", err)
	}
	if err := h.store.SetCurrent(pkgName, digestDir); err != nil {
		return nil, fmt.Errorf("set current symlink: %w", err)
	}
	if err := h.integrator.InstallWrapper(pkgName, result.WrapperContent); err != nil {
		return nil, fmt.Errorf("install wrapper: %w", err)
	}

	util.LogInfo("Installed %s (digest %s)", pkgName, info.Digest)
	return &InstallResult{
		Name:       pkgName,
		Tag:        resolved.Tag,
		Digest:     info.Digest,
		Registry:   resolved.Registry.Name,
		Platform:   info.Platform,
		Entrypoint: info.Entrypoint,
		BinaryPath: h.integrator.BinPath() + "/" + pkgName,
	}, nil
}

// RemovePackage unshims the wrapper and deletes the package directory.
// Order matters: wrapper first so a failed file-delete can't leave a
// runnable shim pointing at a half-removed package.
func (h *PackageHandler) RemovePackage(name string) error {
	if _, err := h.store.GetMetadata(name); err != nil {
		if errors.Is(err, localstore.ErrNotInstalled) {
			return fmt.Errorf("%s: not installed", name)
		}
		return err
	}
	if err := h.integrator.RemoveWrapper(name); err != nil {
		return fmt.Errorf("remove wrapper: %w", err)
	}
	if err := h.store.RemovePackage(name); err != nil {
		return fmt.Errorf("remove package files: %w", err)
	}
	return nil
}

// ListInstalled returns metadata for every currently-active installation.
func (h *PackageHandler) ListInstalled() ([]localstore.PackageMetadata, error) {
	return h.store.ListInstalled()
}

// digestDirFromResult mirrors shim.digestToDirName (colons → dashes).
// Duplicated here so the package doesn't have to export that helper;
// the canonical logic lives in shim, but it's stable enough that
// re-deriving it trivially is fine.
func digestDirFromResult(digest string) string {
	out := make([]byte, 0, len(digest))
	for i := 0; i < len(digest); i++ {
		c := digest[i]
		if c == ':' || c == '/' {
			out = append(out, '-')
			continue
		}
		out = append(out, c)
	}
	return string(out)
}
