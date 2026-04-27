package app

import (
	"bytes"
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	gcrregistry "github.com/google/go-containerregistry/pkg/registry"

	"github.com/uhryniuk/dropzone/internal/packagehandler"
)

// dz tags resolves three ways. We exercise the metadata-driven one
// here because it's the new behavior and the most error-prone:
// without it, `dz tags <name>` would query the default registry even
// for packages installed from somewhere else, silently giving wrong
// answers.
func TestTagsCommandResolvesViaInstalledMetadata(t *testing.T) {
	srv := httptest.NewServer(gcrregistry.New())
	t.Cleanup(srv.Close)
	hostAddr := strings.TrimPrefix(srv.URL, "http://")

	// Push tool:1.0 and tool:2.0 to the in-process registry, install
	// tool:1.0, then call dz tags tool. The default registry the App
	// is configured with happens to be this same fake registry, but
	// even so the metadata-driven path should be the one used (we
	// confirm by routing through resolveTagsTarget directly rather
	// than the CLI in-band).
	pushOneLayer(t, hostAddr, "tool", "1.0", []byte("v1"), srv.Client().Transport)
	pushOneLayer(t, hostAddr, "tool", "2.0", []byte("v2"), srv.Client().Transport)

	a := buildAppForRegistry(t, srv, hostAddr)
	if _, err := a.PackageHandler.InstallPackage(context.Background(), "tool:1.0", packagehandler.InstallOptions{AllowUnsigned: true}); err != nil {
		if strings.Contains(err.Error(), "dynamic loader") {
			t.Skipf("dynamic test binary; skipping: %v", err)
		}
		t.Fatalf("install: %v", err)
	}

	reg, image, err := a.resolveTagsTarget("tool", "")
	if err != nil {
		t.Fatalf("resolveTagsTarget: %v", err)
	}
	if reg.URL != hostAddr {
		t.Errorf("registry URL: got %q, want %q (the registry the package was installed from)", reg.URL, hostAddr)
	}
	if image != "tool" {
		t.Errorf("image path: got %q, want %q", image, "tool")
	}
}

func TestTagsCommandResolvesHostnameQualifiedRefDirectly(t *testing.T) {
	// dz tags gitea.example.com/owner/repo should not require a
	// configured registry. resolveTagsTarget should produce an
	// ephemeral Registry for the hostname, same as install does.
	a := New()
	root := a.SetupCommands()
	var dump bytes.Buffer
	root.SetOut(&dump)
	root.SetErr(&dump)
	t.Setenv("HOME", t.TempDir())
	root.SetArgs([]string{"version"}) // trigger Initialize without exec'ing
	_ = root.Execute()
	// version skips Initialize. Force initialization explicitly so
	// resolveTagsTarget has a manager and a default registry.
	if err := a.Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	reg, image, err := a.resolveTagsTarget("gitea.example.com/owner/repo", "")
	if err != nil {
		t.Fatalf("resolveTagsTarget: %v", err)
	}
	if reg.URL != "gitea.example.com" {
		t.Errorf("registry URL: got %q, want gitea.example.com", reg.URL)
	}
	if image != "owner/repo" {
		t.Errorf("image path: got %q, want owner/repo", image)
	}
}

func TestTagsCommandRegistryFlagOverridesEverything(t *testing.T) {
	// --registry should win even if the argument also matches an
	// installed package or looks like a hostname.
	a := New()
	root := a.SetupCommands()
	var dump bytes.Buffer
	root.SetOut(&dump)
	root.SetErr(&dump)
	t.Setenv("HOME", t.TempDir())
	root.SetArgs([]string{"version"})
	_ = root.Execute()
	if err := a.Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	// chainguard is the default-seeded registry. Even with a
	// hostname-shaped argument, --registry should pin to it.
	reg, image, err := a.resolveTagsTarget("gitea.example.com/owner/repo", "chainguard")
	if err != nil {
		t.Fatalf("resolveTagsTarget: %v", err)
	}
	if reg.Name != "chainguard" {
		t.Errorf("registry name: got %q, want chainguard", reg.Name)
	}
	// The full arg becomes the image path under the chosen registry.
	// Slightly weird semantically but it's what --registry implies.
	if image != "gitea.example.com/owner/repo" {
		t.Errorf("image path: got %q, want gitea.example.com/owner/repo", image)
	}
}
