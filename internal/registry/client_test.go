package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The manifest body the fake registry serves. Exported as a var so tests
// can reference its actual digest, which go-containerregistry computes from
// the body (not from a header).
var fakeManifestBody = []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"digest":"sha256:00","size":0,"mediaType":"application/vnd.oci.image.config.v1+json"},"layers":[]}`)

func fakeManifestDigest() string {
	sum := sha256.Sum256(fakeManifestBody)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// fakeRegistry implements just enough of the OCI distribution /v2/ API to
// exercise Client methods. Each test wires in its own handlers for the
// endpoints it cares about.
type fakeRegistry struct {
	catalogStatus int
	catalogBody   any
	tagsByRepo    map[string][]string
	digestByRef   map[string]string
}

func (f *fakeRegistry) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		// /v2/ base — required handshake for go-containerregistry.
		case r.URL.Path == "/v2/" || r.URL.Path == "/v2":
			w.WriteHeader(http.StatusOK)

		case r.URL.Path == "/v2/_catalog":
			if f.catalogStatus != 0 && f.catalogStatus != http.StatusOK {
				w.WriteHeader(f.catalogStatus)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(f.catalogBody)

		case strings.HasSuffix(r.URL.Path, "/tags/list"):
			// /v2/<name>/tags/list
			repo := strings.TrimPrefix(r.URL.Path, "/v2/")
			repo = strings.TrimSuffix(repo, "/tags/list")
			tags, ok := f.tagsByRepo[repo]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"name": repo, "tags": tags})

		case strings.Contains(r.URL.Path, "/manifests/"):
			// /v2/<name>/manifests/<reference>
			path := strings.TrimPrefix(r.URL.Path, "/v2/")
			parts := strings.SplitN(path, "/manifests/", 2)
			if len(parts) != 2 {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			ref := parts[0] + ":" + parts[1]
			digest, ok := f.digestByRef[ref]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			// Minimal OCI manifest body. go-containerregistry hashes the
			// body itself to produce the Descriptor.Digest; the header
			// value is cross-checked but not authoritative.
			w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
			w.Header().Set("Docker-Content-Digest", digest)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(fakeManifestBody)

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

func startFake(t *testing.T, f *fakeRegistry) (*Client, *Registry) {
	t.Helper()
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)

	host := strings.TrimPrefix(srv.URL, "http://")
	client := NewClient().WithTransport(srv.Client().Transport)
	reg := &Registry{Name: "fake", URL: host}
	return client, reg
}

func TestClientCatalogOK(t *testing.T) {
	f := &fakeRegistry{
		catalogStatus: http.StatusOK,
		catalogBody:   map[string]any{"repositories": []string{"jq", "yq", "ripgrep"}},
	}
	client, reg := startFake(t, f)

	names, err := client.Catalog(context.Background(), reg)
	if err != nil {
		t.Fatalf("Catalog: %v", err)
	}
	if len(names) != 3 || names[0] != "jq" {
		t.Errorf("unexpected catalog: %v", names)
	}
}

func TestClientCatalogUnavailableMappings(t *testing.T) {
	cases := []int{http.StatusNotFound, http.StatusUnauthorized, http.StatusForbidden, http.StatusMethodNotAllowed}
	for _, code := range cases {
		t.Run(http.StatusText(code), func(t *testing.T) {
			f := &fakeRegistry{catalogStatus: code}
			client, reg := startFake(t, f)
			_, err := client.Catalog(context.Background(), reg)
			if !errors.Is(err, ErrCatalogUnavailable) {
				t.Errorf("status %d: want ErrCatalogUnavailable, got %v", code, err)
			}
		})
	}
}

func TestClientCatalogNamespaceFilter(t *testing.T) {
	// Simulates a registry whose URL is configured with a namespace prefix:
	// "cgr.dev/chainguard". The /v2/_catalog response still returns the full
	// repo name ("chainguard/jq"); the Client strips the prefix.
	f := &fakeRegistry{
		catalogStatus: http.StatusOK,
		catalogBody: map[string]any{"repositories": []string{
			"chainguard/jq", "chainguard/yq", "other/thing",
		}},
	}
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)

	host := strings.TrimPrefix(srv.URL, "http://")
	client := NewClient().WithTransport(srv.Client().Transport)
	reg := &Registry{Name: "fake", URL: host + "/chainguard"}

	names, err := client.Catalog(context.Background(), reg)
	if err != nil {
		t.Fatalf("Catalog: %v", err)
	}
	// Expect only the chainguard-prefixed entries, with the prefix stripped.
	if len(names) != 2 {
		t.Fatalf("filtered catalog: got %v", names)
	}
	for _, n := range names {
		if n != "jq" && n != "yq" {
			t.Errorf("unexpected entry %q", n)
		}
	}
}

func TestClientTagsList(t *testing.T) {
	f := &fakeRegistry{
		tagsByRepo: map[string][]string{
			"jq": {"1.7.1", "1.7.0"},
		},
	}
	client, reg := startFake(t, f)

	tags, err := client.Tags(context.Background(), reg, "jq")
	if err != nil {
		t.Fatalf("Tags: %v", err)
	}
	if len(tags) != 2 || tags[0] != "1.7.1" {
		t.Errorf("unexpected tags: %v", tags)
	}
}

func TestClientDigestRead(t *testing.T) {
	wantDigest := fakeManifestDigest()
	f := &fakeRegistry{
		digestByRef: map[string]string{"jq:latest": wantDigest},
	}
	client, reg := startFake(t, f)

	got, err := client.Digest(context.Background(), reg, "jq", "latest")
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	if got != wantDigest {
		t.Errorf("digest: got %q, want %q", got, wantDigest)
	}
}

func TestClientPullIsStubbed(t *testing.T) {
	client := NewClient()
	_, err := client.Pull(context.Background(), nil, "")
	if !errors.Is(err, ErrNotImplemented) {
		t.Errorf("Pull: want ErrNotImplemented, got %v", err)
	}
}
