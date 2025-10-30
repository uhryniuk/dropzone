package attestation

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateChecksum_File(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "test.txt")
	content := []byte("hello world")
	if err := os.WriteFile(filePath, content, 0644); err != nil {
		t.Fatalf("Failed to write file: %v", err)
	}

	sum, err := GenerateChecksum(filePath)
	if err != nil {
		t.Fatalf("GenerateChecksum failed: %v", err)
	}

	// SHA256 of "hello world"
	// echo -n "hello world" | shasum -a 256
	expected := "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9"
	if sum != expected {
		t.Errorf("Expected checksum %s, got %s", expected, sum)
	}
}

func TestGenerateChecksum_Directory(t *testing.T) {
	tmpDir := t.TempDir()
	dirPath := filepath.Join(tmpDir, "testdir")
	if err := os.Mkdir(dirPath, 0755); err != nil {
		t.Fatalf("Failed to create dir: %v", err)
	}

	// Create files
	file1 := filepath.Join(dirPath, "a.txt")
	file2 := filepath.Join(dirPath, "b.txt")
	// subdir
	subDir := filepath.Join(dirPath, "subdir")
	os.Mkdir(subDir, 0755)
	file3 := filepath.Join(subDir, "c.txt")

	os.WriteFile(file1, []byte("foo"), 0644)
	os.WriteFile(file2, []byte("bar"), 0644)
	os.WriteFile(file3, []byte("baz"), 0644)

	sum1, err := GenerateChecksum(dirPath)
	if err != nil {
		t.Fatalf("GenerateChecksum failed: %v", err)
	}

	// Test Reproducibility (create identical structure elsewhere)
	tmpDir2 := t.TempDir()
	dirPath2 := filepath.Join(tmpDir2, "testdir2")
	os.Mkdir(dirPath2, 0755)
	os.Mkdir(filepath.Join(dirPath2, "subdir"), 0755)
	os.WriteFile(filepath.Join(dirPath2, "a.txt"), []byte("foo"), 0644)
	os.WriteFile(filepath.Join(dirPath2, "b.txt"), []byte("bar"), 0644)
	os.WriteFile(filepath.Join(dirPath2, "subdir", "c.txt"), []byte("baz"), 0644)

	sum2, err := GenerateChecksum(dirPath2)
	if err != nil {
		t.Fatalf("GenerateChecksum 2 failed: %v", err)
	}

	if sum1 != sum2 {
		t.Errorf("Checksums should match for identical directories. Got %s and %s", sum1, sum2)
	}

	// Test Change (modify one file)
	os.WriteFile(filepath.Join(dirPath2, "a.txt"), []byte("foo2"), 0644)
	sum3, err := GenerateChecksum(dirPath2)
	if err != nil {
		t.Fatalf("GenerateChecksum 3 failed: %v", err)
	}

	if sum1 == sum3 {
		t.Error("Checksums should differ when content changes")
	}
}

func TestVerifySignedChecksum_PreChecks(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "artifact")
	os.WriteFile(filePath, []byte("content"), 0644)

	realSum, _ := GenerateChecksum(filePath)
	badSum := "0000000000000000000000000000000000000000000000000000000000000000"

	// Test Mismatch
	err := VerifySignedChecksum(filePath, badSum, []byte("sig"), "")
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("Expected checksum mismatch error, got: %v", err)
	}

	// Test Missing Signature
	err = VerifySignedChecksum(filePath, realSum, nil, "")
	if err == nil || !strings.Contains(err.Error(), "missing signature") {
		t.Errorf("Expected missing signature error, got: %v", err)
	}
}
