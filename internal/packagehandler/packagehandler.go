package packagehandler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/uhryniuk/dropzone/internal/cosign"
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
//   registry.Manager  (pull + platform select)         [phase 1-2]
//   cosign.Verifier   (signature + policy)             [phase 4]
//   shim.Build        (validate + stage + wrapper)     [phase 3]
//   localstore        (persistent state)               [phase 3]
//   hostintegration   (wrapper file + bin dir)         [phase 3]
//
// Attestation fetch (SBOM/SLSA/vuln) lands after verification in phase 5.
type PackageHandler struct {
	store      *localstore.LocalStore
	integrator *hostintegration.HostIntegrator
	registries *registry.Manager
	verifier   *cosign.Verifier
	out        io.Writer
}

// New constructs a PackageHandler. The registry manager and verifier are
// optional in tests that don't exercise the network or signature paths.
func New(store *localstore.LocalStore, integrator *hostintegration.HostIntegrator, registries *registry.Manager, verifier *cosign.Verifier) *PackageHandler {
	return &PackageHandler{
		store:      store,
		integrator: integrator,
		registries: registries,
		verifier:   verifier,
		out:        os.Stdout,
	}
}

// InstallOptions captures install-time flags.
type InstallOptions struct {
	// AllowUnsigned skips signature verification. Only honored when the
	// source registry has no cosign policy configured; a registry WITH a
	// policy that fails verification cannot be bypassed (that flag is for
	// missing policies, not failed ones).
	AllowUnsigned bool
}

// InstallResult summarizes a completed install. Returned to the CLI so
// it can print a structured success message instead of re-deriving state
// from disk.
type InstallResult struct {
	Name              string
	Tag               string
	Digest            string
	Registry          string
	Platform          string
	Entrypoint        []string
	BinaryPath        string
	SignatureVerified bool
	Signer            string
	Issuer            string
	Attestations      *cosign.Attestations
}

// InstallPackage performs the end-to-end install for a user-typed ref.
//
// Phase 4 wiring: between Pull and shim.Build we now fetch a Sigstore
// signature bundle from the registry and run it through the cosign
// verifier with the registry's configured policy. Failure modes:
//
//   - Registry has a policy + bundle present + verification succeeds
//     → install proceeds, metadata records the verified identity.
//   - Registry has a policy + bundle missing → abort with a clear "no
//     signature for this image" error.
//   - Registry has a policy + bundle present + verification fails →
//     abort. --allow-unsigned does not bypass this; failed verification
//     of a policy-signed image is a hard "do not install" signal.
//   - Registry has no policy + --allow-unsigned passed → install
//     proceeds, recorded as unsigned in metadata.
//   - Registry has no policy + --allow-unsigned NOT passed → abort,
//     telling the user to add a policy or pass --allow-unsigned.
func (h *PackageHandler) InstallPackage(ctx context.Context, ref string, opts InstallOptions) (*InstallResult, error) {
	resolved, err := h.registries.Resolve(ref)
	if err != nil {
		return nil, fmt.Errorf("resolve %q: %w", ref, err)
	}
	util.LogInfo("Resolving %s -> %s/%s:%s", ref, resolved.Registry.Name, resolved.Image, resolved.Tag)

	staging, err := os.MkdirTemp("", "dz-install-*")
	if err != nil {
		return nil, fmt.Errorf("create staging dir: %w", err)
	}
	defer os.RemoveAll(staging)

	util.LogInfo("Pulling image...")
	info, err := h.registries.Pull(ctx, resolved, staging)
	if err != nil {
		return nil, fmt.Errorf("pull: %w", annotatePullError(err, resolved.Registry.URL))
	}

	// Verification: hard gate before any persistent state changes.
	verifyResult, err := h.verifyImage(ctx, resolved, info.Digest, opts)
	if err != nil {
		return nil, err
	}

	// Attestation fetch: best-effort, only meaningful for verified
	// signatures (otherwise the attestations have no trust root). Failure
	// here never blocks the install; we just skip the surfacing.
	var atts *cosign.Attestations
	if verifyResult.verified {
		raws, ferr := h.registries.Client().FetchAttestations(ctx, *resolved, info.Digest)
		if ferr == nil {
			atts = cosign.SummarizeAttestations(raws)
		}
	}

	util.LogInfo("Unpacking to package directory...")
	// Package name is the basename of the image path, not the full path.
	// Otherwise an install of "dilly/crane" would try to write
	// ~/.dropzone/bin/dilly/crane which has a directory in it.
	pkgName := resolved.Image
	if i := strings.LastIndex(pkgName, "/"); i >= 0 {
		pkgName = pkgName[i+1:]
	}
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

	digestDir := digestDirFromResult(info.Digest)

	meta := localstore.PackageMetadata{
		Name:              pkgName,
		Tag:               resolved.Tag,
		Digest:            info.Digest,
		Registry:          resolved.Registry.Name,
		Entrypoint:        info.Entrypoint,
		Platform:          info.Platform,
		InstalledAt:       time.Now().UTC(),
		SignatureVerified: verifyResult.verified,
		Signer:            verifyResult.signer,
		Attestations:      atts,
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
		Name:              pkgName,
		Tag:               resolved.Tag,
		Digest:            info.Digest,
		Registry:          resolved.Registry.Name,
		Platform:          info.Platform,
		Entrypoint:        info.Entrypoint,
		BinaryPath:        h.integrator.BinPath() + "/" + pkgName,
		SignatureVerified: verifyResult.verified,
		Signer:            verifyResult.signer,
		Issuer:            verifyResult.issuer,
		Attestations:      atts,
	}, nil
}

