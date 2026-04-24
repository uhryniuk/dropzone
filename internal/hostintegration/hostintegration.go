// Package hostintegration manages the dropzone-owned parts of the host's
// user environment: wrapper scripts under ~/.dropzone/bin/ and optional
// PATH setup in the user's shell rc file.
//
// The PATH setup is an explicit user action via `dz path setup`; package
// installs never touch shell rc files on their own. This keeps the
// surprise-blast-radius of any install strictly inside ~/.dropzone/.
package hostintegration

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/uhryniuk/dropzone/internal/util"
)

// HostIntegrator owns the bin directory and shell rc manipulation.
type HostIntegrator struct {
	basePath string // ~/.dropzone
	binPath  string // ~/.dropzone/bin
	// homeDir is the user's home directory. Captured at construction so
	// tests can drive shell-rc paths deterministically without relying on
	// $HOME shifting between calls.
	homeDir string
}

// New builds a HostIntegrator rooted at basePath (typically ~/.dropzone).
func New(basePath string) *HostIntegrator {
	home, _ := util.GetHomeDir()
	return &HostIntegrator{
		basePath: basePath,
		binPath:  filepath.Join(basePath, "bin"),
		homeDir:  home,
	}
}

// BinPath returns the bin directory path (~/.dropzone/bin).
func (h *HostIntegrator) BinPath() string { return h.binPath }

// SetupDropzoneBinPath creates ~/.dropzone/bin if missing and prints
// advice when it isn't on PATH. Never modifies shell rc files; that's
// what `dz path setup` is for.
func (h *HostIntegrator) SetupDropzoneBinPath() error {
	if err := util.CreateDirIfNotExist(h.binPath); err != nil {
		return fmt.Errorf("create bin dir: %w", err)
	}
	if OnPath(h.binPath) {
		return nil
	}
	util.LogInfo("NOTE: %s is not in your PATH.", h.binPath)
	util.LogInfo("Run `dz path setup` to configure your shell, or add this manually:")
	util.LogInfo("  export PATH=%q", h.binPath+":$PATH")
	return nil
}

// OnPath reports whether dir is present as a component of the current
// $PATH (exact-match only, not a prefix or substring check).
func OnPath(dir string) bool {
	for _, p := range filepath.SplitList(os.Getenv("PATH")) {
		if p == dir {
			return true
		}
	}
	return false
}

// InstallWrapper writes wrapperContent to ~/.dropzone/bin/<name> with
// mode 0755, subject to the conflict policy:
//
//   - No file at that path: write it.
//   - File exists and has our marker comment: overwrite (typical update).
//   - File exists without our marker: skip and return an informational
//     error so the caller can surface it without aborting.
//
// The wrapperContent is produced by shim.GenerateWrapper; this function
// doesn't know or care about its internals.
func (h *HostIntegrator) InstallWrapper(name, wrapperContent string) error {
	if err := util.CreateDirIfNotExist(h.binPath); err != nil {
		return fmt.Errorf("ensure bin dir: %w", err)
	}
	target := filepath.Join(h.binPath, name)

	if existing, err := os.ReadFile(target); err == nil {
		// File exists. Overwrite only if it's one of ours.
		if !isDropzoneWrapper(existing) {
			return fmt.Errorf("refusing to overwrite %s: file exists and is not a dropzone wrapper", target)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat %s: %w", target, err)
	}

	if err := os.WriteFile(target, []byte(wrapperContent), 0o755); err != nil {
		return fmt.Errorf("write wrapper: %w", err)
	}
	return nil
}

// RemoveWrapper removes ~/.dropzone/bin/<name> if it is a dropzone-written
// wrapper. Files without our marker are left alone (user may have put
// something there intentionally). Returns a typed reason on skip.
func (h *HostIntegrator) RemoveWrapper(name string) error {
	target := filepath.Join(h.binPath, name)
	data, err := os.ReadFile(target)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read %s: %w", target, err)
	}
	if !isDropzoneWrapper(data) {
		return fmt.Errorf("refusing to remove %s: not a dropzone wrapper", target)
	}
	return os.Remove(target)
}

// isDropzoneWrapper reports whether content looks like a wrapper dropzone
// itself wrote. We require the marker comment to appear within the first
// few lines so a random file that happens to contain "# dropzone-wrapper"
// deep inside doesn't get overwritten.
func isDropzoneWrapper(content []byte) bool {
	const marker = "# dropzone-wrapper"
	// Scan only the first few lines for the marker; bound the work so a
	// 100MB random file doesn't cost us a full scan.
	scanner := bufio.NewScanner(strings.NewReader(string(content)))
	for line := 0; line < 5 && scanner.Scan(); line++ {
		if strings.HasPrefix(scanner.Text(), marker) {
			return true
		}
	}
	return false
}

// -- dz path subcommand support --

// RCMarkerStart and RCMarkerEnd bracket the dropzone-managed block in the
// user's shell rc file. Idempotency, detection on unset, and clarity for
// anyone reading the file all hinge on these strings.
const (
	RCMarkerStart = "# >>> dropzone PATH >>>"
	RCMarkerEnd   = "# <<< dropzone PATH <<<"
)

