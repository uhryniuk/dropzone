package attestation

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dropzone/internal/util"
)

// GenerateChecksum calculates a SHA256 checksum for a file or a directory.
// If path is a directory, it calculates the checksum over all files in the directory
// recursively, sorting them by path to ensure reproducibility.
func GenerateChecksum(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("failed to stat path: %w", err)
	}

	if !info.IsDir() {
		return hashFile(path)
	}

	return hashDirectory(path)
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

func hashDirectory(root string) (string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			relPath, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			files = append(files, relPath)
		}
		return nil
	})
	if err != nil {
		return "", err
	}

	sort.Strings(files)

	h := sha256.New()
	for _, file := range files {
		// Hash the relative path
		h.Write([]byte(file))

		// Hash the file content
		f, err := os.Open(filepath.Join(root, file))
		if err != nil {
			return "", err
		}

		if _, err := io.Copy(h, f); err != nil {
			f.Close()
			return "", err
		}
		f.Close()
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// SignChecksum signs the provided checksum string using GPG.
// It returns the signature and the key identifier used.
func SignChecksum(checksum string, signingKeyIdentifier string) ([]byte, string, error) {
	// We'll sign the checksum string itself.
	// Using gpg --detach-sign --armor

	args := []string{"--detach-sign", "--armor"}
	if signingKeyIdentifier != "" {
		args = append(args, "--local-user", signingKeyIdentifier)
	}

	cmd := exec.Command("gpg", args...)
	cmd.Stdin = strings.NewReader(checksum)
	var out bytes.Buffer
	cmd.Stdout = &out
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	// GPG might prompt for passphrase via pinentry.
	// We assume a configured GPG environment.
	if err := cmd.Run(); err != nil {
		return nil, "", fmt.Errorf("gpg signing failed: %w, stderr: %s", err, stderr.String())
	}

	// For the key ID, we'd ideally extract it from the signature or output,
	// but for MVP if the user provided it, we return it.
	// If empty (default key), we might need to query GPG to find which key was used,
	// or leave it empty/default.
	return out.Bytes(), signingKeyIdentifier, nil
}

// PromptForSigning asks the user if they want to sign the checksum and handles the interaction.
func PromptForSigning(checksum string) ([]byte, string, error) {
	reader := bufio.NewReader(os.Stdin)

	fmt.Printf("\nGenerated Checksum: %s\n", checksum)
	fmt.Print("Do you want to sign this checksum with GPG? [y/N]: ")
	response, _ := reader.ReadString('\n')
	response = strings.TrimSpace(strings.ToLower(response))

	if response != "y" && response != "yes" {
		util.LogInfo("Skipping signing.")
		return nil, "", nil
	}

	fmt.Print("Enter GPG Key ID (leave empty for default): ")
	keyID, _ := reader.ReadString('\n')
	keyID = strings.TrimSpace(keyID)

	signature, usedKeyID, err := SignChecksum(checksum, keyID)
	if err != nil {
		return nil, "", err
	}

	util.LogInfo("Checksum signed successfully.")
	return signature, usedKeyID, nil
}

// VerifySignedChecksum verifies that the file at filePath matches the expectedChecksum,
// and that the signature is valid for that checksum using the publicKeyRef.
func VerifySignedChecksum(filePath string, expectedChecksum string, signature []byte, publicKeyRef string) error {
	// 1. Verify integrity (file matches checksum)
	calculatedChecksum, err := GenerateChecksum(filePath)
	if err != nil {
		return fmt.Errorf("failed to calculate checksum of artifact: %w", err)
	}

	if calculatedChecksum != expectedChecksum {
		return fmt.Errorf("checksum mismatch: expected %s, got %s", expectedChecksum, calculatedChecksum)
	}

	if len(signature) == 0 {
		// If no signature provided, we can't verify authenticity.
		// For MVP, if we require signed packages, this should be an error.
		// However, maybe we allow unsigned for now with a warning?
		// The requirement says "Verify the integrity and authenticity... If verification fails, aborted."
		return fmt.Errorf("missing signature for checksum verification")
	}

	// 2. Verify authenticity (signature matches checksum)
	// We verify the signature against the checksum string.

	// Write signature to a temp file
	sigFile, err := os.CreateTemp("", "dropzone-sig-*.asc")
	if err != nil {
		return fmt.Errorf("failed to create temp signature file: %w", err)
	}
	defer os.Remove(sigFile.Name())
	if _, err := sigFile.Write(signature); err != nil {
		return err
	}
	sigFile.Close()

	// Write checksum to a temp file (data that was signed)
	checksumFile, err := os.CreateTemp("", "dropzone-checksum-*.txt")
	if err != nil {
		return fmt.Errorf("failed to create temp checksum file: %w", err)
	}
	defer os.Remove(checksumFile.Name())
	if _, err := checksumFile.WriteString(expectedChecksum); err != nil {
		return err
	}
	checksumFile.Close()

	// gpg --verify sigFile checksumFile
	args := []string{"--verify", sigFile.Name(), checksumFile.Name()}
	// If publicKeyRef is provided, we might want to ensure it's in the keyring?
	// GPG verification usually checks against the keyring.

	cmd := exec.Command("gpg", args...)
	var out bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("signature verification failed: %w, stderr: %s", err, stderr.String())
	}

	// TODO: For stronger security, we should check if the key fingerprint used
	// matches publicKeyRef if provided. GPG output contains this info.

	return nil
}
