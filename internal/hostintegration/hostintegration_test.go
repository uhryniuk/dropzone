package hostintegration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// wrapperContent builds a minimally-valid shim wrapper for tests: starts
// with the marker line that InstallWrapper uses to identify our files.
func wrapperContent(name string) string {
	return "#!/bin/sh\n# dropzone-wrapper " + name + " sha256:abc\nexec /bin/true\n"
}

func TestInstallWrapperWritesExecutableFile(t *testing.T) {
	base := t.TempDir()
	h := New(base)

	if err := h.InstallWrapper("jq", wrapperContent("jq")); err != nil {
		t.Fatalf("InstallWrapper: %v", err)
	}
	info, err := os.Stat(filepath.Join(base, "bin", "jq"))
	if err != nil {
		t.Fatalf("stat wrapper: %v", err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Errorf("mode: got %o, want 0755", info.Mode().Perm())
	}
}

func TestInstallWrapperOverwritesOtherDropzoneWrapper(t *testing.T) {
	// Update flow: a wrapper already exists from a previous install and
	// gets replaced with the new content. Same marker → overwrite allowed.
	base := t.TempDir()
	h := New(base)

	_ = h.InstallWrapper("jq", wrapperContent("jq"))
	newContent := "#!/bin/sh\n# dropzone-wrapper jq sha256:def\nexec /bin/true\n"
	if err := h.InstallWrapper("jq", newContent); err != nil {
		t.Fatalf("second InstallWrapper: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(base, "bin", "jq"))
	if !strings.Contains(string(got), "sha256:def") {
		t.Errorf("wrapper not replaced: %s", got)
	}
}

func TestInstallWrapperRefusesNonDropzoneFile(t *testing.T) {
	// Users may have their own scripts at ~/.dropzone/bin/<name>; dropzone
	// must not silently overwrite them.
	base := t.TempDir()
	h := New(base)
	binDir := filepath.Join(base, "bin")
	_ = os.MkdirAll(binDir, 0o755)
	userScript := filepath.Join(binDir, "jq")
	if err := os.WriteFile(userScript, []byte("#!/bin/sh\necho not dropzone\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := h.InstallWrapper("jq", wrapperContent("jq")); err == nil {
		t.Fatal("expected error when overwriting non-dropzone file")
	}
	got, _ := os.ReadFile(userScript)
	if !strings.Contains(string(got), "not dropzone") {
		t.Error("user's script was overwritten")
	}
}

func TestRemoveWrapperOnlyRemovesDropzoneFiles(t *testing.T) {
	base := t.TempDir()
	h := New(base)

	_ = h.InstallWrapper("jq", wrapperContent("jq"))
	if err := h.RemoveWrapper("jq"); err != nil {
		t.Fatalf("RemoveWrapper: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, "bin", "jq")); !os.IsNotExist(err) {
		t.Errorf("wrapper should be gone, stat err=%v", err)
	}

	binDir := filepath.Join(base, "bin")
	userScript := filepath.Join(binDir, "user")
	_ = os.WriteFile(userScript, []byte("not dropzone"), 0o755)
	if err := h.RemoveWrapper("user"); err == nil {
		t.Error("expected RemoveWrapper to refuse non-dropzone files")
	}
	if _, err := os.Stat(userScript); err != nil {
		t.Errorf("user script should still exist: %v", err)
	}
}

func TestRemoveWrapperMissingIsNoOp(t *testing.T) {
	base := t.TempDir()
	h := New(base)
	_ = os.MkdirAll(filepath.Join(base, "bin"), 0o755)
	if err := h.RemoveWrapper("never-installed"); err != nil {
		t.Errorf("RemoveWrapper on missing wrapper should be no-op, got %v", err)
	}
}

func TestSetupShellRCIsIdempotent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SHELL", "/bin/zsh")

	h := New(filepath.Join(home, ".dropzone"))
	wrote, rc, err := h.SetupShellRC()
	if err != nil {
		t.Fatalf("SetupShellRC: %v", err)
	}
	if !wrote {
		t.Error("first SetupShellRC should report changes")
	}
	if rc != filepath.Join(home, ".zshrc") {
		t.Errorf("rc path: got %q", rc)
	}
	data1, _ := os.ReadFile(rc)
	if !strings.Contains(string(data1), RCMarkerStart) {
		t.Fatal("marker not written")
	}

	wrote, _, err = h.SetupShellRC()
	if err != nil {
		t.Fatalf("SetupShellRC second call: %v", err)
	}
	if wrote {
		t.Error("second SetupShellRC should report no changes")
	}
	data2, _ := os.ReadFile(rc)
	if string(data1) != string(data2) {
		t.Errorf("rc file changed on idempotent run:\nbefore: %q\nafter:  %q", data1, data2)
	}
}

func TestUnsetShellRCRemovesOnlyTheMarkedBlock(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SHELL", "/bin/zsh")
	rc := filepath.Join(home, ".zshrc")

	before := "# user alias\nalias ll='ls -al'\n"
	after := "# more user stuff\nexport EDITOR=vim\n"
	h := New(filepath.Join(home, ".dropzone"))
	if err := os.WriteFile(rc, []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.SetupShellRC(); err != nil {
		t.Fatal(err)
	}
	f, _ := os.OpenFile(rc, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString(after)
	f.Close()

	removed, _, err := h.UnsetShellRC()
	if err != nil {
		t.Fatalf("UnsetShellRC: %v", err)
	}
	if !removed {
		t.Error("UnsetShellRC should report removal")
	}

	got, _ := os.ReadFile(rc)
	if strings.Contains(string(got), RCMarkerStart) || strings.Contains(string(got), RCMarkerEnd) {
		t.Errorf("marker block still present: %q", got)
	}
	if !strings.Contains(string(got), "alias ll=") || !strings.Contains(string(got), "EDITOR=vim") {
		t.Errorf("user content was touched: %q", got)
	}
}

func TestUnsetShellRCOnMissingBlockIsNoOp(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SHELL", "/bin/zsh")
	rc := filepath.Join(home, ".zshrc")
	original := "# user's zshrc, no dropzone block\n"
	_ = os.WriteFile(rc, []byte(original), 0o644)

	h := New(filepath.Join(home, ".dropzone"))
	removed, _, err := h.UnsetShellRC()
	if err != nil {
		t.Fatalf("UnsetShellRC: %v", err)
	}
	if removed {
		t.Error("UnsetShellRC on missing block should be no-op")
	}
	got, _ := os.ReadFile(rc)
	if string(got) != original {
		t.Errorf("file touched unnecessarily: %q", got)
	}
}

func TestSetupShellRCOnUnsupportedShellErrors(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SHELL", "/usr/bin/fish")

	h := New(filepath.Join(home, ".dropzone"))
	_, _, err := h.SetupShellRC()
	if err == nil {
		t.Error("expected error for unsupported shell")
	}
}

func TestPathStatusReflectsEnv(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SHELL", "/bin/zsh")
	binDir := filepath.Join(home, ".dropzone", "bin")

	h := New(filepath.Join(home, ".dropzone"))

	t.Setenv("PATH", "/usr/bin:/bin")
	status := h.PathStatus()
	if status.OnPath {
		t.Error("OnPath should be false when bin dir isn't in PATH")
	}
	if status.RCBlockInstalled {
		t.Error("RCBlockInstalled should be false without a block")
	}

	t.Setenv("PATH", binDir+":/usr/bin")
	if _, _, err := h.SetupShellRC(); err != nil {
		t.Fatal(err)
	}
	status = h.PathStatus()
	if !status.OnPath {
		t.Error("OnPath should be true after PATH update")
	}
	if !status.RCBlockInstalled {
		t.Error("RCBlockInstalled should be true after SetupShellRC")
	}
}
