package shim

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/uhryniuk/dropzone/internal/util"
)

// Result of a shim build. Callers use the wrapper content and package dir
// to finish the install (write the wrapper and update metadata).
type Result struct {
	// PackageDir is the absolute path to packages/<name>/<digest>/.
	PackageDir string
	// RootfsDir is the absolute path to packages/<name>/<digest>/rootfs/.
	RootfsDir string
	// Entrypoint is the validated entrypoint that Build was called with.
	Entrypoint *Entrypoint
	// LoaderRel is the detected loader (empty for static binaries/macOS).
	LoaderRel string
	// WrapperContent is the POSIX shell script content. Callers write it
	// to ~/.dropzone/bin/<name>.
	WrapperContent string
}

// BuildInput feeds Build.
type BuildInput struct {
	// Name is the package name (last segment of the install ref).
	Name string
	// Digest is the resolved image digest.
	Digest string
	// PackagesDir is the absolute path to ~/.dropzone/packages/.
	PackagesDir string
	// StagingDir is the absolute path to the rootfs staging directory
	// produced by registry.Client.Pull. Build moves this into place under
	// PackagesDir; callers should not rely on it existing after Build
	// returns successfully.
	StagingDir string
	// ImageEntrypoint is the Entrypoint[] slice from the image config.
	ImageEntrypoint []string
}

// Build finalizes a pulled image into an installed shimmed package.
//
// The flow, with partial-failure cleanup:
//
//   1. Validate the staging rootfs has a usable entrypoint. If not,
//      nothing persistent happens and the staging dir is left for the
//      caller to remove.
//   2. Detect the dynamic loader (Linux only).
//   3. Generate wrapper content in memory.
//   4. Move the staging rootfs to packages/<name>/<digest>/rootfs/.
//      This is a rename — atomic when staging and destination are on the
//      same filesystem, which they are by construction (both under the
//      local store path).
//
// At that point the on-disk state is:
//
//   ~/.dropzone/packages/<name>/<digest>/rootfs/...
//
// Callers (packagehandler) write the wrapper, write metadata.json, and
// flip the `current` symlink. We don't do those here because they're
// concerns of the install orchestrator, not the shim builder.
func Build(in BuildInput, hostOS, hostArch string) (*Result, error) {
	if in.Name == "" || in.Digest == "" || in.PackagesDir == "" || in.StagingDir == "" {
		return nil, fmt.Errorf("shim.Build: all fields in BuildInput are required")
	}

	entry, err := IdentifyEntrypoint(in.ImageEntrypoint, in.StagingDir, hostOS, hostArch)
	if err != nil {
		return nil, err
	}

	var loaderRel string
	if hostOS == "linux" {
		loaderRel, err = FindLoader(in.StagingDir, entry.Path)
		if err != nil {
			return nil, fmt.Errorf("locate dynamic loader: %w", err)
		}
	}

	wrapper, err := GenerateWrapper(WrapperInput{
		Name:        in.Name,
		Digest:      in.Digest,
		PackagesDir: in.PackagesDir,
		Entrypoint:  entry,
		LoaderRel:   loaderRel,
		HostOS:      hostOS,
	})
	if err != nil {
		return nil, fmt.Errorf("generate wrapper: %w", err)
	}

	// digestDir is the destination, e.g., packages/jq/sha256:abc.../
	// Digest strings from go-containerregistry contain a colon. Most
	// filesystems accept colons in names, but it's a hostile character
	// to have in a path (shell interpretation, Windows compatibility if
	// we ever expand there). Replace with a dash to be safe.
	digestDirName := digestToDirName(in.Digest)
	pkgDir := filepath.Join(in.PackagesDir, in.Name, digestDirName)
	rootfsDir := filepath.Join(pkgDir, "rootfs")

	if err := util.CreateDirIfNotExist(filepath.Dir(pkgDir)); err != nil {
		return nil, fmt.Errorf("create package parent dir: %w", err)
	}
	// If a prior failed install left the directory behind, nuke it so the
	// rename succeeds. This is safe: partial state here means nothing was
	// ever flipped into `current`.
	if _, err := os.Stat(pkgDir); err == nil {
		if err := os.RemoveAll(pkgDir); err != nil {
			return nil, fmt.Errorf("clean stale package dir: %w", err)
		}
	}
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		return nil, fmt.Errorf("create package dir: %w", err)
	}
	if err := os.Rename(in.StagingDir, rootfsDir); err != nil {
		// Fallback: if rename fails because of cross-filesystem moves or
		// similar, fall back to recursive copy + remove. Not expected in
		// normal use — staging lives under the local store — but worth
		// handling for exotic setups (e.g., /tmp on a different mount).
		if err := copyDir(in.StagingDir, rootfsDir); err != nil {
			return nil, fmt.Errorf("move rootfs into package dir: %w", err)
		}
		_ = os.RemoveAll(in.StagingDir)
	}

	return &Result{
		PackageDir:     pkgDir,
		RootfsDir:      rootfsDir,
		Entrypoint:     entry,
		LoaderRel:      loaderRel,
		WrapperContent: wrapper,
	}, nil
}

// digestToDirName replaces path-hostile characters in an OCI digest string
// ("sha256:abc...") with dashes so it's safe as a directory name.
func digestToDirName(d string) string {
	out := make([]byte, 0, len(d))
	for i := 0; i < len(d); i++ {
		c := d[i]
		if c == ':' || c == '/' {
			out = append(out, '-')
			continue
		}
		out = append(out, c)
	}
	return string(out)
}

// copyDir is a minimal recursive directory copy used as a cross-filesystem
// fallback when os.Rename fails. Preserves file modes and symlinks; does
// not preserve ownership (we're not root).
func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)

		if info.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			return os.Symlink(link, target)
		}

		if info.IsDir() {
			return os.MkdirAll(target, info.Mode()&0o777|0o700)
		}

		src, err := os.Open(path)
		if err != nil {
			return err
		}
		defer src.Close()

		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		dst, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode()&0o777)
		if err != nil {
			return err
		}
		defer dst.Close()

		_, err = dst.ReadFrom(src)
		return err
	})
}
