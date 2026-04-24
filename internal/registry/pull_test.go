package registry

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
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
	"github.com/google/go-containerregistry/pkg/v1/types"
)

// startInProcessRegistry boots the go-containerregistry in-memory registry.
// Returns the host:port string and a Client configured to use its transport.
func startInProcessRegistry(t *testing.T) (string, *Client) {
	t.Helper()
	srv := httptest.NewServer(gcrregistry.New())
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	client := NewClient().WithTransport(srv.Client().Transport)
	return host, client
}

// tarLayer builds a v1.Layer from a set of (path, body) entries.
func tarLayer(t *testing.T, files map[string]string) v1.Layer {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for path, body := range files {
		hdr := &tar.Header{
			Name:     path,
			Mode:     0o755,
			Size:     int64(len(body)),
			Typeflag: tar.TypeReg,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("tar header: %v", err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatalf("tar body: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	bytesCopy := buf.Bytes()
	layer, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(bytesCopy)), nil
	})
	if err != nil {
		t.Fatalf("LayerFromOpener: %v", err)
	}
	return layer
}

// buildImage composes a single-layer image with the given Entrypoint and
// platform, plus the files in filesByPath.
func buildImage(t *testing.T, os string, arch string, entrypoint []string, files map[string]string) v1.Image {
	t.Helper()
	layer := tarLayer(t, files)
	img, err := mutate.AppendLayers(empty.Image, layer)
	if err != nil {
		t.Fatalf("AppendLayers: %v", err)
	}
	cfg, err := img.ConfigFile()
	if err != nil {
		t.Fatalf("ConfigFile: %v", err)
	}
	cfg = cfg.DeepCopy()
	cfg.OS = os
	cfg.Architecture = arch
	cfg.Config.Entrypoint = entrypoint
	img, err = mutate.ConfigFile(img, cfg)
	if err != nil {
		t.Fatalf("ConfigFile mutate: %v", err)
	}
	return img
}

func pushImage(t *testing.T, host, repo, tag string, img v1.Image, transport *Client) {
	t.Helper()
	ref, err := name.ParseReference(host + "/" + repo + ":" + tag)
	if err != nil {
		t.Fatalf("ParseReference: %v", err)
	}
	if err := gcrremote.Write(ref, img, transport.opts...); err != nil {
		t.Fatalf("remote.Write: %v", err)
	}
}

func pushIndex(t *testing.T, host, repo, tag string, idx v1.ImageIndex, transport *Client) {
	t.Helper()
	ref, err := name.ParseReference(host + "/" + repo + ":" + tag)
	if err != nil {
		t.Fatalf("ParseReference: %v", err)
	}
	if err := gcrremote.WriteIndex(ref, idx, transport.opts...); err != nil {
		t.Fatalf("remote.WriteIndex: %v", err)
	}
}

func TestPullSinglePlatformImageMatchingHost(t *testing.T) {
	host, client := startInProcessRegistry(t)

	img := buildImage(t, runtime.GOOS, runtime.GOARCH,
		[]string{"/usr/bin/tool"},
		map[string]string{
			"usr/bin/tool":   "#!/bin/echo\n",
			"etc/hello.conf": "hello from the rootfs",
		})
	pushImage(t, host, "tool", "latest", img, client)

	staging := t.TempDir()
	info, err := client.Pull(context.Background(),
		&ResolvedRef{Registry: &Registry{Name: "r", URL: host}, Image: "tool", Tag: "latest"},
		staging)
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}

	// Rootfs extracted.
	if got, err := os.ReadFile(filepath.Join(staging, "etc/hello.conf")); err != nil {
		t.Errorf("etc/hello.conf: %v", err)
	} else if string(got) != "hello from the rootfs" {
		t.Errorf("etc/hello.conf body: %q", got)
	}

	// Entrypoint surfaced from config.
	if len(info.Entrypoint) != 1 || info.Entrypoint[0] != "/usr/bin/tool" {
		t.Errorf("Entrypoint: %v", info.Entrypoint)
	}

	// Digest present and well-formed.
	if !strings.HasPrefix(info.Digest, "sha256:") {
		t.Errorf("Digest: %q", info.Digest)
	}

	// Platform string matches host.
	wantPlatform := runtime.GOOS + "/" + runtime.GOARCH
	if info.Platform != wantPlatform {
		t.Errorf("Platform: got %q, want %q", info.Platform, wantPlatform)
	}
}

func TestPullSinglePlatformImageRejectsWrongPlatform(t *testing.T) {
	host, client := startInProcessRegistry(t)

	// Build an image declaring linux/ppc64le regardless of host.
	img := buildImage(t, "linux", "ppc64le",
		[]string{"/usr/bin/tool"},
		map[string]string{"usr/bin/tool": "x"})
	pushImage(t, host, "tool", "latest", img, client)

	staging := t.TempDir()
	_, err := client.Pull(context.Background(),
		&ResolvedRef{Registry: &Registry{Name: "r", URL: host}, Image: "tool", Tag: "latest"},
		staging)
	if !errors.Is(err, ErrNoMatchingPlatform) {
		t.Fatalf("want ErrNoMatchingPlatform, got %v", err)
	}
}

