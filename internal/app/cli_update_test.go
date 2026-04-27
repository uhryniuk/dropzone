package app

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	gcrregistry "github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	gcrremote "github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"

	"github.com/uhryniuk/dropzone/internal/config"
	"github.com/uhryniuk/dropzone/internal/hostintegration"
	"github.com/uhryniuk/dropzone/internal/localstore"
	"github.com/uhryniuk/dropzone/internal/packagehandler"
	"github.com/uhryniuk/dropzone/internal/registry"
)

// pushOneLayer publishes a single-layer image whose entrypoint is the
// running test binary (guaranteed to be a valid ELF/Mach-O for the
// host). marker is a file we write *alongside* the binary so two
// pushes with different markers produce different image digests --
// that's the same-tag drift case `dz update` needs to detect.
func pushOneLayer(t *testing.T, host, repo, tag string, marker []byte, transport http.RoundTripper) v1.Hash {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	binary, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	_ = tw.WriteHeader(&tar.Header{Name: "usr/bin/tool", Mode: 0o755, Size: int64(len(binary)), Typeflag: tar.TypeReg})
	_, _ = tw.Write(binary)
	_ = tw.WriteHeader(&tar.Header{Name: "etc/marker", Mode: 0o644, Size: int64(len(marker)), Typeflag: tar.TypeReg})
	_, _ = tw.Write(marker)
	_ = tw.Close()
	bs := buf.Bytes()

	layer, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(bs)), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	img, err := mutate.AppendLayers(empty.Image, layer)
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ := img.ConfigFile()
	cfg = cfg.DeepCopy()
	cfg.OS = runtime.GOOS
	cfg.Architecture = runtime.GOARCH
	cfg.Config.Entrypoint = []string{"/usr/bin/tool"}
	img, err = mutate.ConfigFile(img, cfg)
	if err != nil {
		t.Fatal(err)
	}
	ref, _ := name.ParseReference(host + "/" + repo + ":" + tag)
	if err := gcrremote.Write(ref, img, gcrremote.WithTransport(transport)); err != nil {
		t.Fatalf("push %s:%s: %v", repo, tag, err)
	}
	d, _ := img.Digest()
	return d
}

// buildAppForRegistry wires up an App pointed at a fake in-process
// registry. Used by the update tests to install once, change the image,
// and re-check.
func buildAppForRegistry(t *testing.T, srv *httptest.Server, hostAddr string) *App {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfg := &config.Config{
		LocalStorePath:  filepath.Join(home, ".dropzone"),
		DefaultRegistry: "fake",
		Registries:      []config.RegistryConfig{{Name: "fake", URL: hostAddr}},
	}
	store := localstore.New(cfg.LocalStorePath)
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	integrator := hostintegration.New(cfg.LocalStorePath)
	client := registry.NewClient("").WithTransport(srv.Client().Transport)
	regMgr := registry.NewManager(cfg, func() error { return nil }, client, nil)

	a := &App{
		Config:          cfg,
		LocalStore:      store,
		HostIntegrator:  integrator,
		RegistryManager: regMgr,
	}
	a.PackageHandler = packagehandler.New(store, integrator, regMgr, nil)
	return a
}

func TestCheckUpdateDetectsSameTagDigestDrift(t *testing.T) {
	srv := httptest.NewServer(gcrregistry.New())
	t.Cleanup(srv.Close)
	hostAddr := strings.TrimPrefix(srv.URL, "http://")

	// First publish + install.
	pushOneLayer(t, hostAddr, "tool", "latest", []byte("v1-body"), srv.Client().Transport)

	a := buildAppForRegistry(t, srv, hostAddr)
	if _, err := a.PackageHandler.InstallPackage(context.Background(), "tool", packagehandler.InstallOptions{AllowUnsigned: true}); err != nil {
		// Same-host-binary trick like install_e2e: if the test binary is
		// dynamically linked, shim.Build needs the loader inside the
		// rootfs. We don't have it — skip rather than fail.
		if strings.Contains(err.Error(), "dynamic loader") {
			t.Skipf("test binary is dynamically linked; update e2e skipped: %v", err)
		}
		t.Fatalf("InstallPackage v1: %v", err)
	}

	// Push a different image at the same tag. Same name + tag, new
	// digest -- the drift case.
	pushOneLayer(t, hostAddr, "tool", "latest", []byte("v2-body-different"), srv.Client().Transport)

	info, err := a.PackageHandler.CheckUpdate(context.Background(), "tool")
	if err != nil {
		t.Fatalf("CheckUpdate: %v", err)
	}
	if !info.SameTagRebuild() {
		t.Errorf("expected same-tag drift to be detected; got %+v", info)
	}
	if info.InstalledDigest == info.CurrentDigest {
		t.Errorf("digests should differ across pushes")
	}
}

