package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runCmdInHome boots an App with HOME=tmp, runs `dz <args...>`, and
// returns captured output + the test home path. Each test gets its own
// home so config writes don't bleed between cases.
func runCmdInHome(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)

	a := New()
	root := a.SetupCommands()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)

	err := root.Execute()
	return out.String(), home, err
}

func TestAddRegistryWritesConfig(t *testing.T) {
	out, home, err := runCmdInHome(t,
		"add", "registry", "mycorp", "registry.mycorp.example/hardened",
		"--template", "github",
		"--identity-regex", "https://github.com/mycorp/.*",
	)
	if err != nil {
		t.Fatalf("add registry: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Added registry \"mycorp\"") {
		t.Errorf("expected confirmation in output: %q", out)
	}
	if !strings.Contains(out, "(signed)") {
		t.Errorf("expected (signed) marker: %q", out)
	}

	cfg, _ := os.ReadFile(filepath.Join(home, ".dropzone", "config", "config.yaml"))
	if !strings.Contains(string(cfg), "name: mycorp") {
		t.Errorf("config missing mycorp entry: %s", cfg)
	}
	if !strings.Contains(string(cfg), "https://github.com/mycorp/.*") {
		t.Errorf("config missing identity regex: %s", cfg)
	}
}

func TestAddRegistryWithoutPolicyMarksUnsigned(t *testing.T) {
	out, home, err := runCmdInHome(t,
		"add", "registry", "hub", "docker.io",
	)
	if err != nil {
		t.Fatalf("add registry: %v\n%s", err, out)
	}
	if !strings.Contains(out, "(unsigned, requires --allow-unsigned at install)") {
		t.Errorf("expected unsigned marker: %q", out)
	}
	cfg, _ := os.ReadFile(filepath.Join(home, ".dropzone", "config", "config.yaml"))
	if strings.Contains(string(cfg), "cosign_policy") && strings.Contains(string(cfg), "name: hub") {
		// The chainguard seed has cosign_policy; we only object if the
		// "hub" entry itself carries one.
		// Quick scan: locate the hub entry and confirm it has no policy
		// in the immediately following lines.
		idx := strings.Index(string(cfg), "name: hub")
		if idx >= 0 {
			tail := string(cfg)[idx:]
			endIdx := strings.Index(tail, "name:")
			if endIdx == -1 {
				endIdx = len(tail)
			} else {
				// "name:" appears at idx and again later; we want the
				// next one.
				endIdx = strings.Index(tail[len("name: hub"):], "name:")
				if endIdx == -1 {
					endIdx = len(tail)
				}
			}
			if strings.Contains(tail[:endIdx], "cosign_policy") {
				t.Errorf("hub entry should not have cosign_policy: %s", tail[:endIdx])
			}
		}
	}
}

func TestAddRegistryDuplicateNameErrors(t *testing.T) {
	_, _, err := runCmdInHome(t,
		"add", "registry", "chainguard", "docker.io",
	)
	if err == nil {
		t.Error("expected error adding duplicate registry name")
	}
}

func TestAddRegistryDefaultFlagSwitchesDefault(t *testing.T) {
	out, home, err := runCmdInHome(t,
		"add", "registry", "newdefault", "docker.io", "--default",
	)
	if err != nil {
		t.Fatalf("add registry: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Set \"newdefault\" as the default") {
		t.Errorf("expected default-switch message: %q", out)
	}
	cfg, _ := os.ReadFile(filepath.Join(home, ".dropzone", "config", "config.yaml"))
	if !strings.Contains(string(cfg), "default_registry: newdefault") {
		t.Errorf("config didn't switch default: %s", cfg)
	}
}

func TestListRegistriesShowsConfiguredEntries(t *testing.T) {
	// Add a custom one, then list. Should show both seeded chainguard
	// and the new entry.
	home := t.TempDir()
	t.Setenv("HOME", home)

	a := New()
	root := a.SetupCommands()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"add", "registry", "extra", "extra.example"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}

	a2 := New()
	root2 := a2.SetupCommands()
	var listOut bytes.Buffer
	root2.SetOut(&listOut)
	root2.SetErr(&listOut)
	root2.SetArgs([]string{"list", "registries"})
	if err := root2.Execute(); err != nil {
		t.Fatalf("list registries: %v", err)
	}
	got := listOut.String()
	if !strings.Contains(got, "chainguard") {
		t.Errorf("missing chainguard: %q", got)
	}
	if !strings.Contains(got, "extra") {
		t.Errorf("missing extra: %q", got)
	}
	// Default marker should be on the chainguard line.
	for _, line := range strings.Split(got, "\n") {
		if strings.Contains(line, "chainguard") && !strings.Contains(line, "*") {
			t.Errorf("chainguard line missing * default marker: %q", line)
		}
	}
}

func TestRemoveRegistrySucceedsOnEmptyState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Add then remove.
	a := New()
	root := a.SetupCommands()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"add", "registry", "extra", "extra.example"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}

	a2 := New()
	root2 := a2.SetupCommands()
	var rmOut bytes.Buffer
	root2.SetOut(&rmOut)
	root2.SetErr(&rmOut)
	root2.SetArgs([]string{"remove", "registry", "extra"})
	if err := root2.Execute(); err != nil {
		t.Fatalf("remove registry: %v", err)
	}
	if !strings.Contains(rmOut.String(), `Removed registry "extra"`) {
		t.Errorf("expected removal message: %q", rmOut.String())
	}
}

func TestRemoveRegistryUnknownErrors(t *testing.T) {
	_, _, err := runCmdInHome(t, "remove", "registry", "never-added")
	if err == nil {
		t.Error("expected error removing unknown registry")
	}
}
