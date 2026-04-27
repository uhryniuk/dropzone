package cosign

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"

	protocommon "github.com/sigstore/protobuf-specs/gen/pb-go/common/v1"
	"google.golang.org/protobuf/proto"
)

// makeFixture builds a self-signed cert + signature + simple-signing
// payload + Rekor-bundle JSON shaped like what cosign produces. Used
// to exercise buildLegacyBundle's parsing without touching live Sigstore.
//
// We don't try to make this verifiable end-to-end (that would need
// Fulcio + Rekor), only that the converter assembles a well-formed
// protobuf bundle and runs the binding check.
func makeFixture(t *testing.T, imageDigest string) LegacyParts {
	t.Helper()

	// Self-signed cert.
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "fixture"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	derBytes, err := x509.CreateCertificate(rand.Reader, template, template, &priv.PublicKey, priv)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})

	// Simple-signing payload binding to imageDigest.
	payload := map[string]any{
		"critical": map[string]any{
			"identity": map[string]string{"docker-reference": "example/tool"},
			"image":    map[string]string{"docker-manifest-digest": imageDigest},
			"type":     "cosign container image signature",
		},
		"optional": nil,
	}
	payloadBytes, _ := json.Marshal(payload)

	// Sign sha256(payload) with the cert's private key. Doesn't need to
	// chain to Fulcio for the converter unit test; we never actually
	// verify in this test.
	sum := sha256.Sum256(payloadBytes)
	sig, err := ecdsa.SignASN1(rand.Reader, priv, sum[:])
	if err != nil {
		t.Fatal(err)
	}

	// Legacy Rekor bundle. The body is what Rekor stored; we can stuff
	// any JSON in there for unit-test purposes since the converter
	// doesn't validate its content.
	body := map[string]any{
		"apiVersion": "0.0.1",
		"kind":       "hashedrekord",
		"spec":       map[string]any{},
	}
	bodyBytes, _ := json.Marshal(body)
	rekor := map[string]any{
		"SignedEntryTimestamp": base64.StdEncoding.EncodeToString([]byte("fake-set")),
		"Payload": map[string]any{
			"body":           base64.StdEncoding.EncodeToString(bodyBytes),
			"integratedTime": int64(1700000000),
			"logIndex":       int64(42),
			"logID":          hex.EncodeToString([]byte("fake-log-id-32-bytes-padding-here")[:32]),
		},
	}
	rekorJSON, _ := json.Marshal(rekor)

	return LegacyParts{
		Bundle:         rekorJSON,
		SignatureB64:   base64.StdEncoding.EncodeToString(sig),
		CertificatePEM: string(certPEM),
		Payload:        payloadBytes,
	}
}

func TestBuildLegacyBundleAssembleAndBind(t *testing.T) {
	imageDigest := "sha256:" + strings.Repeat("a", 64)
	parts := makeFixture(t, imageDigest)

	bundle, payload, err := buildLegacyBundle(parts, imageDigest)
	if err != nil {
		t.Fatalf("buildLegacyBundle: %v", err)
	}

	// Returned payload is the simple-signing JSON we'll hand to
	// sigstore-go's WithArtifact.
	if string(payload) != string(parts.Payload) {
		t.Errorf("payload returned doesn't match input")
	}

	// MessageDigest should be sha256 of the simple-signing payload.
	want := sha256.Sum256(parts.Payload)
	gotDigest := bundle.GetMessageSignature().GetMessageDigest()
	if gotDigest == nil {
		t.Fatal("bundle has no MessageDigest")
	}
	if gotDigest.GetAlgorithm() != protocommon.HashAlgorithm_SHA2_256 {
		t.Errorf("digest algo: got %v, want SHA2_256", gotDigest.GetAlgorithm())
	}
	if string(gotDigest.GetDigest()) != string(want[:]) {
		t.Errorf("digest bytes don't match sha256(payload)")
	}

	// VerificationMaterial should carry the cert and one tlog entry.
	if cert := bundle.GetVerificationMaterial().GetCertificate(); cert == nil || len(cert.GetRawBytes()) == 0 {
		t.Error("certificate missing from verification material")
	}
	if entries := bundle.GetVerificationMaterial().GetTlogEntries(); len(entries) != 1 {
		t.Fatalf("expected 1 tlog entry, got %d", len(entries))
	}

	// MediaType should be the v0.3 bundle media type.
	if !strings.HasPrefix(bundle.GetMediaType(), "application/vnd.dev.sigstore.bundle") {
		t.Errorf("media type wrong: %q", bundle.GetMediaType())
	}

	// Sanity: bundle should be valid protobuf (i.e., serializable).
	if _, err := proto.Marshal(bundle); err != nil {
		t.Errorf("proto.Marshal: %v", err)
	}
}

func TestBuildLegacyBundleRejectsWrongImageDigest(t *testing.T) {
	imageDigest := "sha256:" + strings.Repeat("a", 64)
	parts := makeFixture(t, imageDigest)

	wrongDigest := "sha256:" + strings.Repeat("b", 64)
	_, _, err := buildLegacyBundle(parts, wrongDigest)
	if err == nil {
		t.Fatal("expected binding-check failure when payload digest doesn't match expected")
	}
	if !strings.Contains(err.Error(), "binds to") {
		t.Errorf("error should mention the binding mismatch: %v", err)
	}
}

func TestBuildLegacyBundleRejectsMissingParts(t *testing.T) {
	imageDigest := "sha256:" + strings.Repeat("a", 64)
	cases := map[string]LegacyParts{
		"missing bundle":    {SignatureB64: "x", CertificatePEM: "y", Payload: []byte("z")},
		"missing signature": {Bundle: []byte("{}"), CertificatePEM: "y", Payload: []byte("z")},
		"missing cert":      {Bundle: []byte("{}"), SignatureB64: "x", Payload: []byte("z")},
		"missing payload":   {Bundle: []byte("{}"), SignatureB64: "x", CertificatePEM: "y"},
	}
	for name, parts := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, err := buildLegacyBundle(parts, imageDigest); err == nil {
				t.Errorf("expected error")
			}
		})
	}
}

func TestBuildLegacyBundleRejectsPayloadWithoutDigest(t *testing.T) {
	// A simple-signing payload missing critical.image.docker-manifest-digest
	// has no binding to check against, so we refuse to use it.
	parts := makeFixture(t, "sha256:"+strings.Repeat("a", 64))
	parts.Payload = []byte(`{"critical": {"identity": {"docker-reference": "x"}, "type": "cosign container image signature"}}`)
	if _, _, err := buildLegacyBundle(parts, "sha256:abc"); err == nil {
		t.Error("expected error when payload has no docker-manifest-digest")
	}
}
