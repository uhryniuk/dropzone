package download

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/dropzone/internal/config"
	"github.com/dropzone/internal/util"
)

// DownloadFile downloads a file from the given URL to the destination path.
// It supports optional authentication via AuthOptions (Bearer Token or Basic Auth).
func DownloadFile(url string, destinationPath string, auth *config.AuthOptions) error {
	// Ensure destination directory exists
	if err := util.CreateDirIfNotExist(filepath.Dir(destinationPath)); err != nil {
		return fmt.Errorf("failed to create destination directory: %w", err)
	}

	// Create the file
	out, err := os.Create(destinationPath)
	if err != nil {
		return fmt.Errorf("failed to create destination file: %w", err)
	}
	defer out.Close()

	// Create the HTTP request
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return fmt.Errorf("failed to create HTTP request: %w", err)
	}

	// Apply Authentication if provided
	if auth != nil {
		if auth.Token != "" {
			req.Header.Set("Authorization", "Bearer "+auth.Token)
		} else if auth.Username != "" || auth.Password != "" {
			req.SetBasicAuth(auth.Username, auth.Password)
		}
	}

	// Use a client with a reasonable timeout for large files
	client := &http.Client{
		Timeout: 60 * time.Minute,
	}

	// Execute the request
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download request failed: %w", err)
	}
	defer resp.Body.Close()

	// Check server response
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("bad status: %s", resp.Status)
	}

	// Write the body to file
	_, err = io.Copy(out, resp.Body)
	if err != nil {
		return fmt.Errorf("failed to write file content: %w", err)
	}

	return nil
}
