package app

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runCmd runs the root cobra command with the given args against a fresh
// $HOME (temp dir) and returns captured stdout/stderr plus the resulting
// auth file path. stdin is supplied by the caller when needed.
func runCmd(t *testing.T, stdin string, args ...string) (string, string, error) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)

	a := New()
	root := a.SetupCommands()

	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(args)

	err := root.Execute()
	return stdout.String(), stderr.String(), err
}

func TestLoginWithFlagsWritesAuthFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	a := New()
	root := a.SetupCommands()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"login", "registry.mycorp.example", "-u", "alice", "-p", "hunter2"})
	if err := root.Execute(); err != nil {
		t.Fatalf("login: %v\n%s", err, out.String())
	}

	authPath := filepath.Join(home, ".dropzone", "auth.json")
	raw, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatalf("auth file not written: %v", err)
	}

	var parsed struct {
		Auths map[string]struct {
			Auth string `json:"auth"`
		} `json:"auths"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("parse auth file: %v", err)
	}
	entry, ok := parsed.Auths["registry.mycorp.example"]
	if !ok {
		t.Fatal("registry entry missing")
	}
	decoded, _ := base64.StdEncoding.DecodeString(entry.Auth)
	if string(decoded) != "alice:hunter2" {
		t.Errorf("auth payload: got %q, want alice:hunter2", decoded)
	}

	// File permissions keep the password out of casual view.
	info, _ := os.Stat(authPath)
	if info.Mode().Perm() != 0o600 {
		t.Errorf("auth file mode: got %o, want 0600", info.Mode().Perm())
	}

	if !strings.Contains(out.String(), "Saved credentials") {
		t.Errorf("login output missing confirmation: %q", out.String())
	}
}

func TestLoginPasswordStdin(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	a := New()
	root := a.SetupCommands()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetIn(strings.NewReader("s3cret\n"))
	root.SetArgs([]string{"login", "r.example", "-u", "alice", "--password-stdin"})
	if err := root.Execute(); err != nil {
		t.Fatalf("login: %v\n%s", err, out.String())
	}

	raw, err := os.ReadFile(filepath.Join(home, ".dropzone", "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Auths map[string]struct {
			Auth string `json:"auth"`
		} `json:"auths"`
	}
	_ = json.Unmarshal(raw, &parsed)
	decoded, _ := base64.StdEncoding.DecodeString(parsed.Auths["r.example"].Auth)
	if string(decoded) != "alice:s3cret" {
		t.Errorf("password-stdin auth: got %q", decoded)
	}
}

func TestLoginPromptsForUsernameAndPasswordFromStdin(t *testing.T) {
	// When stdin isn't a TTY (which is true under go test), the command
	// reads the password from stdin as a plain line. This test exercises
	// both the username prompt and the password fallback path.
	home := t.TempDir()
	t.Setenv("HOME", home)

	a := New()
	root := a.SetupCommands()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetIn(strings.NewReader("alice\nhunter2\n"))
	root.SetArgs([]string{"login", "r.example"})
	if err := root.Execute(); err != nil {
		t.Fatalf("login: %v\n%s", err, out.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".dropzone", "auth.json")); err != nil {
		t.Errorf("auth file not written: %v", err)
	}
}

func TestLogoutRemovesEntry(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Save first.
	a := New()
	root := a.SetupCommands()
	var dump bytes.Buffer
	root.SetOut(&dump)
	root.SetErr(&dump)
	root.SetArgs([]string{"login", "r.example", "-u", "alice", "-p", "hunter2"})
	if err := root.Execute(); err != nil {
		t.Fatalf("login: %v", err)
	}

	// Fresh App + SetupCommands for the logout call: cobra's flag state is
	// per-Command, and reusing root here would carry over the login flags.
	a2 := New()
	root2 := a2.SetupCommands()
	var out bytes.Buffer
	root2.SetOut(&out)
	root2.SetErr(&out)
	root2.SetArgs([]string{"logout", "r.example"})
	if err := root2.Execute(); err != nil {
		t.Fatalf("logout: %v\n%s", err, out.String())
	}

	if !strings.Contains(out.String(), "Removed credentials") {
		t.Errorf("logout output: %q", out.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".dropzone", "auth.json")); !os.IsNotExist(err) {
		t.Errorf("auth file should be removed when last entry is deleted, stat err=%v", err)
	}
}

func TestLogoutOnUnknownRegistryIsIdempotent(t *testing.T) {
	_, _, err := runCmd(t, "", "logout", "never.saved.example")
	if err != nil {
		t.Errorf("logout for unknown registry should succeed, got %v", err)
	}
}
