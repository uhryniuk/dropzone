package packagehandler

import (
	"errors"

	"github.com/uhryniuk/dropzone/internal/hostintegration"
	"github.com/uhryniuk/dropzone/internal/localstore"
)

// errNotReimplemented is the uniform stub error returned by every
// PackageHandler method until the registry + shim + cosign pipeline
// lands (see docs/roadmap.md, phases 1-5).
var errNotReimplemented = errors.New("not yet reimplemented after design pivot; see docs/roadmap.md")

// PackageHandler orchestrates install, update, list, and remove.
//
// Post-pivot, its dependencies will be the Registry Manager, Cosign Verifier,
// and Shim Builder. Those slots are intentionally absent during Phase 0.
type PackageHandler struct {
	store      *localstore.LocalStore
	integrator *hostintegration.HostIntegrator
}

// New creates a new PackageHandler.
func New(store *localstore.LocalStore, integrator *hostintegration.HostIntegrator) *PackageHandler {
	return &PackageHandler{
		store:      store,
		integrator: integrator,
	}
}

func (h *PackageHandler) InstallPackage(ref string) error { return errNotReimplemented }
func (h *PackageHandler) RemovePackage(name string) error { return errNotReimplemented }
func (h *PackageHandler) ListInstalled() error            { return errNotReimplemented }
