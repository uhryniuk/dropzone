package shim

import (
	"debug/elf"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestFindLoaderReadsInterpreterFromPTInterp(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("ELF-specific")
	}
	rootfs := t.TempDir()
	writeHostBinary(t, filepath.Join(rootfs, "usr/bin/tool"))

	// The test binary is a dynamically-linked Go binary (Go test binaries
	// are linked this way by default); it should have a PT_INTERP segment.
	// We don't hard-code the exact loader path — it varies with glibc vs
	// musl and distro — but we do require it to be the same one the host
	// system uses, and we place that file at the expected path inside the
	// rootfs so the "loader exists" check passes.
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	interpHost, err := readInterpFromHost(self)
	if err != nil {
		t.Skipf("self binary has no PT_INTERP (static?): %v", err)
	}

	// Stage the loader inside the rootfs at the same path.
	loaderRel := strings.TrimPrefix(interpHost, "/")
	if err := os.MkdirAll(filepath.Dir(filepath.Join(rootfs, loaderRel)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootfs, loaderRel), []byte("fake loader"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := FindLoader(rootfs, "usr/bin/tool")
	if err != nil {
		t.Fatalf("FindLoader: %v", err)
	}
	if got != loaderRel {
		t.Errorf("loader: got %q, want %q", got, loaderRel)
	}
}

func TestFindLoaderRejectsMissingLoaderInRootfs(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("ELF-specific")
	}
	// If the test binary itself is statically linked (CGO_ENABLED=0 is
	// common on CI), it has no PT_INTERP and FindLoader has nothing to
	// check. Detect that and skip so the test stays meaningful where it
	// applies without flaking where it doesn't.
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := readInterpFromHost(self); err != nil {
		t.Skipf("self binary is static; skipping dynamic-only test: %v", err)
	}

	rootfs := t.TempDir()
	// Binary exists but the loader it points at doesn't. Typical of a
	// broken image: the image author forgot to bundle libc.
	writeHostBinary(t, filepath.Join(rootfs, "usr/bin/tool"))

	_, err = FindLoader(rootfs, "usr/bin/tool")
	if err == nil {
		t.Fatal("expected error when loader is absent from rootfs")
	}
	if !errors.Is(err, ErrNotExecutable) {
		t.Errorf("want ErrNotExecutable, got %v", err)
	}
}

// readInterpFromHost reads the PT_INTERP segment of a binary on the host
// filesystem. Used by tests to discover the host's interpreter so the
// test can stage it inside the rootfs before calling FindLoader.
func readInterpFromHost(path string) (string, error) {
	f, err := elf.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	for _, p := range f.Progs {
		if p.Type != elf.PT_INTERP {
			continue
		}
		buf := make([]byte, p.Filesz)
		if _, err := p.ReadAt(buf, 0); err != nil {
			return "", err
		}
		return strings.TrimRight(string(buf), "\x00"), nil
	}
	return "", fmt.Errorf("no PT_INTERP segment")
}
