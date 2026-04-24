package shim

import (
	"fmt"
	"strings"
)

// WrapperInput is everything the wrapper generator needs to produce a
// POSIX shell script at ~/.dropzone/bin/<Name>.
//
// LoaderRel is empty for static binaries (and always empty on macOS, where
// the system dyld is used). Baked args are passed through Entrypoint.BakedArgs
// and inserted before "$@" in the wrapper.
type WrapperInput struct {
	// Name is the package name used for the marker comment in the wrapper.
	// It's the last path segment of the user's install ref.
	Name string
	// Digest is the resolved digest, written into the marker comment for
	// easy auditing (`cat ~/.dropzone/bin/jq` shows the exact image digest
	// that binary came from).
	Digest string
	// PackagesDir is the absolute path to the dropzone packages directory
	// (typically ~/.dropzone/packages). We bake this into the wrapper as
	// an absolute path so the wrapper is independent of $HOME at runtime
	// and can be audited by reading the file.
	PackagesDir string
	// Entrypoint is the validated entrypoint from IdentifyEntrypoint.
	Entrypoint *Entrypoint
	// LoaderRel is the rootfs-relative path to the dynamic loader, or
	// empty for a static binary / macOS.
	LoaderRel string
	// HostOS is the host GOOS ("linux" or "darwin"). Determines the
	// wrapper template.
	HostOS string
}

// Marker is the comment line used to identify a dropzone-written wrapper.
// Host integration uses this to distinguish our files from user-written
// scripts on remove.
const Marker = "# dropzone-wrapper"

// GenerateWrapper returns the wrapper script content for the given input.
// Callers write this to ~/.dropzone/bin/<Name> with mode 0755.
func GenerateWrapper(in WrapperInput) (string, error) {
	if in.Name == "" || in.PackagesDir == "" || in.Entrypoint == nil {
		return "", fmt.Errorf("wrapper input is missing required fields")
	}
	switch in.HostOS {
	case "linux":
		return generateLinuxWrapper(in), nil
	case "darwin":
		return generateDarwinWrapper(in), nil
	default:
		return "", fmt.Errorf("unsupported host OS: %q", in.HostOS)
	}
}

func generateLinuxWrapper(in WrapperInput) string {
	var b strings.Builder

	b.WriteString("#!/bin/sh\n")
	fmt.Fprintf(&b, "%s %s %s\n", Marker, in.Name, in.Digest)
	fmt.Fprintf(&b, "PKG=%s/%s/current\n", shellEscape(in.PackagesDir), shellEscape(in.Name))
	b.WriteString("ROOT=\"$PKG/rootfs\"\n")

	if in.LoaderRel == "" {
		// Static binary: exec directly, no library path fiddling needed.
		fmt.Fprintf(&b, "exec \"$ROOT\"/%s", pathShellEscape(in.Entrypoint.Path))
	} else {
		// Dynamic: invoke the container's loader with --library-path
		// pointing at the bundled rootfs lib dirs. This is the
		// "wrapper-script trick" — no patchelf, binary stays byte-exact.
		fmt.Fprintf(&b, "exec \"$ROOT\"/%s \\\n", pathShellEscape(in.LoaderRel))
		b.WriteString("    --library-path \"$ROOT/usr/lib:$ROOT/lib:$ROOT/usr/local/lib:$ROOT/usr/lib64:$ROOT/lib64\" \\\n")
		fmt.Fprintf(&b, "    \"$ROOT\"/%s", pathShellEscape(in.Entrypoint.Path))
	}

	for _, arg := range in.Entrypoint.BakedArgs {
		b.WriteString(" ")
		b.WriteString(shellEscape(arg))
	}
	b.WriteString(" \"$@\"\n")
	return b.String()
}

func generateDarwinWrapper(in WrapperInput) string {
	var b strings.Builder

	b.WriteString("#!/bin/sh\n")
	fmt.Fprintf(&b, "%s %s %s\n", Marker, in.Name, in.Digest)
	fmt.Fprintf(&b, "PKG=%s/%s/current\n", shellEscape(in.PackagesDir), shellEscape(in.Name))
	b.WriteString("ROOT=\"$PKG/rootfs\"\n")
	// macOS uses the system dyld. We set DYLD_FALLBACK_LIBRARY_PATH
	// rather than DYLD_LIBRARY_PATH so user-set library paths still win;
	// the bundled libs are the fallback. Prepending to any existing value
	// keeps the caller's environment intact.
	b.WriteString("DYLD_FALLBACK_LIBRARY_PATH=\"$ROOT/usr/lib:$ROOT/lib:${DYLD_FALLBACK_LIBRARY_PATH}\" \\\n")
	fmt.Fprintf(&b, "    exec \"$ROOT\"/%s", pathShellEscape(in.Entrypoint.Path))

	for _, arg := range in.Entrypoint.BakedArgs {
		b.WriteString(" ")
		b.WriteString(shellEscape(arg))
	}
	b.WriteString(" \"$@\"\n")
	return b.String()
}

// shellEscape wraps s in single quotes for safe use in a POSIX shell
// command, handling embedded single quotes via the standard '\'' dance.
func shellEscape(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// pathShellEscape handles a rootfs-relative path that gets appended onto
// "$ROOT/" in the wrapper. We can't wrap the whole thing in single quotes
// (that would break the $ROOT expansion), so we escape just the path's
// own characters. For the paths we emit — standard binary paths like
// "usr/bin/jq" or "lib64/ld-linux-x86-64.so.2" — this is trivially safe.
// Pathological inputs with spaces or special characters would need more
// care; we assert they're absent at write time if they ever show up in a
// real image.
func pathShellEscape(p string) string {
	// Guard against surprise characters. Chainguard and friends produce
	// plain POSIX paths, so hitting this path means something weird.
	if strings.ContainsAny(p, " \t\n'\"\\$`") {
		// Fallback: single-quote and break the quote context around $ROOT.
		// Rare enough that the awkwardness is acceptable.
		return "'" + strings.ReplaceAll(p, "'", `'\''`) + "'"
	}
	return p
}
