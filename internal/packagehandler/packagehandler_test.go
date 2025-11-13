package packagehandler

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/dropzone/internal/builder"
	"github.com/dropzone/internal/hostintegration"
	"github.com/dropzone/internal/localstore"
	"github.com/dropzone/internal/util"
)

// createMockRuntime creates a temporary shell script that acts as the container runtime.
// Copied/adapted for testing PackageHandler's interaction with Builder.
func createMockRuntime(t *testing.T, behavior string) string {
	if runtime.GOOS == "windows" {
		t.Skip("Skipping test on Windows due to shell script usage")
	}

	tmpDir := t.TempDir()
	mockPath := filepath.Join(tmpDir, "mock-runtime")

	script := "#!/bin/sh\n" + behavior

	err := os.WriteFile(mockPath, []byte(script), 0755)
	if err != nil {
		t.Fatalf("Failed to create mock runtime: %v", err)
	}

	return mockPath
}

func setupHandler(t *testing.T, mockRuntimePath string) (*PackageHandler, *localstore.LocalStore, *hostintegration.HostIntegrator) {
	tmpDir := t.TempDir()
	store := localstore.New(filepath.Join(tmpDir, "store"))
	if err := store.Init(); err != nil {
		t.Fatalf("Store init failed: %v", err)
	}

	integrator := hostintegration.New(filepath.Join(tmpDir, "home"))
	// Ignore PATH warning in tests
	integrator.SetupDropzoneBinPath()

	b := builder.New(mockRuntimePath)

	return New(store, integrator, b), store, integrator
}

func TestBuildPackage(t *testing.T) {
	// Mock runtime behavior for build flow
	behavior := `#!/bin/sh
set -e
cmd="$1"
case "$cmd" in
	build) exit 0 ;;
	create) echo "mock-container-id"; exit 0 ;;
	cp)
		# cp <src> <dest>
		# $3 is destination directory.
		dest_dir="$3"
		mkdir -p "$dest_dir/bin"
		touch "$dest_dir/bin/myapp"
		exit 0
		;;
	rm) exit 0 ;;
	*) exit 1 ;;
esac
`
	mockRuntime := createMockRuntime(t, behavior)
	h, store, _ := setupHandler(t, mockRuntime)

	// Create dummy Dockerfile
	tmpDir := t.TempDir()
	dockerfile := filepath.Join(tmpDir, "Dockerfile")
	if err := os.WriteFile(dockerfile, []byte("FROM alpine"), 0644); err != nil {
		t.Fatalf("Failed to write dockerfile: %v", err)
	}

	// Mock stdin for signing prompt (answer "n" for no signing)
	r, w, _ := os.Pipe()
	oldStdin := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = oldStdin }()

	go func() {
		w.Write([]byte("n\n"))
		w.Close()
	}()

	err := h.BuildPackage("myapp", dockerfile, tmpDir, nil, nil)
	if err != nil {
		t.Fatalf("BuildPackage failed: %v", err)
	}

	// Verify store has package
	versions, err := store.GetInstalledPackageVersions("myapp")
	if err != nil {
		t.Fatalf("Failed to get versions: %v", err)
	}
	if len(versions) != 1 {
		t.Errorf("Expected 1 installed version, got %d", len(versions))
	}

	pkgPath := store.GetPackagePath("myapp", versions[0].Version)
	if !util.FileExists(filepath.Join(pkgPath, "bin", "myapp")) {
		t.Error("Package content not stored correctly")
	}
}

func TestListPackages(t *testing.T) {
	// Runtime irrelevant here, pass empty string or existing path to avoid errors if constructor checks
	h, store, _ := setupHandler(t, "/bin/true")

	// Add installed package
	meta1 := localstore.PackageMetadata{Name: "pkgInstalled", Version: "1.0", SourceRepo: "local"}

	// Create dir for it so it's picked up by GetAllInstalledPackages
	pkgPath := store.GetPackagePath("pkgInstalled", "1.0")
	util.CreateDirIfNotExist(pkgPath)
	store.StorePackageMetadata(meta1)

	// Add remote index
	index := map[string][]localstore.PackageMetadata{
		"pkgAvailable": {{Name: "pkgAvailable", Version: "2.0", SourceRepo: "remote"}},
	}
	store.StoreControlPlaneIndex("remote", index)

	// Capture stdout
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := h.ListPackages(false, false, "", "")

	w.Close()
	os.Stdout = oldStdout

	if err != nil {
		t.Fatalf("ListPackages failed: %v", err)
	}

	var buf bytes.Buffer
	io.Copy(&buf, r)
	output := buf.String()

	if !strings.Contains(output, "pkgInstalled") {
		t.Error("Output missing installed package")
	}
	if !strings.Contains(output, "pkgAvailable") {
		t.Error("Output missing available package")
	}
}

func TestRemovePackage_SpecificVersion(t *testing.T) {
	h, store, _ := setupHandler(t, "/bin/true")

	pkgName := "myapp"
	pkgVer := "1.0.0"

	// Setup package
	pkgDir := store.GetPackagePath(pkgName, pkgVer)
	util.CreateDirIfNotExist(pkgDir)
	store.StorePackageMetadata(localstore.PackageMetadata{Name: pkgName, Version: pkgVer})

	err := h.RemovePackage(pkgName, pkgVer)
	if err != nil {
		t.Fatalf("RemovePackage failed: %v", err)
	}

	if util.FileExists(pkgDir) {
		t.Error("Package directory should be removed")
	}
}

func TestRemovePackage_Interactive(t *testing.T) {
	h, store, _ := setupHandler(t, "/bin/true")

	pkgName := "myapp"

	// Setup 2 versions
	v1 := "1.0.0"
	v2 := "2.0.0"

	for _, v := range []string{v1, v2} {
		pkgDir := store.GetPackagePath(pkgName, v)
		util.CreateDirIfNotExist(pkgDir)
		store.StorePackageMetadata(localstore.PackageMetadata{Name: pkgName, Version: v})
	}

	// Mock stdin: select option 1.
	// Since ReadDir usually sorts by name, 1.0.0 is index 0 (option 1).
	r, w, _ := os.Pipe()
	oldStdin := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = oldStdin }()

	go func() {
		// Prompt asks for number.
		w.Write([]byte("1\n"))
		w.Close()
	}()

	err := h.RemovePackage(pkgName, "")
	if err != nil {
		t.Fatalf("RemovePackage interactive failed: %v", err)
	}

	// One version should remain
	versions, _ := store.GetInstalledPackageVersions(pkgName)
	if len(versions) != 1 {
		t.Errorf("Expected 1 version remaining, got %d", len(versions))
	}
	// We removed v1 (1.0.0), so v2 should remain
	if versions[0].Version != v2 {
		t.Errorf("Expected remaining version to be %s, got %s", v2, versions[0].Version)
	}
}
