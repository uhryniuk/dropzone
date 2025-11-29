package packagehandler

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/uhryniuk/dropzone/internal/builder"
	"github.com/uhryniuk/dropzone/internal/config"
	"github.com/uhryniuk/dropzone/internal/controlplane"
	"github.com/uhryniuk/dropzone/internal/hostintegration"
	"github.com/uhryniuk/dropzone/internal/localstore"
	"github.com/uhryniuk/dropzone/internal/util"
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

	cfg, _ := config.DefaultConfig()
	cpManager, _ := controlplane.NewManager(cfg, store)

	return New(store, integrator, b, cpManager), store, integrator
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

// mockCP implements ControlPlane for testing InstallPackage
type mockCP struct {
	name string
	pkgs map[string]localstore.PackageMetadata
}

func (m *mockCP) Name() string                      { return m.name }
func (m *mockCP) Type() string                      { return "mock-cp" }
func (m *mockCP) Endpoint() string                  { return "mock://endpoint" }
func (m *mockCP) Authenticate(u, p, t string) error { return nil }
func (m *mockCP) ListPackageNames() ([]string, error) {
	var names []string
	for k := range m.pkgs {
		names = append(names, k)
	}
	return names, nil
}
func (m *mockCP) GetPackageTags(packageName string) ([]string, error) {
	if p, ok := m.pkgs[packageName]; ok {
		return []string{p.Version}, nil
	}
	return nil, nil
}
func (m *mockCP) GetPackageMetadata(packageName, tag string) (*localstore.PackageMetadata, error) {
	if p, ok := m.pkgs[packageName]; ok {
		// Return copy
		return &p, nil
	}
	return nil, nil
}
func (m *mockCP) DownloadArtifact(packageName, tag, destinationPath string) error {
	// Create bin directory and file
	binDir := filepath.Join(destinationPath, "bin")
	os.MkdirAll(binDir, 0755)
	os.WriteFile(filepath.Join(binDir, packageName), []byte("content"), 0755)
	return nil
}

func TestInstallPackage(t *testing.T) {
	// Register mock factory
	controlplane.RegisterFactory("mock-cp", func(cfg config.ControlPlaneConfig) (controlplane.ControlPlane, error) {
		return &mockCP{
			name: cfg.Name,
			pkgs: map[string]localstore.PackageMetadata{
				"remotePkg": {
					Name:       "remotePkg",
					Version:    "1.0.0",
					SourceRepo: cfg.Name,
					Checksum:   "dummy", // Will cause checksum mismatch
				},
			},
		}, nil
	})

	h, _, _ := setupHandler(t, "/bin/true")

	// Add repo
	err := h.cpManager.Add("test-repo", "mock-cp", "mock://endpoint", config.AuthOptions{})
	if err != nil {
		t.Fatalf("Failed to add repo: %v", err)
	}

	// Update index
	if err := h.cpManager.UpdateAll(); err != nil {
		t.Fatalf("UpdateAll failed: %v", err)
	}

	// Install
	// We expect failure due to attestation verification (checksum mismatch/no signature)
	err = h.InstallPackage("remotePkg:1.0.0")
	if err == nil {
		t.Fatal("Expected InstallPackage to fail due to attestation verification (mock), but succeeded")
	}

	// Check if error is about attestation
	if !strings.Contains(err.Error(), "attestation verification failed") {
		t.Errorf("Expected attestation error, got: %v", err)
	}
}
