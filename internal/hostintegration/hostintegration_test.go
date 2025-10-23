package hostintegration

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dropzone/internal/util"
)

func setupTestIntegrator(t *testing.T) *HostIntegrator {
	tmpDir := t.TempDir()
	return New(tmpDir)
}

func TestSetupDropzoneBinPath(t *testing.T) {
	h := setupTestIntegrator(t)

	if err := h.SetupDropzoneBinPath(); err != nil {
		t.Fatalf("SetupDropzoneBinPath failed: %v", err)
	}

	if !util.FileExists(h.binPath) {
		t.Error("Bin directory was not created")
	}
}

func TestLinkPackageBinaries(t *testing.T) {
	h := setupTestIntegrator(t)
	// Ensure bin path exists
	h.SetupDropzoneBinPath()

	// 1. Setup Package
	pkgName := "testpkg"
	pkgVer := "1.0.0"
	pkgRoot := filepath.Join(t.TempDir(), "packages", pkgName, pkgVer)
	pkgBin := filepath.Join(pkgRoot, "bin")
	util.CreateDirIfNotExist(pkgBin)

	// Create a dummy binary
	binaryName := "testapp"
	binaryPath := filepath.Join(pkgBin, binaryName)
	os.WriteFile(binaryPath, []byte("exec"), 0755)

	// 2. Test Normal Link
	linked, err := h.LinkPackageBinaries(pkgName, pkgVer, pkgRoot)
	if err != nil {
		t.Fatalf("LinkPackageBinaries failed: %v", err)
	}

	if len(linked) != 1 || linked[0] != binaryName {
		t.Errorf("Expected linked binaries [%s], got %v", binaryName, linked)
	}

	targetLink := filepath.Join(h.binPath, binaryName)
	if !util.FileExists(targetLink) {
		t.Error("Symlink not created")
	}

	dest, _ := os.Readlink(targetLink)
	if dest != binaryPath {
		t.Errorf("Symlink destination mismatch. Got %s, want %s", dest, binaryPath)
	}

	// 3. Test Overwrite existing dropzone link
	// Simulate upgrading package (v2)
	pkgVer2 := "2.0.0"
	pkgRoot2 := filepath.Join(t.TempDir(), "packages", pkgName, pkgVer2)
	pkgBin2 := filepath.Join(pkgRoot2, "bin")
	util.CreateDirIfNotExist(pkgBin2)
	binaryPath2 := filepath.Join(pkgBin2, binaryName)
	os.WriteFile(binaryPath2, []byte("exec2"), 0755)

	linked, err = h.LinkPackageBinaries(pkgName, pkgVer2, pkgRoot2)
	if err != nil {
		t.Fatalf("LinkPackageBinaries update failed: %v", err)
	}
	if len(linked) != 1 {
		t.Error("Expected update to report linked binary")
	}

	dest, _ = os.Readlink(targetLink)
	if dest != binaryPath2 {
		t.Errorf("Symlink not updated. Got %s, want %s", dest, binaryPath2)
	}

	// 4. Test Conflict with System Binary
	// Create a fake system bin dir
	sysBinDir := filepath.Join(t.TempDir(), "sysbin")
	util.CreateDirIfNotExist(sysBinDir)
	sysToolName := "systemtool"
	os.WriteFile(filepath.Join(sysBinDir, sysToolName), []byte("system"), 0755)

	// Add to PATH for this test
	originalPath := os.Getenv("PATH")
	defer os.Setenv("PATH", originalPath)
	os.Setenv("PATH", sysBinDir+string(os.PathListSeparator)+originalPath)

	// Create package providing same tool
	pkgSysToolPath := filepath.Join(pkgBin, sysToolName)
	os.WriteFile(pkgSysToolPath, []byte("pkg"), 0755)

	linked, err = h.LinkPackageBinaries(pkgName, pkgVer, pkgRoot)
	if err != nil {
		t.Fatalf("LinkPackageBinaries conflict test failed: %v", err)
	}

	// Should NOT be linked
	found := false
	for _, l := range linked {
		if l == sysToolName {
			found = true
		}
	}
	if found {
		t.Error("Should not link binary that conflicts with system path")
	}

	if util.FileExists(filepath.Join(h.binPath, sysToolName)) {
		t.Error("Symlink created despite conflict with system binary")
	}
}

func TestUnlinkPackageBinaries(t *testing.T) {
	h := setupTestIntegrator(t)
	h.SetupDropzoneBinPath()

	// Create dummy symlinks
	// Link 1: Correct package and version
	// Since we need relative path detection in Unlink logic (contains check),
	// we just need the readlink to return a path containing the structure.
	// We can create dummy files to link to, or just ensure readlink works.
	// However, os.Symlink requires the target to just be a string, valid or not (on most FS).
	// Let's create dummy targets to be safe.
	dummyTargetDir := filepath.Join(h.basePath, "packages", "pkgA", "1.0.0", "bin")
	util.CreateDirIfNotExist(dummyTargetDir)
	dummyTargetFile := filepath.Join(dummyTargetDir, "tool1")
	os.WriteFile(dummyTargetFile, []byte(""), 0755)

	link1 := filepath.Join(h.binPath, "tool1")
	os.Symlink(dummyTargetFile, link1)

	// Link 2: Same package, different version
	dummyTargetDir2 := filepath.Join(h.basePath, "packages", "pkgA", "2.0.0", "bin")
	util.CreateDirIfNotExist(dummyTargetDir2)
	dummyTargetFile2 := filepath.Join(dummyTargetDir2, "tool1") // Same binary name, diff version
	os.WriteFile(dummyTargetFile2, []byte(""), 0755)

	link2 := filepath.Join(h.binPath, "tool1_v2") // artificially named link
	os.Symlink(dummyTargetFile2, link2)

	// Unlink v1.0.0
	if err := h.UnlinkPackageBinaries("pkgA", "1.0.0"); err != nil {
		t.Fatalf("UnlinkPackageBinaries failed: %v", err)
	}

	if util.FileExists(link1) {
		t.Error("tool1 (v1.0.0) should have been unlinked")
	}
	if !util.FileExists(link2) {
		t.Error("tool1_v2 (v2.0.0) should NOT have been unlinked")
	}
}

func TestVerifyRuntime(t *testing.T) {
	h := setupTestIntegrator(t)

	// Test with a command that definitely exists (e.g., "git")
	// "git" supports --version, which is what VerifyRuntime checks.
	if err := h.VerifyRuntime("git"); err != nil {
		t.Errorf("VerifyRuntime('git') failed: %v", err)
	}

	// Test with non-existent command
	if err := h.VerifyRuntime("nonexistentcommand_xyz"); err == nil {
		t.Error("VerifyRuntime should fail for non-existent command")
	}
}
