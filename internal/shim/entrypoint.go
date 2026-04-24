package shim

import (
	"debug/elf"
	"debug/macho"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// maxSymlinkHops bounds entrypoint symlink resolution. Chainguard's images
// typically have one or two levels at most (e.g., /usr/bin/python3 →
// /usr/bin/python3.12). Anything higher suggests a cycle or a malformed
// image.
const maxSymlinkHops = 10

// IdentifyEntrypoint resolves and validates an image's Entrypoint against
// an unpacked rootfs.
//
//   - imageEntrypoint is the Entrypoint[] slice from the image config.
//     The first element is the executable; remaining elements are baked
//     args preserved verbatim in the wrapper.
//   - rootfs is the absolute path to the unpacked rootfs directory.
//   - hostOS, hostArch are runtime.GOOS and runtime.GOARCH.
//
// Returns the validated entrypoint with its path resolved through any
// symlinks, or a typed error describing why the image isn't installable.
func IdentifyEntrypoint(imageEntrypoint []string, rootfs, hostOS, hostArch string) (*Entrypoint, error) {
	if len(imageEntrypoint) == 0 {
		return nil, ErrEmptyEntrypoint
	}
	rel := strings.TrimPrefix(imageEntrypoint[0], "/")
	if rel == "" {
		return nil, fmtError(ErrEmptyEntrypoint, "entrypoint path is empty")
	}

	resolved, err := resolveInsideRootfs(rootfs, rel)
	if err != nil {
		return nil, err
	}

	format, err := checkExecutable(filepath.Join(rootfs, resolved), hostOS, hostArch)
	if err != nil {
		return nil, err
	}

	return &Entrypoint{
		Path:      resolved,
		BakedArgs: append([]string(nil), imageEntrypoint[1:]...),
		Format:    format,
	}, nil
}

// resolveInsideRootfs follows symlinks relative to rootfs, refusing any
// target that escapes it. Returns the final rootfs-relative path.
func resolveInsideRootfs(rootfs, rel string) (string, error) {
	rootfsAbs, err := filepath.Abs(rootfs)
	if err != nil {
		return "", fmt.Errorf("resolve rootfs path: %w", err)
	}

	current := rel
	for hop := 0; hop < maxSymlinkHops; hop++ {
		abs := filepath.Join(rootfsAbs, current)
		info, err := os.Lstat(abs)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return "", fmtError(ErrEntrypointNotFound, "%s (resolving %q)", err, current)
			}
			return "", fmt.Errorf("stat %q: %w", current, err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			// Reached a non-symlink. Confirm the resolved absolute path
			// is still inside rootfs — defense in depth against a chain
			// that looped back.
			if !isInside(rootfsAbs, abs) {
				return "", fmtError(ErrEntrypointNotFound, "entrypoint resolves outside rootfs")
			}
			return current, nil
		}

		target, err := os.Readlink(abs)
		if err != nil {
			return "", fmt.Errorf("readlink %q: %w", current, err)
		}

		// Symlink target may be absolute (interpreted inside the rootfs,
		// not the host) or relative (to the symlink's containing directory).
		if filepath.IsAbs(target) {
			current = strings.TrimPrefix(target, "/")
		} else {
			current = filepath.Join(filepath.Dir(current), target)
		}
		current = filepath.Clean(current)

		// After the clean, confirm we haven't walked off the rootfs via
		// too many "../" components.
		if strings.HasPrefix(current, "..") || filepath.IsAbs(current) && !isInside(rootfsAbs, filepath.Join(rootfsAbs, current)) {
			return "", fmtError(ErrEntrypointNotFound, "symlink target escapes rootfs: %q", target)
		}
	}
	return "", fmtError(ErrEntrypointNotFound, "symlink chain too deep (>%d hops)", maxSymlinkHops)
}

// isInside reports whether path is equal to or inside base. Both must be
// absolute and cleaned.
func isInside(base, path string) bool {
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return false
	}
	return rel == "." || !strings.HasPrefix(rel, "..")
}

// checkExecutable opens the file, verifies it's an ELF (on Linux) or Mach-O
// (on macOS), and that its CPU matches the host. Anything else produces a
// typed error that the CLI can surface cleanly.
func checkExecutable(absPath, hostOS, hostArch string) (string, error) {
	f, err := os.Open(absPath)
	if err != nil {
		return "", fmt.Errorf("open entrypoint: %w", err)
	}
	defer f.Close()

	// Sniff the first few bytes. ELF starts with 0x7F 'E' 'L' 'F'.
	// Mach-O has four magic numbers (two variants × two endian orders).
	var magic [4]byte
	if _, err := f.ReadAt(magic[:], 0); err != nil {
		return "", fmtError(ErrNotExecutable, "read magic: %s", err)
	}

	if isELFMagic(magic) {
		if hostOS != "linux" {
			return "", fmtError(ErrWrongOS, "ELF binary but host is %s", hostOS)
		}
		return "elf", verifyELFArch(absPath, hostArch)
	}
	if isMachOMagic(magic) {
		if hostOS != "darwin" {
			return "", fmtError(ErrWrongOS, "Mach-O binary but host is %s", hostOS)
		}
		return "mach-o", verifyMachOArch(absPath, hostArch)
	}
	return "", fmtError(ErrNotExecutable, "entrypoint is not an ELF or Mach-O file")
}

func isELFMagic(m [4]byte) bool {
	return m[0] == 0x7f && m[1] == 'E' && m[2] == 'L' && m[3] == 'F'
}

func isMachOMagic(m [4]byte) bool {
	// 32-bit LE, 32-bit BE, 64-bit LE, 64-bit BE respectively.
	return (m[0] == 0xfe && m[1] == 0xed && m[2] == 0xfa && (m[3] == 0xce || m[3] == 0xcf)) ||
		(m[0] == 0xce && m[1] == 0xfa && m[2] == 0xed && m[3] == 0xfe) ||
		(m[0] == 0xcf && m[1] == 0xfa && m[2] == 0xed && m[3] == 0xfe)
}

func verifyELFArch(path, hostArch string) error {
	f, err := elf.Open(path)
	if err != nil {
		return fmtError(ErrNotExecutable, "parse ELF: %s", err)
	}
	defer f.Close()

	want := map[string]elf.Machine{
		"amd64": elf.EM_X86_64,
		"arm64": elf.EM_AARCH64,
	}[hostArch]
	if f.Machine != want {
		return fmtError(ErrWrongArch, "ELF e_machine=%s, host %s", f.Machine, hostArch)
	}
	return nil
}

func verifyMachOArch(path, hostArch string) error {
	f, err := macho.Open(path)
	if err != nil {
		return fmtError(ErrNotExecutable, "parse Mach-O: %s", err)
	}
	defer f.Close()

	want := map[string]macho.Cpu{
		"amd64": macho.CpuAmd64,
		"arm64": macho.CpuArm64,
	}[hostArch]
	if f.Cpu != want {
		return fmtError(ErrWrongArch, "Mach-O cpu=%s, host %s", f.Cpu, hostArch)
	}
	return nil
}