// PathStatus summarizes the user-visible state of PATH integration.
type PathStatus struct {
	BinDir           string // the dropzone bin directory
	OnPath           bool   // is BinDir currently on $PATH
	DetectedShell    string // "bash", "zsh", or "" when unknown
	RCFile           string // absolute path to the rc file we would edit ("" if unknown)
	RCBlockInstalled bool   // does RCFile contain the dropzone marker block
}

// PathStatus inspects the current state: whether ~/.dropzone/bin is on
// PATH, and whether we've already written the marker block to the user's
// rc file.
func (h *HostIntegrator) PathStatus() PathStatus {
	status := PathStatus{
		BinDir: h.binPath,
		OnPath: OnPath(h.binPath),
	}
	shell, rc := h.detectShell()
	status.DetectedShell = shell
	status.RCFile = rc
	if rc != "" {
		if data, err := os.ReadFile(rc); err == nil && bytes(data).containsMarker() {
			status.RCBlockInstalled = true
		}
	}
	return status
}

// SetupShellRC appends a dropzone-managed PATH block to the user's shell
// rc file. Idempotent: if the marker block is already present, no write
// happens and a clear "already set up" message is returned.
//
// Returns (wroteChanges, error). `wroteChanges` is false when the block
// was already present so the CLI can suppress the "open a new shell"
// nudge in the no-op case.
func (h *HostIntegrator) SetupShellRC() (bool, string, error) {
	shell, rc := h.detectShell()
	if rc == "" {
		return false, "", fmt.Errorf("unsupported shell %q: add %q to your PATH manually", shell, h.binPath+":$PATH")
	}

	if data, err := os.ReadFile(rc); err == nil && bytes(data).containsMarker() {
		return false, rc, nil
	}

	block := fmt.Sprintf("\n%s\nexport PATH=%q\n%s\n", RCMarkerStart, h.binPath+":$PATH", RCMarkerEnd)
	f, err := os.OpenFile(rc, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return false, rc, fmt.Errorf("open rc file: %w", err)
	}
	defer f.Close()
	if _, err := io.WriteString(f, block); err != nil {
		return false, rc, fmt.Errorf("append to rc: %w", err)
	}
	return true, rc, nil
}

// UnsetShellRC removes the dropzone-managed PATH block from the user's rc
// file. Returns (removedChanges, error). If the block wasn't present,
// `removedChanges` is false and no write happens.
func (h *HostIntegrator) UnsetShellRC() (bool, string, error) {
	_, rc := h.detectShell()
	if rc == "" {
		return false, "", errors.New("no supported shell detected; nothing to unset")
	}
	data, err := os.ReadFile(rc)
	if err != nil {
		if os.IsNotExist(err) {
			return false, rc, nil
		}
		return false, rc, fmt.Errorf("read rc: %w", err)
	}
	if !bytes(data).containsMarker() {
		return false, rc, nil
	}
	stripped := bytes(data).stripMarkerBlock()
	if err := os.WriteFile(rc, stripped, 0o644); err != nil {
		return false, rc, fmt.Errorf("write rc: %w", err)
	}
	return true, rc, nil
}

// detectShell returns ("bash"/"zsh"/"", absoluteRCPath). On macOS we
// prefer ~/.bash_profile for bash users since that's what login shells
// read there; we fall back to ~/.bashrc when ~/.bash_profile doesn't
// exist so non-login bash setups still work.
func (h *HostIntegrator) detectShell() (string, string) {
	shell := filepath.Base(os.Getenv("SHELL"))
	switch shell {
	case "zsh":
		return "zsh", filepath.Join(h.homeDir, ".zshrc")
	case "bash":
		bashProfile := filepath.Join(h.homeDir, ".bash_profile")
		if _, err := os.Stat(bashProfile); err == nil {
			return "bash", bashProfile
		}
		return "bash", filepath.Join(h.homeDir, ".bashrc")
	default:
		return shell, ""
	}
}

// bytes is a small helper type so we can hang rc-file utilities off a
// byte slice without exporting them.
type bytes []byte

func (b bytes) containsMarker() bool {
	return strings.Contains(string(b), RCMarkerStart)
}

// stripMarkerBlock returns the content with the dropzone marker block
// removed, plus any blank line immediately preceding the start marker so
// we don't leave a stray newline behind.
func (b bytes) stripMarkerBlock() []byte {
	s := string(b)
	startIdx := strings.Index(s, RCMarkerStart)
	if startIdx < 0 {
		return b
	}
	// Swallow one leading newline if present, to avoid leaving a blank
	// line where the block used to be.
	if startIdx > 0 && s[startIdx-1] == '\n' {
		startIdx--
	}
	endMarkerIdx := strings.Index(s[startIdx:], RCMarkerEnd)
	if endMarkerIdx < 0 {
		// Malformed: start marker without end. Leave content unchanged
		// to avoid corrupting the user's file.
		return b
	}
	endIdx := startIdx + endMarkerIdx + len(RCMarkerEnd)
	// Swallow the trailing newline after the end marker if present.
	if endIdx < len(s) && s[endIdx] == '\n' {
		endIdx++
	}
	return []byte(s[:startIdx] + s[endIdx:])
}
