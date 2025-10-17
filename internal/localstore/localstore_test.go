package localstore

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dropzone/internal/util"
)

func setupTestStore(t *testing.T) *LocalStore {
	tmpDir := t.TempDir()
	store := New(tmpDir)
	if err := store.Init(); err != nil {
		t.Fatalf("Failed to init store: %v", err)
	}
	return store
}

func TestInit(t *testing.T) {
	tmpDir := t.TempDir()
	store := New(tmpDir)

	if err := store.Init(); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	dirs := []string{
		store.BinPath(),
		store.PackagesPath(),
		store.ConfigPath(),
		store.IndexPath(),
	}

	for _, dir := range dirs {
		if !util.FileExists(dir) {
			t.Errorf("Expected directory %s to exist", dir)
		}
	}
}

func TestStorePackageAndMetadata(t *testing.T) {
	store := setupTestStore(t)

	// Create dummy package content
	pkgName := "testpkg"
	pkgVersion := "1.0.0"
	tmpPkgDir := t.TempDir()
	binDir := filepath.Join(tmpPkgDir, "bin")
	os.Mkdir(binDir, 0755)
	os.WriteFile(filepath.Join(binDir, "app"), []byte("executable"), 0755)

	// Test StorePackage
	destPath, err := store.StorePackage(pkgName, pkgVersion, tmpPkgDir)
	if err != nil {
		t.Fatalf("StorePackage failed: %v", err)
	}

	expectedPath := store.GetPackagePath(pkgName, pkgVersion)
	if destPath != expectedPath {
		t.Errorf("Expected path %s, got %s", expectedPath, destPath)
	}

	if !util.FileExists(filepath.Join(destPath, "bin", "app")) {
		t.Error("Package content not found in store")
	}

	// Test StorePackageMetadata
	meta := PackageMetadata{
		Name:        pkgName,
		Version:     pkgVersion,
		Checksum:    "sha256:dummy",
		InstallDate: time.Now(),
	}

	if err := store.StorePackageMetadata(meta); err != nil {
		t.Fatalf("StorePackageMetadata failed: %v", err)
	}

	// Test GetPackageMetadata
	retrievedMeta, err := store.GetPackageMetadata(pkgName, pkgVersion)
	if err != nil {
		t.Fatalf("GetPackageMetadata failed: %v", err)
	}

	if retrievedMeta.Name != meta.Name || retrievedMeta.Checksum != meta.Checksum {
		t.Errorf("Metadata mismatch. Got %+v, want %+v", retrievedMeta, meta)
	}
}

func TestListPackages(t *testing.T) {
	store := setupTestStore(t)

	pkgs := []PackageMetadata{
		{Name: "pkgA", Version: "1.0", Checksum: "c1"},
		{Name: "pkgA", Version: "2.0", Checksum: "c2"},
		{Name: "pkgB", Version: "1.0", Checksum: "c3"},
	}

	for _, p := range pkgs {
		// Mock package directory creation
		pkgDir := store.GetPackagePath(p.Name, p.Version)
		util.CreateDirIfNotExist(pkgDir)
		store.StorePackageMetadata(p)
	}

	// Test GetAllInstalledPackages
	installed, err := store.GetAllInstalledPackages()
	if err != nil {
		t.Fatalf("GetAllInstalledPackages failed: %v", err)
	}

	if len(installed) != 3 {
		t.Errorf("Expected 3 installed packages, got %d", len(installed))
	}

	// Test GetInstalledPackageVersions
	versions, err := store.GetInstalledPackageVersions("pkgA")
	if err != nil {
		t.Fatalf("GetInstalledPackageVersions failed: %v", err)
	}
	if len(versions) != 2 {
		t.Errorf("Expected 2 versions for pkgA, got %d", len(versions))
	}
}

func TestRemovePackage(t *testing.T) {
	store := setupTestStore(t)
	pkgName := "toremove"
	pkgVersion := "1.0.0"

	// Setup package
	pkgDir := store.GetPackagePath(pkgName, pkgVersion)
	util.CreateDirIfNotExist(pkgDir)
	store.StorePackageMetadata(PackageMetadata{Name: pkgName, Version: pkgVersion})

	if err := store.RemovePackageFiles(pkgName, pkgVersion); err != nil {
		t.Fatalf("RemovePackageFiles failed: %v", err)
	}

	if util.FileExists(pkgDir) {
		t.Error("Package directory should be removed")
	}

	// Parent directory should be removed if empty
	parent := filepath.Dir(pkgDir)
	if util.FileExists(parent) {
		t.Error("Package parent directory should be removed if empty")
	}
}

func TestIndexOperations(t *testing.T) {
	store := setupTestStore(t)

	index := map[string][]PackageMetadata{
		"pkgRemote": {
			{Name: "pkgRemote", Version: "1.0", Checksum: "r1"},
		},
	}

	cpName := "test-cp"
	if err := store.StoreControlPlaneIndex(cpName, index); err != nil {
		t.Fatalf("StoreControlPlaneIndex failed: %v", err)
	}

	// Get Index
	retrieved, err := store.GetControlPlaneIndex(cpName)
	if err != nil {
		t.Fatalf("GetControlPlaneIndex failed: %v", err)
	}
	if len(retrieved["pkgRemote"]) != 1 {
		t.Error("Failed to retrieve correct index")
	}

	// Get All Available
	all, err := store.GetAllAvailablePackagesFromIndexes()
	if err != nil {
		t.Fatalf("GetAllAvailablePackagesFromIndexes failed: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("Expected 1 available package, got %d", len(all))
	}
}
