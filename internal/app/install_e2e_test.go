package app

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	gcrregistry "github.com/google/go-containerregistry/pkg/registry"
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

// End-to-end install: a go-containerregistry in-process registry serves
// a minimal image whose entrypoint is the running test binary (so it's
// guaranteed to be an ELF/Mach-O matching the host CPU). InstallPackage
// pulls, shims, and stores; we verify every on-disk artifact the design
// docs commit to. Signature verification is deferred to phase 4, so this
// test goes straight from pull to shim build.
func TestInstallEndToEnd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	srv := httptest.NewServer(gcrregistry.New())
	t.Cleanup(srv.Close)
	hostAddr := strings.TrimPrefix(srv.URL, "http://")

	// Push a minimal image with the test binary as its entrypoint.
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	binary, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	var tarBuf bytes.Buffer
	tw := tar.NewWriter(&tarBuf)
	_ = tw.WriteHeader(&tar.Header{
		Name: "usr/bin/tool", Mode: 0o755,
		Size: int64(len(binary)), Typeflag: tar.TypeReg,
	})
	_, _ = tw.Write(binary)
	_ = tw.Close()
	tarBytes := tarBuf.Bytes()

	layer, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(tarBytes)), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	img, err := mutate.AppendLayers(empty.Image, layer)
	if err != nil {
		t.Fatal(err)
	}
	imgCfg, _ := img.ConfigFile()
	imgCfg = imgCfg.DeepCopy()
	imgCfg.OS = runtime.GOOS
	imgCfg.Architecture = runtime.GOARCH
	imgCfg.Config.Entrypoint = []string{"/usr/bin/tool"}
	img, err = mutate.ConfigFile(img, imgCfg)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := name.ParseReference(hostAddr + "/tool:latest")
	if err != nil {
		t.Fatal(err)
	}
	if err := gcrremote.Write(ref, img, gcrremote.WithTransport(srv.Client().Transport)); err != nil {
		t.Fatalf("push image: %v", err)
	}

	// Wire an App pointed at the fake registry as default.
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

	// AllowUnsigned because the test image is unsigned and the fake
	// registry has no policy. This exercises the no-policy + opt-in path.
	result, err := a.PackageHandler.InstallPackage(context.Background(), "tool", packagehandler.InstallOptions{AllowUnsigned: true})
	if err != nil {
		// Dynamically-linked host binaries require their loader to be
		// present inside the bundled rootfs. We only bundled the binary
		// itself, so shim.Build rightly rejects when PT_INTERP points at
		// a loader we didn't stage. In that case the shim logic is still
		// behaving correctly — skip so the e2e test stays meaningful on
		// statically-linked CI hosts without flaking on others.
		if strings.Contains(err.Error(), "dynamic loader") {
			t.Skipf("test binary is dynamically linked; end-to-end skipped: %v", err)
		}
		t.Fatalf("InstallPackage: %v", err)
	}

	// Wrapper exists, is executable, has our marker.
	wrapperPath := filepath.Join(home, ".dropzone", "bin", "tool")
	info, err := os.Stat(wrapperPath)
	if err != nil {
		t.Fatalf("wrapper not written: %v", err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Errorf("wrapper not executable: mode=%o", info.Mode())
	}
	wrapperContent, _ := os.ReadFile(wrapperPath)
	if !strings.Contains(string(wrapperContent), "# dropzone-wrapper tool") {
		t.Errorf("wrapper missing marker: %s", wrapperContent)
	}

	// Rootfs under packages/tool/<digest-dir>/rootfs/.
	pkgDir := filepath.Join(home, ".dropzone", "packages", "tool")
	currentDir, err := os.Readlink(filepath.Join(pkgDir, "current"))
	if err != nil {
		t.Fatalf("current symlink missing: %v", err)
	}
	rootfsEntry := filepath.Join(pkgDir, currentDir, "rootfs", "usr", "bin", "tool")
	if _, err := os.Stat(rootfsEntry); err != nil {
		t.Errorf("entrypoint not in rootfs: %v", err)
	}

	// Metadata captures name + digest.
	metaBytes, err := os.ReadFile(filepath.Join(pkgDir, currentDir, "metadata.json"))
	if err != nil {
		t.Fatalf("metadata missing: %v", err)
	}
	if !strings.Contains(string(metaBytes), `"name": "tool"`) {
		t.Errorf("metadata missing name: %s", metaBytes)
	}
	if !strings.Contains(string(metaBytes), result.Digest) {
		t.Errorf("metadata missing digest %q: %s", result.Digest, metaBytes)
	}

	// ListInstalled reflects the new install.
	list, err := a.PackageHandler.ListInstalled()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Name != "tool" {
		t.Errorf("ListInstalled: got %+v", list)
	}

	// Remove cleans wrapper + package dir.
	if err := a.PackageHandler.RemovePackage("tool"); err != nil {
		t.Fatalf("RemovePackage: %v", err)
	}
	if _, err := os.Stat(wrapperPath); !os.IsNotExist(err) {
		t.Errorf("wrapper should be gone: stat err=%v", err)
	}
	if _, err := os.Stat(pkgDir); !os.IsNotExist(err) {
		t.Errorf("package dir should be gone: stat err=%v", err)
	}
}