// verifyOutcome is the cosign verification step's result. Internal to
// the package handler; collapses both "verified successfully" and
// "skipped because allowed unsigned" into one shape with a flag.
type verifyOutcome struct {
	verified bool
	signer   string
	issuer   string
}

// verifyImage decides whether the install can proceed based on the
// registry's policy, the available signature bundle, and the user's
// --allow-unsigned choice. See InstallPackage doc for the truth table.
func (h *PackageHandler) verifyImage(ctx context.Context, ref *registry.ResolvedRef, digest string, opts InstallOptions) (verifyOutcome, error) {
	policy := ref.Registry.CosignPolicy

	// Registry has no policy: install only with explicit user opt-in.
	if policy == nil {
		if !opts.AllowUnsigned {
			return verifyOutcome{}, fmt.Errorf(
				"registry %q has no cosign policy configured. "+
					"Add one in ~/.dropzone/config/config.yaml, pass --allow-unsigned for one install, "+
					"or set always_allow_unsigned: true in the config to stop being prompted",
				ref.Registry.Name)
		}
		util.LogInfo("Skipping signature verification (registry has no policy, --allow-unsigned)")
		return verifyOutcome{verified: false}, nil
	}

	// Registry has a policy: fetch the bundle and verify. Failure to find
	// a bundle is a hard error -- a policy says "this registry's images
	// must be signed".
	if h.verifier == nil {
		return verifyOutcome{}, errors.New("cosign verifier not configured")
	}

	util.LogInfo("Fetching signature bundle...")
	bundleJSON, err := h.registries.Client().FetchBundle(ctx, *ref, digest)
	if err != nil {
		if errors.Is(err, registry.ErrBundleNotFound) {
			return verifyOutcome{}, fmt.Errorf(
				"no signature bundle for %s@%s. The image is unsigned or this registry doesn't carry signatures here",
				ref.Image, digest)
		}
		return verifyOutcome{}, fmt.Errorf("fetch bundle: %w", err)
	}

	util.LogInfo("Verifying signature against %s policy...", ref.Registry.Name)
	cp := cosign.Policy{
		Issuer:        policy.Issuer,
		IdentityRegex: policy.IdentityRegex,
	}
	var res *cosign.Result
	switch {
	case bundleJSON.Legacy != nil:
		// Cosign legacy signature: Rekor inclusion proof in the bundle
		// annotation, signature/cert in sibling annotations, payload in
		// the layer blob. Convert into a Sigstore protobuf bundle and
		// verify through the same code path.
		res, err = h.verifier.VerifyLegacy(cosign.LegacyParts{
			Bundle:         bundleJSON.Legacy.Bundle,
			SignatureB64:   bundleJSON.Legacy.SignatureB64,
			CertificatePEM: bundleJSON.Legacy.CertificatePEM,
			Payload:        bundleJSON.Legacy.Payload,
		}, digest, cp)
	case len(bundleJSON.Modern) > 0:
		res, err = h.verifier.Verify(bundleJSON.Modern, digest, cp)
	default:
		return verifyOutcome{}, fmt.Errorf("bundle fetch returned no payload")
	}
	if err != nil {
		// Do not fall through to --allow-unsigned. A policy that fails
		// is the strongest possible "do not install" signal.
		return verifyOutcome{}, fmt.Errorf("verification failed: %w", err)
	}

	util.LogInfo("Signature verified: %s", res.Signer)
	return verifyOutcome{
		verified: true,
		signer:   res.Signer,
		issuer:   res.Issuer,
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

// annotatePullError adds a Chainguard-specific hint when the registry
// returns FORBIDDEN against cgr.dev. The seeded default registry is
// docker.io/chainguard (anonymous pulls work); cgr.dev is reachable
// only after `chainctl auth login` + `chainctl auth configure-docker`
// writes credentials Docker's keychain reads. The hint helps users
// who add cgr.dev as an additional registry and hit the 403 token
// failure with no clue about why.
func annotatePullError(err error, registryURL string) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if !strings.Contains(strings.ToLower(msg), "forbidden") {
		return err
	}
	if !strings.Contains(registryURL, "cgr.dev") {
		return err
	}
	return fmt.Errorf(
		"%w\n\n"+
			"Chainguard's catalog requires a free login. If you have chainctl installed:\n"+
			"    chainctl auth login\n"+
			"    chainctl auth configure-docker\n"+
			"Then re-run dz install. Sessions are short-lived; if you've authenticated before, run those two commands again to refresh.",
		err)
}
