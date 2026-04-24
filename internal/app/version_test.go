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

// Every non-version command should return the not-reimplemented sentinel
// so users get a clear message instead of a crash during the transition.
func TestStubCommandsReturnNotReimplemented(t *testing.T) {
	cases := [][]string{
		{"install", "foo"},
		{"list"},
		{"remove", "foo"},
		{"update"},
		{"search"},
		{"tags", "foo"},
		{"add", "registry", "n", "u"},
	}

	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			a := New()
			root := a.SetupCommands()
			var out bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&out)
			root.SetArgs(args)

			// These commands hit PersistentPreRunE (which calls Initialize) before
			// returning the stub error. Initialize writes under $HOME, so point it
			// at a temp dir to keep the test hermetic.
			t.Setenv("HOME", t.TempDir())

			err := root.Execute()
			if err == nil {
				t.Fatalf("%v: expected an error, got nil", args)
			}
			if !strings.Contains(err.Error(), "not yet reimplemented") {
				t.Fatalf("%v: want not-reimplemented error, got %v", args, err)
			}
		})
	}
}
