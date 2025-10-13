package util

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// CreateDirIfNotExist creates a directory if it does not exist with 0755 permissions.
func CreateDirIfNotExist(path string) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return os.MkdirAll(path, 0755)
	}
	return nil
}

// CopyFile copies a file from src to dest. It preserves the file mode.
func CopyFile(src, dest string) error {
	sourceFile, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("failed to open source file: %w", err)
	}
	defer sourceFile.Close()

	// Ensure destination directory exists
	destDir := filepath.Dir(dest)
	if err := CreateDirIfNotExist(destDir); err != nil {
		return fmt.Errorf("failed to create destination directory: %w", err)
	}

	destFile, err := os.Create(dest)
	if err != nil {
		return fmt.Errorf("failed to create destination file: %w", err)
	}
	defer destFile.Close()

	if _, err = io.Copy(destFile, sourceFile); err != nil {
		return fmt.Errorf("failed to copy file content: %w", err)
	}

	sourceInfo, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("failed to get source file info: %w", err)
	}

	if err := os.Chmod(dest, sourceInfo.Mode()); err != nil {
		return fmt.Errorf("failed to set file mode: %w", err)
	}

	return nil
}

// RemovePath removes a file or directory recursively.
func RemovePath(path string) error {
	return os.RemoveAll(path)
}

// FileExists checks if a file or directory exists.
func FileExists(path string) bool {
	_, err := os.Stat(path)
	return !os.IsNotExist(err)
}

// GetHomeDir returns the user's home directory.
func GetHomeDir() (string, error) {
	return os.UserHomeDir()
}

// Basic logging interface

// LogInfo prints an informational message to stdout.
func LogInfo(format string, v ...interface{}) {
	fmt.Printf("[INFO] "+format+"\n", v...)
}

// LogError prints an error message to stderr.
func LogError(format string, v ...interface{}) {
	fmt.Fprintf(os.Stderr, "[ERROR] "+format+"\n", v...)
}

// LogDebug prints a debug message to stdout.
func LogDebug(format string, v ...interface{}) {
	fmt.Printf("[DEBUG] "+format+"\n", v...)
}
