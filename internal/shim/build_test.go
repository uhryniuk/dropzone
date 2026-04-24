package shim

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestBuildMovesStagingIntoPackageDir(t *testing.T) {
	home := t.TempDir()
	packagesDir := filepath.Join(home, ".dropzone", "packages")

	// Stage a usable rootfs with the test binary as the entrypoint. On
	// Linux we also pre-stage the host loader inside the rootfs so the
	// shim builder's loader check passes.
	staging := filepath.Join(home, "staging")
	writeHostBinary(t, filepath.Join(staging, "usr/bin/tool"))
	if runtime.GOOS == "linux" {
		self, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		interp, err := readInterpFromHost(self)
		if err != nil {
			t.Skipf("self binary is static (no PT_INTERP): %v", err)
		}
		rel := strings.TrimPrefix(interp, "/")
		if err := os.MkdirAll(filepath.Dir(filepath.Join(staging, rel)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(staging, rel), []byte("stub"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	result, err := Build(BuildInput{
		Name:            "tool",
		Digest:          "sha256:abc123",
		PackagesDir:     packagesDir,
		StagingDir:      staging,
		ImageEntrypoint: []string{"/usr/bin/tool"},
	}, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	// Staging dir should be gone; rootfs should be under the package dir.
	if _, err := os.Stat(staging); !os.IsNotExist(err) {
		t.Errorf("staging dir should be moved away, stat err=%v", err)
	}
	wantRootfs := filepath.Join(packagesDir, "tool", "sha256-abc123", "rootfs")
	if result.RootfsDir != wantRootfs {
		t.Errorf("RootfsDir: got %q, want %q", result.RootfsDir, wantRootfs)
	}
	if _, err := os.Stat(filepath.Join(wantRootfs, "usr/bin/tool")); err != nil {
		t.Errorf("entrypoint not at expected path after build: %v", err)
	}

	// Wrapper content references the digest-dir layout via `current`.
	if !strings.Contains(result.WrapperContent, "'tool'/current") {
		t.Errorf("wrapper does not reference current symlink: %q", result.WrapperContent)
	}
	if !strings.Contains(result.WrapperContent, `"$ROOT"/usr/bin/tool`) {
		t.Errorf("wrapper entrypoint path wrong: %q", result.WrapperContent)
	}
}

func TestBuildFailsCleanlyOnBadEntrypoint(t *testing.T) {
	home := t.TempDir()
	staging := filepath.Join(home, "staging")
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatal(err)
	}
	// Shell-script entrypoint.
	if err := os.MkdirAll(filepath.Join(staging, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, "bin/tool"), []byte("#!/bin/sh\necho hi\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := Build(BuildInput{
		Name:            "tool",
		Digest:          "sha256:abc",
		PackagesDir:     filepath.Join(home, ".dropzone", "packages"),
		StagingDir:      staging,
		ImageEntrypoint: []string{"/bin/tool"},
	}, runtime.GOOS, runtime.GOARCH)
	if err == nil {
		t.Fatal("expected Build to refuse shell-script entrypoints")
	}

	// Staging dir must not have been moved (Build aborted before any move).
	if _, err := os.Stat(staging); err != nil {
		t.Errorf("staging dir should remain untouched on validation failure: %v", err)
	}
}

func TestBuildReplacesStalePackageDir(t *testing.T) {
	// If a prior run left behind packages/<name>/<digest-dir>/ from a
	// failed install, Build should nuke it and proceed rather than fail.
	home := t.TempDir()
	packagesDir := filepath.Join(home, ".dropzone", "packages")

	// Put junk in the destination directory.
	stale := filepath.Join(packagesDir, "tool", "sha256-abc")
	if err := os.MkdirAll(stale, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stale, "junk"), []byte("leftover"), 0o644); err != nil {
		t.Fatal(err)
	}

	staging := filepath.Join(home, "staging")
	writeHostBinary(t, filepath.Join(staging, "usr/bin/tool"))
	if runtime.GOOS == "linux" {
		self, _ := os.Executable()
		interp, err := readInterpFromHost(self)
		if err != nil {
			t.Skipf("self is static: %v", err)
		}
		rel := strings.TrimPrefix(interp, "/")
		if err := os.MkdirAll(filepath.Dir(filepath.Join(staging, rel)), 0o755); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(staging, rel), []byte("stub"), 0o755)
	}

	_, err := Build(BuildInput{
		Name:            "tool",
		Digest:          "sha256:abc",
		PackagesDir:     packagesDir,
		StagingDir:      staging,
		ImageEntrypoint: []string{"/usr/bin/tool"},
	}, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stale, "junk")); !os.IsNotExist(err) {
		t.Errorf("stale junk should be cleared: stat err=%v", err)
	}
}

func TestDigestToDirNameReplacesColons(t *testing.T) {
	cases := map[string]string{
		"sha256:abc":      "sha256-abc",
		"sha512:def":      "sha512-def",
		"no-colon":        "no-colon",
		"slashes/in/name": "slashes-in-name",
	}
	for in, want := range cases {
		if got := digestToDirName(in); got != want {
			t.Errorf("digestToDirName(%q) = %q, want %q", in, got, want)
		}
	}
}
