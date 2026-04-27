package app

import (
	"bytes"
	"strings"
	"testing"
)

// Phase 0 smoke: `dropzone version` prints a non-empty version string and
// does not attempt to initialize the (stubbed) app context.
func TestVersionCommand(t *testing.T) {
	a := New()
	root := a.SetupCommands()

	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"version"})

	if err := root.Execute(); err != nil {
		t.Fatalf("version command failed: %v", err)
	}

	got := out.String()
	if !strings.HasPrefix(got, "dropzone ") {
		t.Errorf("unexpected version output: %q", got)
	}
	if strings.TrimSpace(got) == "dropzone" {
		t.Error("version output has empty version string")
	}
}

// All commands have real implementations as of phase 8 -- there's
// nothing left to gate on the not-reimplemented sentinel. The CLI
// surface is exercised by command-specific tests:
//   - install:    install_e2e_test.go
//   - login/out:  login_test.go
//   - registries: cli_registries_test.go
//   - update:     cli_update_test.go
// Plus per-package unit tests for the underlying logic.
