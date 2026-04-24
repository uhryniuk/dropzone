// Package shim turns a pulled OCI image's rootfs into a runnable, native
// shimmed binary on the host.
//
// The pipeline is intentionally minimal:
//
//   1. Validate the image's Entrypoint[0] is an ELF (Linux) or Mach-O
//      (macOS) matching the host CPU architecture. Reject shell-script
//      entrypoints and cross-platform binaries at this layer so failures
//      surface before we touch persistent state.
//   2. Read the dynamic loader path out of the binary's PT_INTERP on
//      Linux. macOS uses the system dyld, so no loader lookup is needed.
//   3. Generate a POSIX wrapper script that invokes the entrypoint from
//      inside the bundled rootfs with library-search env vars pointing at
//      the rootfs's lib directories.
//
// The binary itself is never modified. No patchelf, no ELF rewriting.
// Signatures on the binary (relevant on macOS) stay valid because we only
// touch wrapping metadata.
package shim

import (
	"errors"
	"fmt"
)

// Errors surfaced by the shim package.
var (
	// ErrNotExecutable means the resolved entrypoint exists in the rootfs
	// but is not an ELF or Mach-O file. Shell scripts fall in here. The
	// error message points users at the non-Dev variant of their image.
	ErrNotExecutable = errors.New("entrypoint is not an executable")

	// ErrWrongArch means the entrypoint is an ELF/Mach-O for a different
	// CPU architecture than the host. Typically impossible if Phase 2's
	// platform selection worked, but we still verify so a bad image can't
	// silently install a broken binary.
	ErrWrongArch = errors.New("entrypoint architecture does not match host")

	// ErrWrongOS mirrors ErrWrongArch for OS mismatch (ELF on macOS, Mach-O
	// on Linux).
	ErrWrongOS = errors.New("entrypoint OS does not match host")

	// ErrEmptyEntrypoint means the image declared an empty Entrypoint.
	// Base images like wolfi-base do this intentionally; they're not
	// directly installable.
	ErrEmptyEntrypoint = errors.New("image entrypoint is empty")

	// ErrEntrypointNotFound means Entrypoint[0] does not resolve to a file
	// inside the rootfs. Indicates a broken image or a bad symlink chain.
	ErrEntrypointNotFound = errors.New("entrypoint not found in rootfs")
)

// Entrypoint describes a validated, resolved entrypoint inside an unpacked
// rootfs. All paths are rootfs-relative (no leading slash) for portability.
type Entrypoint struct {
	// Path is the resolved rootfs-relative path to the binary. If the
	// image's Entrypoint[0] was a symlink, this is the path it pointed to,
	// with the chain already followed.
	Path string
	// BakedArgs are Entrypoint[1:] preserved verbatim. The wrapper script
	// passes these before the user's args, so an image like
	// ["/usr/bin/jq", "--unbuffered"] keeps that flag even under dropzone.
	BakedArgs []string
	// Format is "elf" or "mach-o".
	Format string
}

// fmtError wraps a typed sentinel with context. Short helper to keep the
// validation paths terse and uniform.
func fmtError(base error, format string, args ...any) error {
	return fmt.Errorf("%w: %s", base, fmt.Sprintf(format, args...))
}