func TestPullManifestListSelectsMatchingPlatform(t *testing.T) {
	host, client := startInProcessRegistry(t)

	// Two platform entries: a "foreign" one and one matching the host.
	// Content differs between them so we can confirm the right one was
	// extracted.
	foreign := buildImage(t, "linux", "ppc64le",
		[]string{"/usr/bin/tool"},
		map[string]string{"usr/bin/tool": "foreign"})
	native := buildImage(t, runtime.GOOS, runtime.GOARCH,
		[]string{"/usr/bin/tool"},
		map[string]string{"usr/bin/tool": "native"})

	idx := mutate.AppendManifests(empty.Index,
		mutate.IndexAddendum{
			Add: foreign,
			Descriptor: v1.Descriptor{
				MediaType: mustMediaType(t, foreign),
				Platform:  &v1.Platform{OS: "linux", Architecture: "ppc64le"},
			},
		},
		mutate.IndexAddendum{
			Add: native,
			Descriptor: v1.Descriptor{
				MediaType: mustMediaType(t, native),
				Platform:  &v1.Platform{OS: runtime.GOOS, Architecture: runtime.GOARCH},
			},
		},
	)
	pushIndex(t, host, "tool", "latest", idx, client)

	staging := t.TempDir()
	_, err := client.Pull(context.Background(),
		&ResolvedRef{Registry: &Registry{Name: "r", URL: host}, Image: "tool", Tag: "latest"},
		staging)
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(staging, "usr/bin/tool"))
	if err != nil {
		t.Fatalf("read tool: %v", err)
	}
	if string(got) != "native" {
		t.Errorf("wrong platform selected: got body %q", got)
	}
}

func TestPullManifestListRejectsNoMatchingPlatform(t *testing.T) {
	host, client := startInProcessRegistry(t)

	only := buildImage(t, "linux", "ppc64le",
		[]string{"/bin/tool"},
		map[string]string{"bin/tool": "x"})

	idx := mutate.AppendManifests(empty.Index, mutate.IndexAddendum{
		Add: only,
		Descriptor: v1.Descriptor{
			MediaType: mustMediaType(t, only),
			Platform:  &v1.Platform{OS: "linux", Architecture: "ppc64le"},
		},
	})
	pushIndex(t, host, "tool", "latest", idx, client)

	_, err := client.Pull(context.Background(),
		&ResolvedRef{Registry: &Registry{Name: "r", URL: host}, Image: "tool", Tag: "latest"},
		t.TempDir())
	if !errors.Is(err, ErrNoMatchingPlatform) {
		t.Fatalf("want ErrNoMatchingPlatform, got %v", err)
	}
	// Error should name the offered platforms so users know why it failed.
	if err != nil && !strings.Contains(err.Error(), "linux/ppc64le") {
		t.Errorf("error should list offered platforms, got: %v", err)
	}
}

func TestPullManifestListIgnoresAttestationEntries(t *testing.T) {
	host, client := startInProcessRegistry(t)

	// Docker BuildKit emits "unknown/unknown" attestation manifest entries
	// alongside real images. Pull should ignore them and still match the
	// real host-platform image.
	native := buildImage(t, runtime.GOOS, runtime.GOARCH,
		[]string{"/bin/tool"},
		map[string]string{"bin/tool": "native"})
	attestation := buildImage(t, "unknown", "unknown",
		nil, map[string]string{"attestation.json": "{}"})

	idx := mutate.AppendManifests(empty.Index,
		mutate.IndexAddendum{
			Add: native,
			Descriptor: v1.Descriptor{
				MediaType: mustMediaType(t, native),
				Platform:  &v1.Platform{OS: runtime.GOOS, Architecture: runtime.GOARCH},
			},
		},
		mutate.IndexAddendum{
			Add: attestation,
			Descriptor: v1.Descriptor{
				MediaType: mustMediaType(t, attestation),
				Platform:  &v1.Platform{OS: "unknown", Architecture: "unknown"},
			},
		},
	)
	pushIndex(t, host, "tool", "latest", idx, client)

	if _, err := client.Pull(context.Background(),
		&ResolvedRef{Registry: &Registry{Name: "r", URL: host}, Image: "tool", Tag: "latest"},
		t.TempDir()); err != nil {
		t.Fatalf("Pull should skip attestation entries, got: %v", err)
	}
}

// mustMediaType fetches the image's manifest media type for inclusion in
// IndexAddendum descriptors. The registry test server needs a descriptor
// with the right media type so the index it serves is well-formed.
func mustMediaType(t *testing.T, img v1.Image) types.MediaType {
	t.Helper()
	mt, err := img.MediaType()
	if err != nil {
		t.Fatalf("MediaType: %v", err)
	}
	return mt
}
