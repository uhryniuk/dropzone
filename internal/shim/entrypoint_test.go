package shim

import (
	"debug/elf"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// writeHostBinary copies the currently-running test binary (guaranteed to
// be an ELF on Linux or Mach-O on macOS for the current arch) to dest.
// Using the test binary itself avoids needing a separate build step to
// produce a platform-correct executable for entrypoint validation tests.
func writeHostBinary(t *testing.T, dest string) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	src, err := os.Open(self)
	if err != nil {
		t.Fatalf("open self: %v", err)
	}
	defer src.Close()

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	dst, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		t.Fatalf("create dest: %v", err)
	}
	defer dst.Close()

	if _, err := io.Copy(dst, src); err != nil {
		t.Fatalf("copy: %v", err)
	}
}

func TestIdentifyEntrypointAcceptsHostExecutable(t *testing.T) {
	rootfs := t.TempDir()
	writeHostBinary(t, filepath.Join(rootfs, "usr/bin/tool"))

	entry, err := IdentifyEntrypoint(
		[]string{"/usr/bin/tool", "--default"},
		rootfs, runtime.GOOS, runtime.GOARCH,
	)
	if err != nil {
		t.Fatalf("IdentifyEntrypoint: %v", err)
	}
	if entry.Path != "usr/bin/tool" {
		t.Errorf("Path: got %q", entry.Path)
	}
	if len(entry.BakedArgs) != 1 || entry.BakedArgs[0] != "--default" {
		t.Errorf("BakedArgs: %v", entry.BakedArgs)
	}

	wantFormat := "elf"
	if runtime.GOOS == "darwin" {
		wantFormat = "mach-o"
	}
	if entry.Format != wantFormat {
		t.Errorf("Format: got %q, want %q", entry.Format, wantFormat)
	}
}

func TestIdentifyEntrypointEmptyIsError(t *testing.T) {
	_, err := IdentifyEntrypoint(nil, t.TempDir(), runtime.GOOS, runtime.GOARCH)
	if !errors.Is(err, ErrEmptyEntrypoint) {
		t.Errorf("want ErrEmptyEntrypoint, got %v", err)
	}
}

func TestIdentifyEntrypointMissingFileIsError(t *testing.T) {
	_, err := IdentifyEntrypoint(
		[]string{"/does/not/exist"},
		t.TempDir(), runtime.GOOS, runtime.GOARCH,
	)
	if !errors.Is(err, ErrEntrypointNotFound) {
		t.Errorf("want ErrEntrypointNotFound, got %v", err)
	}
}

func TestIdentifyEntrypointRejectsShellScript(t *testing.T) {
	rootfs := t.TempDir()
	path := filepath.Join(rootfs, "usr/bin/wrapper")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho hi\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := IdentifyEntrypoint(
		[]string{"/usr/bin/wrapper"},
		rootfs, runtime.GOOS, runtime.GOARCH,
	)
	if !errors.Is(err, ErrNotExecutable) {
		t.Errorf("shell script entrypoint: want ErrNotExecutable, got %v", err)
	}
}

func TestIdentifyEntrypointFollowsSymlinks(t *testing.T) {
	rootfs := t.TempDir()
	// /usr/bin/tool -> /usr/bin/tool.real
	writeHostBinary(t, filepath.Join(rootfs, "usr/bin/tool.real"))
	if err := os.Symlink("tool.real", filepath.Join(rootfs, "usr/bin/tool")); err != nil {
		t.Fatal(err)
	}

	entry, err := IdentifyEntrypoint(
		[]string{"/usr/bin/tool"},
		rootfs, runtime.GOOS, runtime.GOARCH,
	)
	if err != nil {
		t.Fatalf("IdentifyEntrypoint: %v", err)
	}
	if entry.Path != "usr/bin/tool.real" {
		t.Errorf("symlink not followed: got %q", entry.Path)
	}
}

func TestIdentifyEntrypointFollowsAbsoluteSymlinksInsideRootfs(t *testing.T) {
	rootfs := t.TempDir()
	// /usr/bin/tool -> /opt/tool (absolute target, interpreted inside rootfs)
	writeHostBinary(t, filepath.Join(rootfs, "opt/tool"))
	if err := os.MkdirAll(filepath.Join(rootfs, "usr/bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/opt/tool", filepath.Join(rootfs, "usr/bin/tool")); err != nil {
		t.Fatal(err)
	}

	entry, err := IdentifyEntrypoint(
		[]string{"/usr/bin/tool"},
		rootfs, runtime.GOOS, runtime.GOARCH,
	)
	if err != nil {
		t.Fatalf("IdentifyEntrypoint: %v", err)
	}
	if entry.Path != "opt/tool" {
		t.Errorf("absolute-in-rootfs symlink: got %q", entry.Path)
	}
}

func TestIdentifyEntrypointRejectsSymlinkEscape(t *testing.T) {
	rootfs := t.TempDir()
	if err := os.MkdirAll(filepath.Join(rootfs, "usr/bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Target attempts to escape via ../../../..
	if err := os.Symlink("../../../../../../etc/passwd", filepath.Join(rootfs, "usr/bin/tool")); err != nil {
		t.Fatal(err)
	}

	_, err := IdentifyEntrypoint(
		[]string{"/usr/bin/tool"},
		rootfs, runtime.GOOS, runtime.GOARCH,
	)
	if err == nil {
		t.Fatal("expected symlink-escape rejection, got nil")
	}
	if !errors.Is(err, ErrEntrypointNotFound) {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestIdentifyEntrypointArchMismatch(t *testing.T) {
	// We can't easily produce a cross-arch binary on an arbitrary CI host,
	// so fake it: write a valid ELF with a wrong e_machine.
	if runtime.GOOS != "linux" {
		t.Skip("ELF-specific test")
	}
	rootfs := t.TempDir()
	path := filepath.Join(rootfs, "usr/bin/tool")
	writeHostBinary(t, path)
	mutateELFMachine(t, path, elf.EM_PPC64)

	_, err := IdentifyEntrypoint(
		[]string{"/usr/bin/tool"},
		rootfs, runtime.GOOS, runtime.GOARCH,
	)
	if !errors.Is(err, ErrWrongArch) {
		t.Errorf("want ErrWrongArch, got %v", err)
	}
}

// mutateELFMachine rewrites the e_machine field of an ELF binary in place.
// Used to synthesize a wrong-arch binary for architecture-mismatch tests
// without needing a cross-compiler available on the test host.
func mutateELFMachine(t *testing.T, path string, machine elf.Machine) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()

	// e_machine is at offset 0x12 for both ELF32 and ELF64 (after
	// EI_NIDENT + e_type, each 16B + 2B). It's a 2-byte little-endian
	// field on the platforms we care about.
	var buf [2]byte
	buf[0] = byte(machine)
	buf[1] = byte(machine >> 8)
	if _, err := f.WriteAt(buf[:], 0x12); err != nil {
		t.Fatalf("patch e_machine: %v", err)
	}
}
