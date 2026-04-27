package registry

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
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

// makeDSSEEnvelope wraps an in-toto statement in a minimal DSSE envelope
// the cosign attestation fetcher expects.
func makeDSSEEnvelope(t *testing.T, predicateType string, predicate any) []byte {
	t.Helper()
	predBytes, err := json.Marshal(predicate)
	if err != nil {
		t.Fatal(err)
	}
	stmt, err := json.Marshal(map[string]any{
		"_type":         "https://in-toto.io/Statement/v1",
		"predicateType": predicateType,
		"subject":       []map[string]any{{"name": "x", "digest": map[string]string{"sha256": "00"}}},
		"predicate":     json.RawMessage(predBytes),
	})
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := json.Marshal(map[string]any{
		"payloadType": "application/vnd.in-toto+json",
		"payload":     base64.StdEncoding.EncodeToString(stmt),
		"signatures":  []map[string]string{{"keyid": "k", "sig": "ignored"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return envelope
}

// attestationLayer wraps DSSE bytes as a single-blob OCI layer ready to
// add to a sidecar attestation manifest.
func attestationLayer(t *testing.T, envelopeBytes []byte) v1.Layer {
	t.Helper()
	bs := envelopeBytes
	layer, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(bs)), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return layer
}

func TestFetchAttestationsViaSidecar(t *testing.T) {
	srv := httptest.NewServer(gcrregistry.New())
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	client := NewClient("").WithTransport(srv.Client().Transport)

	// 1. Push the main image we'll fetch attestations for.
	mainImg := mustEmptyImage(t)
	mainRef, err := name.ParseReference(host + "/foo:latest")
	if err != nil {
		t.Fatal(err)
	}
	if err := gcrremote.Write(mainRef, mainImg, append(client.opts, gcrremote.WithTransport(srv.Client().Transport))...); err != nil {
		t.Fatal(err)
	}
	mainDigest, _ := mainImg.Digest()

	// 2. Build a sidecar manifest carrying two attestation layers: an
	// SPDX SBOM and a SLSA v1 provenance. Each layer is a DSSE envelope.
	sbomDSSE := makeDSSEEnvelope(t, "https://spdx.dev/Document", map[string]any{
		"spdxVersion": "SPDX-2.3",
		"packages":    []map[string]string{{"name": "openssl"}, {"name": "zlib"}},
	})
	provDSSE := makeDSSEEnvelope(t, "https://slsa.dev/provenance/v1", map[string]any{
		"buildDefinition": map[string]any{"buildType": "test-builder"},
		"runDetails": map[string]any{
			"builder": map[string]string{"id": "https://github.com/example/builder"},
		},
	})

	attImg, err := mutate.Append(empty.Image,
		mutate.Addendum{Layer: attestationLayer(t, sbomDSSE)},
		mutate.Addendum{Layer: attestationLayer(t, provDSSE)},
	)
	if err != nil {
		t.Fatal(err)
	}
	attTag, _ := digestToAttestationTag(mainDigest.String())
	attRef, _ := name.ParseReference(host + "/foo:" + attTag)
	if err := gcrremote.Write(attRef, attImg, append(client.opts, gcrremote.WithTransport(srv.Client().Transport))...); err != nil {
		t.Fatal(err)
	}

	// 3. FetchAttestations should return both attestations with the right
	//    predicate types extracted.
	atts, err := client.FetchAttestations(context.Background(),
		ResolvedRef{Registry: &Registry{Name: "fake", URL: host}, Image: "foo", Tag: "latest"},
		mainDigest.String())
	if err != nil {
		t.Fatalf("FetchAttestations: %v", err)
	}
	if len(atts) != 2 {
		t.Fatalf("expected 2 attestations, got %d", len(atts))
	}

	gotTypes := map[string]bool{}
	for _, a := range atts {
		gotTypes[a.PredicateType] = true
	}
	if !gotTypes["https://spdx.dev/Document"] {
		t.Error("missing SPDX attestation")
	}
	if !gotTypes["https://slsa.dev/provenance/v1"] {
		t.Error("missing SLSA provenance attestation")
	}
}

func TestFetchAttestationsNoSidecarReturnsTypedError(t *testing.T) {
	srv := httptest.NewServer(gcrregistry.New())
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	client := NewClient("").WithTransport(srv.Client().Transport)

	// No image pushed, no .att tag present → typed error.
	madeUpDigest := "sha256:" + strings.Repeat("a", 64)
	_, err := client.FetchAttestations(context.Background(),
		ResolvedRef{Registry: &Registry{Name: "fake", URL: host}, Image: "foo", Tag: "latest"},
		madeUpDigest)
	if !errors.Is(err, ErrAttestationsNotFound) {
		t.Errorf("want ErrAttestationsNotFound, got %v", err)
	}
}

func TestDigestToAttestationTag(t *testing.T) {
	tag, err := digestToAttestationTag("sha256:abc123")
	if err != nil {
		t.Fatal(err)
	}
	if tag != "sha256-abc123.att" {
		t.Errorf("tag: got %q", tag)
	}

	if _, err := digestToAttestationTag("md5:bad"); err == nil {
		t.Error("expected error for non-sha256 digest")
	}
}
