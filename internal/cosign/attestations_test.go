package cosign

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// makeStatement builds a minimal in-toto Statement with the given
// predicate type and predicate body. Returns the JSON bytes ready to
// pass to a summarizer.
func makeStatement(t *testing.T, predicateType string, predicate any) []byte {
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
	return stmt
}

func TestSummarizeSPDX(t *testing.T) {
	// SPDX 2.x shape: top-level packages array.
	stmt := makeStatement(t, predicateSPDX, map[string]any{
		"spdxVersion": "SPDX-2.3",
		"packages": []map[string]string{
			{"name": "openssl"}, {"name": "libcurl"}, {"name": "zlib"},
		},
	})
	atts := SummarizeAttestations([]RawAttestation{{
		PredicateType: predicateSPDX,
		Statement:     stmt,
	}})
	if atts == nil || atts.SBOM == nil {
		t.Fatalf("SBOM not summarized: %+v", atts)
	}
	if atts.SBOM.Format != "spdx" || atts.SBOM.ComponentCount != 3 {
		t.Errorf("SBOM: got %+v, want spdx/3", atts.SBOM)
	}
}

func TestSummarizeSPDX3GraphShape(t *testing.T) {
	// SPDX 3.x uses @graph instead of packages.
	stmt := makeStatement(t, predicateSPDX, map[string]any{
		"@context": "https://spdx.org/v3",
		"@graph": []map[string]string{
			{"type": "software_Package"},
			{"type": "Person"},
			{"type": "Package"},
			{"type": "Relationship"},
		},
	})
	atts := SummarizeAttestations([]RawAttestation{{
		PredicateType: predicateSPDX,
		Statement:     stmt,
	}})
	if atts == nil || atts.SBOM == nil || atts.SBOM.ComponentCount != 2 {
		t.Errorf("SPDX 3 graph: got %+v, want 2 packages", atts.SBOM)
	}
}

func TestSummarizeCycloneDX(t *testing.T) {
	stmt := makeStatement(t, predicateCycloneDX, map[string]any{
		"bomFormat":   "CycloneDX",
		"specVersion": "1.4",
		"components":  []map[string]string{{"name": "a"}, {"name": "b"}},
	})
	atts := SummarizeAttestations([]RawAttestation{{
		PredicateType: predicateCycloneDX,
		Statement:     stmt,
	}})
	if atts == nil || atts.SBOM == nil {
		t.Fatalf("CycloneDX not summarized: %+v", atts)
	}
	if atts.SBOM.Format != "cyclonedx" || atts.SBOM.ComponentCount != 2 {
		t.Errorf("CycloneDX: got %+v, want cyclonedx/2", atts.SBOM)
	}
}

func TestSummarizeSLSAv1Provenance(t *testing.T) {
	stmt := makeStatement(t, predicateSLSAv1, map[string]any{
		"buildDefinition": map[string]any{
			"buildType": "https://github.com/actions/runner",
		},
		"runDetails": map[string]any{
			"builder":  map[string]string{"id": "https://github.com/chainguard-images/images/.github/runners"},
			"metadata": map[string]string{"invocationId": "run-12345"},
		},
	})
	atts := SummarizeAttestations([]RawAttestation{{
		PredicateType: predicateSLSAv1,
		Statement:     stmt,
	}})
	if atts == nil || atts.Provenance == nil {
		t.Fatalf("Provenance not summarized: %+v", atts)
	}
	p := atts.Provenance
	if !strings.Contains(p.BuilderID, "chainguard-images") {
		t.Errorf("BuilderID: %q", p.BuilderID)
	}
	if p.InvocationID != "run-12345" {
		t.Errorf("InvocationID: %q", p.InvocationID)
	}
	if !strings.Contains(p.BuildType, "actions/runner") {
		t.Errorf("BuildType: %q", p.BuildType)
	}
}

func TestSummarizeSLSAv02Provenance(t *testing.T) {
	stmt := makeStatement(t, predicateSLSAv02, map[string]any{
		"buildType": "https://example/buildtype",
		"builder":   map[string]string{"id": "https://example/builder"},
	})
	atts := SummarizeAttestations([]RawAttestation{{
		PredicateType: predicateSLSAv02,
		Statement:     stmt,
	}})
	if atts == nil || atts.Provenance == nil {
		t.Fatalf("Provenance not summarized: %+v", atts)
	}
	if atts.Provenance.BuilderID != "https://example/builder" {
		t.Errorf("BuilderID: %q", atts.Provenance.BuilderID)
	}
}

