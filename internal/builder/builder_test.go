package builder

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// createMockRuntime creates a temporary shell script that acts as the container runtime.
// It returns the path to the executable script.
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

func TestVerifyRuntime(t *testing.T) {
	// Success case
	mockSuccess := createMockRuntime(t, `
if [ "$1" = "--version" ]; then
	exit 0
fi
exit 1
`)
	bSuccess := New(mockSuccess)
	if err := bSuccess.VerifyRuntime(); err != nil {
		t.Errorf("VerifyRuntime failed with valid runtime: %v", err)
	}

	// Failure case
	mockFailure := createMockRuntime(t, `exit 1`)
	bFailure := New(mockFailure)
	if err := bFailure.VerifyRuntime(); err == nil {
		t.Error("VerifyRuntime should fail with invalid runtime")
	}
}

func TestBuildAndExtract(t *testing.T) {
	// Mock runtime that simulates successful build, create, cp, and rm
	behavior := `#!/bin/sh
set -e
cmd="$1"
case "$cmd" in
	build)
		# Verify some args if necessary, but mostly just succeed
		# We could output something to verify args were passed
		exit 0
		;;
	create)
		echo "mock-container-id"
		exit 0
		;;
	cp)
		# cp <src> <dest>
		# $3 is destination directory.
		# Create a dummy file there to simulate extraction.
		dest_dir="$3"
		mkdir -p "$dest_dir/bin"
		touch "$dest_dir/bin/testapp"
		exit 0
		;;
	rm)
		exit 0
		;;
	*)
		exit 1
		;;
esac
`
	mockRuntime := createMockRuntime(t, behavior)
	b := New(mockRuntime)

	tmpDir := t.TempDir()
	dockerfile := filepath.Join(tmpDir, "Dockerfile")
	if err := os.WriteFile(dockerfile, []byte("FROM alpine"), 0644); err != nil {
		t.Fatalf("Failed to create dummy dockerfile: %v", err)
	}

	extractedPath, err := b.BuildAndExtract(dockerfile, tmpDir, "testpkg", "1.0.0", nil, nil)
	if err != nil {
		t.Fatalf("BuildAndExtract failed: %v", err)
	}
	defer os.RemoveAll(extractedPath)

	// Verify extracted content
	expectedFile := filepath.Join(extractedPath, "bin", "testapp")
	if _, err := os.Stat(expectedFile); os.IsNotExist(err) {
		t.Errorf("Expected extracted file %s does not exist", expectedFile)
	}
}
