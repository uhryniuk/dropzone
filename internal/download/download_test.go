package download

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/uhryniuk/dropzone/internal/config"
)

func TestDownloadFile_Success(t *testing.T) {
	content := []byte("test content")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write(content)
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	destPath := filepath.Join(tmpDir, "downloaded.txt")

	err := DownloadFile(server.URL, destPath, nil)
	if err != nil {
		t.Fatalf("DownloadFile failed: %v", err)
	}

	got, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatalf("Failed to read downloaded file: %v", err)
	}

	if string(got) != string(content) {
		t.Errorf("Content mismatch. Got %s, want %s", got, content)
	}
}

func TestDownloadFile_AuthToken(t *testing.T) {
	token := "secret-token"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		expected := "Bearer " + token
		if auth != expected {
			http.Error(w, fmt.Sprintf("Expected 'Authorization: %s', got '%s'", expected, auth), http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	destPath := filepath.Join(tmpDir, "downloaded.txt")

	authOpts := &config.AuthOptions{Token: token}
	err := DownloadFile(server.URL, destPath, authOpts)
	if err != nil {
		t.Fatalf("DownloadFile with token failed: %v", err)
	}
}

func TestDownloadFile_BasicAuth(t *testing.T) {
	username := "user"
	password := "pass"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || u != username || p != password {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	destPath := filepath.Join(tmpDir, "downloaded.txt")

	authOpts := &config.AuthOptions{Username: username, Password: password}
	err := DownloadFile(server.URL, destPath, authOpts)
	if err != nil {
		t.Fatalf("DownloadFile with basic auth failed: %v", err)
	}
}

func TestDownloadFile_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	destPath := filepath.Join(tmpDir, "downloaded.txt")

	err := DownloadFile(server.URL, destPath, nil)
	if err == nil {
		t.Fatal("Expected error for 404, got nil")
	}

	expectedError := "bad status: 404 Not Found"
	if err.Error() != expectedError {
		t.Errorf("Expected error '%s', got '%v'", expectedError, err)
	}
}

func TestDownloadFile_NetworkError(t *testing.T) {
	tmpDir := t.TempDir()
	destPath := filepath.Join(tmpDir, "downloaded.txt")

	// Use an invalid URL to force a network error (or immediate failure)
	err := DownloadFile("http://localhost:0", destPath, nil)
	if err == nil {
		t.Fatal("Expected network error, got nil")
	}
}
