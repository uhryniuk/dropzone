package registry

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// extractTar reads a tar stream and writes its entries into dest. The tar is
// expected to be a pre-flattened rootfs (whiteouts already applied by
// go-containerregistry's mutate.Extract), so this function only has to
// materialize files, directories, and links safely.
//
// Safety properties:
//   - Entry paths that escape dest (via "../" or absolute paths) are rejected.
//   - Hardlink targets must also stay inside dest.
//   - File modes are masked to 0o777. Setuid/setgid/sticky bits are dropped
//     per BACKBURNER.md; revisit when we hit a real need for them.
//   - UID/GID in tar headers are ignored; everything is owned by the user
//     running dz (we're not running as root and can't chown to arbitrary ids).
//   - Device and FIFO entries are skipped silently.
func extractTar(r io.Reader, dest string) error {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return fmt.Errorf("create dest: %w", err)
	}
	destAbs, err := filepath.Abs(dest)
	if err != nil {
		return fmt.Errorf("resolve dest: %w", err)
	}

	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read tar entry: %w", err)
		}

		target, err := safeJoin(destAbs, hdr.Name)
		if err != nil {
			return fmt.Errorf("tar entry %q: %w", hdr.Name, err)
		}

		mode := os.FileMode(hdr.Mode) & 0o777

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, mode|0o700); err != nil {
				return fmt.Errorf("mkdir %s: %w", hdr.Name, err)
			}

		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("mkdir parent of %s: %w", hdr.Name, err)
			}
			if err := writeFile(target, tr, mode); err != nil {
				return fmt.Errorf("write %s: %w", hdr.Name, err)
			}

		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("mkdir parent of %s: %w", hdr.Name, err)
			}
			// Overwrite any existing entry at the target path. Symlink
			// target strings are written verbatim; they're interpreted
			// relative to the link's location at resolution time, so a
			// symlink to "../lib/foo" stays valid inside the staging dir.
			_ = os.Remove(target)
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return fmt.Errorf("symlink %s: %w", hdr.Name, err)
			}

		case tar.TypeLink:
			linkTarget, err := safeJoin(destAbs, hdr.Linkname)
			if err != nil {
				return fmt.Errorf("hardlink target %q: %w", hdr.Linkname, err)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("mkdir parent of %s: %w", hdr.Name, err)
			}
			_ = os.Remove(target)
			if err := os.Link(linkTarget, target); err != nil {
				return fmt.Errorf("hardlink %s: %w", hdr.Name, err)
			}

		case tar.TypeChar, tar.TypeBlock, tar.TypeFifo:
			// Skip special files. Not useful for host install; cannot be
			// created without root on most systems anyway.
			continue

		default:
			// Unknown types: skip rather than fail. Includes PAX globals,
			// GNU extensions we don't care about, etc.
			continue
		}
	}
}

// writeFile creates target with the given mode and copies data from r.
// The file is closed before returning to release the descriptor.
func writeFile(target string, r io.Reader, mode os.FileMode) error {
	f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	_, cerr := io.Copy(f, r)
	if err := f.Close(); err != nil && cerr == nil {
		cerr = err
	}
	return cerr
}

// safeJoin returns filepath.Join(base, name) but refuses to return a path
// that resolves outside base. Protects against tar entries whose names
// contain "../" sequences or absolute paths.
func safeJoin(baseAbs, name string) (string, error) {
	// Reject absolute entry names outright. Tar entries should be relative
	// to the archive root; an absolute path is either a mistake or an attack.
	if filepath.IsAbs(name) {
		return "", errors.New("absolute path in tar entry")
	}
	cleaned := filepath.Clean(filepath.Join(baseAbs, name))
	if cleaned != baseAbs && !strings.HasPrefix(cleaned, baseAbs+string(os.PathSeparator)) {
		return "", errors.New("path escapes destination")
	}
	return cleaned, nil
}
