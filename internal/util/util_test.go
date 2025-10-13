package util

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCreateDirIfNotExist(t *testing.T) {
	tmpDir := t.TempDir()
	targetPath := filepath.Join(tmpDir, "testdir")

	err := CreateDirIfNotExist(targetPath)
	if err != nil {
		t.Fatalf("CreateDirIfNotExist failed: %v", err)
	}

	info, err := os.Stat(targetPath)
	if err != nil {
		t.Fatalf("Failed to stat created directory: %v", err)
	}

	if !info.IsDir() {
		t.Errorf("Expected path to be a directory")
	}

	// Test idempotency
	err = CreateDirIfNotExist(targetPath)
	if err != nil {
		t.Errorf("CreateDirIfNotExist should not fail if directory exists: %v", err)
	}
}

func TestCopyFile(t *testing.T) {
	tmpDir := t.TempDir()
	srcPath := filepath.Join(tmpDir, "src.txt")
	destPath := filepath.Join(tmpDir, "dest.txt")

	content := []byte("hello world")
	if err := os.WriteFile(srcPath, content, 0644); err != nil {
		t.Fatalf("Failed to create source file: %v", err)
	}

	if err := CopyFile(srcPath, destPath); err != nil {
		t.Fatalf("CopyFile failed: %v", err)
	}

	destContent, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatalf("Failed to read destination file: %v", err)
	}

	if string(destContent) != string(content) {
		t.Errorf("Destination content mismatch. Got %s, want %s", destContent, content)
	}
}

func TestRemovePath(t *testing.T) {
	tmpDir := t.TempDir()
	targetPath := filepath.Join(tmpDir, "toremove")

	if err := os.Mkdir(targetPath, 0755); err != nil {
		t.Fatalf("Failed to create directory: %v", err)
	}

	if err := RemovePath(targetPath); err != nil {
		t.Fatalf("RemovePath failed: %v", err)
	}

	if _, err := os.Stat(targetPath); !os.IsNotExist(err) {
		t.Errorf("Path still exists after RemovePath")
	}
}

func TestFileExists(t *testing.T) {
	tmpDir := t.TempDir()
	existingPath := filepath.Join(tmpDir, "exists")
	nonExistingPath := filepath.Join(tmpDir, "doesnotexist")

	if err := os.Mkdir(existingPath, 0755); err != nil {
		t.Fatalf("Failed to create directory: %v", err)
	}

	if !FileExists(existingPath) {
		t.Errorf("FileExists should return true for existing path")
	}

	if FileExists(nonExistingPath) {
		t.Errorf("FileExists should return false for non-existing path")
	}
}

func TestGetHomeDir(t *testing.T) {
	dir, err := GetHomeDir()
	if err != nil {
		t.Fatalf("GetHomeDir failed: %v", err)
	}
	if dir == "" {
		t.Errorf("GetHomeDir returned empty string")
	}
}