func TestSummarizeVulnCounts(t *testing.T) {
	stmt := makeStatement(t, predicateVuln, map[string]any{
		"metadata": map[string]any{"scanFinishedOn": "2026-04-22T12:00:00Z"},
		"results": map[string]any{
			"vulnerabilities": []map[string]string{
				{"severity": "Critical"},
				{"severity": "high"},
				{"severity": "HIGH"},
				{"severity": "medium"},
				{"severity": "low"},
				{"severity": "negligible"},
				{"severity": "unknown"}, // ignored
			},
		},
	})
	atts := SummarizeAttestations([]RawAttestation{{
		PredicateType: predicateVuln,
		Statement:     stmt,
	}})
	if atts == nil || atts.VulnScan == nil {
		t.Fatalf("Vuln not summarized: %+v", atts)
	}
	v := atts.VulnScan
	if v.Critical != 1 || v.High != 2 || v.Medium != 1 || v.Low != 2 {
		t.Errorf("counts: got %+v", v)
	}
	want, _ := time.Parse(time.RFC3339, "2026-04-22T12:00:00Z")
	if !v.ScannedAt.Equal(want) {
		t.Errorf("scannedAt: got %v, want %v", v.ScannedAt, want)
	}
}

func TestSummarizeMixedSet(t *testing.T) {
	// Realistic case: image carries SBOM + provenance + vuln, all three
	// surface in the result.
	atts := SummarizeAttestations([]RawAttestation{
		{
			PredicateType: predicateSPDX,
			Statement: makeStatement(t, predicateSPDX, map[string]any{
				"packages": []map[string]string{{"name": "x"}},
			}),
		},
		{
			PredicateType: predicateSLSAv1,
			Statement: makeStatement(t, predicateSLSAv1, map[string]any{
				"buildDefinition": map[string]any{"buildType": "test"},
				"runDetails": map[string]any{
					"builder": map[string]string{"id": "ci-runner"},
				},
			}),
		},
		{
			PredicateType: predicateVuln,
			Statement: makeStatement(t, predicateVuln, map[string]any{
				"results": map[string]any{
					"vulnerabilities": []map[string]string{{"severity": "low"}},
				},
			}),
		},
	})
	if atts == nil {
		t.Fatal("no summary produced")
	}
	if atts.SBOM == nil || atts.Provenance == nil || atts.VulnScan == nil {
		t.Errorf("expected all three summaries: got %+v", atts)
	}
}

func TestSummarizeSkipsUnknownPredicateTypes(t *testing.T) {
	atts := SummarizeAttestations([]RawAttestation{{
		PredicateType: "https://example/unknown",
		Statement:     []byte(`{}`),
	}})
	if atts != nil {
		t.Errorf("unknown predicate produced summary: %+v", atts)
	}
}

func TestDecodeDSSEFromBase64Payload(t *testing.T) {
	stmt := makeStatement(t, predicateSPDX, map[string]any{"packages": []map[string]string{}})
	envelope, _ := json.Marshal(map[string]any{
		"payloadType": "application/vnd.in-toto+json",
		"payload":     base64.StdEncoding.EncodeToString(stmt),
		"signatures":  []map[string]string{{"sig": "abc", "keyid": "k"}},
	})
	raw, err := DecodeDSSE(envelope)
	if err != nil {
		t.Fatalf("DecodeDSSE: %v", err)
	}
	if raw.PredicateType != predicateSPDX {
		t.Errorf("predicateType: %q", raw.PredicateType)
	}
}

func TestDecodeDSSEFromRawPayload(t *testing.T) {
	// Some bundles carry an unwrapped payload; DecodeDSSE handles that
	// by trying base64 first then falling back to raw bytes.
	stmt := makeStatement(t, predicateSPDX, map[string]any{"packages": []map[string]string{}})
	envelope, _ := json.Marshal(map[string]any{
		"payloadType": "application/vnd.in-toto+json",
		"payload":     string(stmt),
	})
	raw, err := DecodeDSSE(envelope)
	if err != nil {
		t.Fatalf("DecodeDSSE: %v", err)
	}
	if raw.PredicateType != predicateSPDX {
		t.Errorf("predicateType: %q", raw.PredicateType)
	}
}

func TestAttestationsHasAny(t *testing.T) {
	var nilAtts *Attestations
	if nilAtts.HasAny() {
		t.Error("nil HasAny should be false")
	}
	if (&Attestations{}).HasAny() {
		t.Error("empty HasAny should be false")
	}
	if !(&Attestations{SBOM: &SBOMSummary{}}).HasAny() {
		t.Error("populated HasAny should be true")
	}
}
