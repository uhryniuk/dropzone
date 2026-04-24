package shim

import (
	"debug/elf"
	"os"
	"path/filepath"
	"strings"
)

// FindLoader reads the dynamic loader path out of an ELF binary's
// PT_INTERP segment and returns it as a rootfs-relative path.
//
// A binary with no PT_INTERP is statically linked; we return ("", nil)
// and the wrapper generator skips the loader-invocation path.
//
// macOS is not handled here: Mach-O binaries load the system dyld, so the
// wrapper on darwin just sets DYLD_FALLBACK_LIBRARY_PATH and execs the
// binary directly. Callers pass Linux binaries only.
//
// The loader path as stored in ELF is absolute (e.g.,
// "/lib64/ld-linux-x86-64.so.2"), interpreted relative to the container's
// filesystem when run. We return it with the leading slash stripped so
// the wrapper can compose `$ROOT/<loader>` without worrying about
// double-slashes.
func FindLoader(rootfs, entrypointRelPath string) (string, error) {
	f, err := elf.Open(filepath.Join(rootfs, entrypointRelPath))
	if err != nil {
		return "", err
	}
	defer f.Close()

	for _, prog := range f.Progs {
		if prog.Type != elf.PT_INTERP {
			continue
		}
		buf := make([]byte, prog.Filesz)
		if _, err := prog.ReadAt(buf, 0); err != nil {
			return "", err
		}
		// PT_INTERP data is a null-terminated C string.
		path := strings.TrimRight(string(buf), "\x00")
		rel := strings.TrimPrefix(path, "/")

		// Confirm the loader actually exists inside the rootfs. If it
		// doesn't, the image is broken: a dynamic binary requires its
		// loader to be present. Returning "" would silently fall through
		// to the static-binary code path and produce a non-functional
		// wrapper. Report it instead.
		absLoader := filepath.Join(rootfs, rel)
		if _, err := os.Stat(absLoader); err != nil {
			if os.IsNotExist(err) {
				return "", fmtError(ErrNotExecutable, "dynamic loader %q not in rootfs", path)
			}
			return "", err
		}
		return rel, nil
	}
	// No PT_INTERP segment: static binary.
	return "", nil
}