func TestCheckUpdateDetectsNewerTags(t *testing.T) {
	srv := httptest.NewServer(gcrregistry.New())
	t.Cleanup(srv.Close)
	hostAddr := strings.TrimPrefix(srv.URL, "http://")

	// Install at "1.0".
	pushOneLayer(t, hostAddr, "tool", "1.0", []byte("v1"), srv.Client().Transport)

	a := buildAppForRegistry(t, srv, hostAddr)
	if _, err := a.PackageHandler.InstallPackage(context.Background(), "tool:1.0", packagehandler.InstallOptions{AllowUnsigned: true}); err != nil {
		if strings.Contains(err.Error(), "dynamic loader") {
			t.Skipf("test binary is dynamically linked; skipping: %v", err)
		}
		t.Fatalf("InstallPackage 1.0: %v", err)
	}

	// Add a "2.0" tag.
	pushOneLayer(t, hostAddr, "tool", "2.0", []byte("v2"), srv.Client().Transport)

	info, err := a.PackageHandler.CheckUpdate(context.Background(), "tool")
	if err != nil {
		t.Fatalf("CheckUpdate: %v", err)
	}
	found := false
	for _, t := range info.NewerTags {
		if t == "2.0" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected NewerTags to include 2.0, got %+v", info.NewerTags)
	}
}

func TestCheckUpdateUnreachableRegistryReportsErrorPerPackage(t *testing.T) {
	srv := httptest.NewServer(gcrregistry.New())
	hostAddr := strings.TrimPrefix(srv.URL, "http://")

	pushOneLayer(t, hostAddr, "tool", "latest", []byte("v1"), srv.Client().Transport)

	a := buildAppForRegistry(t, srv, hostAddr)
	if _, err := a.PackageHandler.InstallPackage(context.Background(), "tool", packagehandler.InstallOptions{AllowUnsigned: true}); err != nil {
		if strings.Contains(err.Error(), "dynamic loader") {
			t.Skipf("test binary is dynamically linked; skipping: %v", err)
		}
		t.Fatalf("install: %v", err)
	}
	// Tear the registry down — subsequent CheckUpdate sees a
	// network failure.
	srv.Close()

	infos, err := a.PackageHandler.CheckUpdates(context.Background())
	if err != nil {
		t.Fatalf("CheckUpdates: %v", err)
	}
	if len(infos) != 1 {
		t.Fatalf("expected 1 update info, got %d", len(infos))
	}
	if infos[0].UnreachableError == nil {
		t.Error("UnreachableError should be set when registry is down")
	}
}

func TestCheckUpdateExcludesFloatingTags(t *testing.T) {
	// Floating tags ("latest", "stable", etc.) point to a current
	// version, not a higher one. They should never be reported as
	// "newer" than what's installed.
	srv := httptest.NewServer(gcrregistry.New())
	t.Cleanup(srv.Close)
	hostAddr := strings.TrimPrefix(srv.URL, "http://")

	pushOneLayer(t, hostAddr, "tool", "1.0", []byte("v1"), srv.Client().Transport)
	pushOneLayer(t, hostAddr, "tool", "latest", []byte("v1"), srv.Client().Transport)

	a := buildAppForRegistry(t, srv, hostAddr)
	if _, err := a.PackageHandler.InstallPackage(context.Background(), "tool:1.0", packagehandler.InstallOptions{AllowUnsigned: true}); err != nil {
		if strings.Contains(err.Error(), "dynamic loader") {
			t.Skipf("dynamic test binary; skipping: %v", err)
		}
		t.Fatalf("install: %v", err)
	}

	info, _ := a.PackageHandler.CheckUpdate(context.Background(), "tool")
	for _, t := range info.NewerTags {
		if t == "latest" || t == "stable" || t == "edge" {
			// fail with the offending tag
			panic("floating tag leaked into NewerTags: " + t)
		}
	}
}
