package registry

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	gcrregistry "github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	gcrremote "github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
)

func TestFetchBundleViaSidecarTag(t *testing.T) {
	srv := httptest.NewServer(gcrregistry.New())
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	client := NewClient("").WithTransport(srv.Client().Transport)

	// 1. Push a main image we can compute the digest of.
	mainImg := mustEmptyImage(t)
	mainRef, err := name.ParseReference(host + "/foo:latest")
	if err != nil {
		t.Fatal(err)
	}
	if err := gcrremote.Write(mainRef, mainImg, append(client.opts, gcrremote.WithTransport(srv.Client().Transport))...); err != nil {
		t.Fatal(err)
	}
	mainDigest, err := mainImg.Digest()
	if err != nil {
		t.Fatal(err)
	}

	// 2. Push a sidecar at sha256-<hex>.sig with the bundle annotation.
	const wantBundle = `{"mediaType":"application/vnd.dev.sigstore.bundle.v0.3+json","content":"placeholder"}`
	sidecarLayer := mustTarLayer(t, "sig", []byte("payload"))
	sidecarImg, err := mutate.Append(empty.Image, mutate.Addendum{
		Layer:       sidecarLayer,
		Annotations: map[string]string{"dev.sigstore.cosign/bundle": wantBundle},
	})
	if err != nil {
		t.Fatal(err)
	}
	sidecarTag, _ := digestToSidecarTag(mainDigest.String())
	sidecarRef, _ := name.ParseReference(host + "/foo:" + sidecarTag)
	if err := gcrremote.Write(sidecarRef, sidecarImg, append(client.opts, gcrremote.WithTransport(srv.Client().Transport))...); err != nil {
		t.Fatalf("push sidecar: %v", err)
	}

	// 3. FetchBundle should locate the sidecar and return the bundle.
	got, err := client.FetchBundle(context.Background(),
		ResolvedRef{Registry: &Registry{Name: "fake", URL: host}, Image: "foo", Tag: "latest"},
		mainDigest.String())
	if err != nil {
		t.Fatalf("FetchBundle: %v", err)
	}
	// The fixture is a v0.3 protobuf bundle JSON (no SignedEntryTimestamp),
	// so FetchBundle should return it as Modern.
	if string(got.Modern) != wantBundle {
		t.Errorf("bundle: got %q, want %q", got.Modern, wantBundle)
	}
	if got.Legacy != nil {
		t.Errorf("expected Legacy to be nil for a v0.3 bundle")
	}
}

func TestFetchBundleNoSidecarReturnsNotFound(t *testing.T) {
	srv := httptest.NewServer(gcrregistry.New())
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	client := NewClient("").WithTransport(srv.Client().Transport)

	// Make a digest that matches no image. Registry will 404 on both the
	// sidecar tag and the referrers query.
	madeUpDigest := "sha256:" + hex.EncodeToString(sha256.New().Sum(nil)) // sha256 of empty
	_, err := client.FetchBundle(context.Background(),
		ResolvedRef{Registry: &Registry{Name: "fake", URL: host}, Image: "foo", Tag: "latest"},
		madeUpDigest)
	if !errors.Is(err, ErrBundleNotFound) {
		t.Errorf("want ErrBundleNotFound, got %v", err)
	}
}

func TestDigestToSidecarTag(t *testing.T) {
	tag, err := digestToSidecarTag("sha256:abc123")
	if err != nil {
		t.Fatal(err)
	}
	if tag != "sha256-abc123.sig" {
		t.Errorf("tag: got %q", tag)
	}

	if _, err := digestToSidecarTag("md5:bad"); err == nil {
		t.Error("expected error for non-sha256 digest")
	}
}

func TestLooksLikeJSON(t *testing.T) {
	cases := map[string]bool{
		`{"k":"v"}`:    true,
		`  {"k":"v"}`:  true, // leading whitespace ok
		"\n\t{}":       true,
		`["array"]`:    false,
		"plain string": false,
		"":             false,
	}
	for in, want := range cases {
		if got := looksLikeJSON(in); got != want {
			t.Errorf("looksLikeJSON(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestIsSigstoreBundleMediaType(t *testing.T) {
	cases := map[string]bool{
		"application/vnd.dev.sigstore.bundle.v0.3+json": true,
		"application/vnd.dev.sigstore.bundle.v0.4+json": true,
		"application/vnd.dev.cosignproject.v0.1+json":   false,
		"":                              false,
		"application/octet-stream":      false,
	}
	for in, want := range cases {
		if got := isSigstoreBundleMediaType(in); got != want {
			t.Errorf("isSigstoreBundleMediaType(%q) = %v, want %v", in, got, want)
		}
	}
}

// mustEmptyImage builds a minimal, valid OCI image with no layers. Used
// when a test only cares about its digest, not its content.
func mustEmptyImage(t *testing.T) v1.Image {
	t.Helper()
	img, err := mutate.AppendLayers(empty.Image, mustTarLayer(t, "marker", []byte("x")))
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func mustTarLayer(t *testing.T, name string, content []byte) v1.Layer {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg})
	_, _ = tw.Write(content)
	_ = tw.Close()
	bs := buf.Bytes()
	layer, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(bs)), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return layer
}
