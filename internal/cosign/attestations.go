package cosign

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"
)

// in-toto predicate types we know how to summarize. Cosign attaches one
// in-toto statement per attestation, with the predicate type identifying
// what's inside. Anything we don't recognize is silently ignored — Phase
// 5 is about surfacing the common cases, not exhaustive parsing.
const (
	predicateSPDX        = "https://spdx.dev/Document"
	predicateCycloneDX   = "https://cyclonedx.org/bom"
	predicateSLSAv1      = "https://slsa.dev/provenance/v1"
	predicateSLSAv02     = "https://slsa.dev/provenance/v0.2"
	predicateVuln        = "https://cosign.sigstore.dev/attestation/vuln/v1"
)

// RawAttestation is a single in-toto Statement extracted from a DSSE
// envelope on the image's .att sidecar manifest. The registry package
// produces these; this package consumes them.
type RawAttestation struct {
	// PredicateType is the in-toto predicate type URL. Determines which
	// summary parser runs.
	PredicateType string
	// Statement is the raw JSON of the entire in-toto statement,
	// including {predicateType, subject, predicate}. We re-parse the
	// predicate field per-type rather than maintaining a typed AST for
	// every supported predicate.
	Statement []byte
}

// SummarizeAttestations turns a slice of in-toto statements into a
// best-effort Attestations summary. Each parser is independent: a
// malformed SBOM doesn't suppress the provenance summary, etc.
func SummarizeAttestations(raws []RawAttestation) *Attestations {
	if len(raws) == 0 {
		return nil
	}
	out := &Attestations{}
	for _, r := range raws {
		switch {
		case strings.HasPrefix(r.PredicateType, predicateSPDX):
			if s := summarizeSPDX(r.Statement); s != nil {
				out.SBOM = s
			}
		case strings.HasPrefix(r.PredicateType, predicateCycloneDX):
			if s := summarizeCycloneDX(r.Statement); s != nil {
				out.SBOM = s
			}
		case r.PredicateType == predicateSLSAv1 || r.PredicateType == predicateSLSAv02:
			if p := summarizeProvenance(r.Statement, r.PredicateType); p != nil {
				out.Provenance = p
			}
		case r.PredicateType == predicateVuln:
			if v := summarizeVuln(r.Statement); v != nil {
				out.VulnScan = v
			}
		}
	}
	if !out.HasAny() {
		return nil
	}
	return out
}

// statementShape pulls just the fields we care about out of the in-toto
// statement envelope. predicate stays as RawMessage so each summarizer
// can re-decode it against its own typed shape.
type statementShape struct {
	PredicateType string          `json:"predicateType"`
	Predicate     json.RawMessage `json:"predicate"`
}

func summarizeSPDX(stmtJSON []byte) *SBOMSummary {
	var stmt statementShape
	if err := json.Unmarshal(stmtJSON, &stmt); err != nil {
		return nil
	}
	// SPDX 2.x: top-level "packages" array.
	// SPDX 3.x: "@graph" with elements; we count the package-typed elements.
	var spdx struct {
		Packages []json.RawMessage `json:"packages"`
		Graph    []struct {
			Type string `json:"type"`
		} `json:"@graph"`
	}
	if err := json.Unmarshal(stmt.Predicate, &spdx); err != nil {
		return nil
	}
	count := len(spdx.Packages)
	if count == 0 {
		for _, e := range spdx.Graph {
			// SPDX 3 element types include "software_Package", "Package",
			// etc. Match loosely on "Package" to catch both.
			if strings.Contains(e.Type, "Package") {
				count++
			}
		}
	}
	if count == 0 {
		return nil
	}
	return &SBOMSummary{Format: "spdx", ComponentCount: count}
}

func summarizeCycloneDX(stmtJSON []byte) *SBOMSummary {
	var stmt statementShape
	if err := json.Unmarshal(stmtJSON, &stmt); err != nil {
		return nil
	}
	var bom struct {
		Components []json.RawMessage `json:"components"`
	}
	if err := json.Unmarshal(stmt.Predicate, &bom); err != nil {
		return nil
	}
	if len(bom.Components) == 0 {
		return nil
	}
	return &SBOMSummary{Format: "cyclonedx", ComponentCount: len(bom.Components)}
}

func summarizeProvenance(stmtJSON []byte, predicateType string) *ProvenanceSummary {
	var stmt statementShape
	if err := json.Unmarshal(stmtJSON, &stmt); err != nil {
		return nil
	}
	out := &ProvenanceSummary{}

	// SLSA v1.0 shape: {buildDefinition: {buildType, externalParameters},
	//                   runDetails: {builder: {id}, metadata: {invocationId}}}.
	if predicateType == predicateSLSAv1 {
		var p struct {
			BuildDefinition struct {
				BuildType string `json:"buildType"`
			} `json:"buildDefinition"`
			RunDetails struct {
				Builder struct {
					ID string `json:"id"`
				} `json:"builder"`
				Metadata struct {
					InvocationID string `json:"invocationId"`
				} `json:"metadata"`
			} `json:"runDetails"`
		}
		if err := json.Unmarshal(stmt.Predicate, &p); err == nil {
			out.BuildType = p.BuildDefinition.BuildType
			out.BuilderID = p.RunDetails.Builder.ID
			out.InvocationID = p.RunDetails.Metadata.InvocationID
		}
	}

	// SLSA v0.2 shape: {buildType, builder: {id}, invocation: ...}.
	if predicateType == predicateSLSAv02 {
		var p struct {
			BuildType string `json:"buildType"`
			Builder   struct {
				ID string `json:"id"`
			} `json:"builder"`
		}
		if err := json.Unmarshal(stmt.Predicate, &p); err == nil {
			out.BuildType = p.BuildType
			out.BuilderID = p.Builder.ID
		}
	}

	if out.BuilderID == "" && out.BuildType == "" && out.InvocationID == "" {
		return nil
	}
	return out
}

func summarizeVuln(stmtJSON []byte) *VulnSummary {
	var stmt statementShape
	if err := json.Unmarshal(stmtJSON, &stmt); err != nil {
		return nil
	}
	// Cosign vuln predicate shape:
	//   { invocation, scanner: {...},
	//     metadata: { scanStartedOn, scanFinishedOn },
	//     results: { vulnerabilities: [{ severity, ... }] } }
	var p struct {
		Metadata struct {
			ScanFinishedOn time.Time `json:"scanFinishedOn"`
			ScanStartedOn  time.Time `json:"scanStartedOn"`
		} `json:"metadata"`
		Results struct {
			Vulnerabilities []struct {
				Severity string `json:"severity"`
			} `json:"vulnerabilities"`
		} `json:"results"`
		// Some scanners flatten "results" into the predicate root.
		Vulnerabilities []struct {
			Severity string `json:"severity"`
		} `json:"vulnerabilities"`
	}
	if err := json.Unmarshal(stmt.Predicate, &p); err != nil {
		return nil
	}
	vulns := p.Results.Vulnerabilities
	if len(vulns) == 0 {
		vulns = p.Vulnerabilities
	}

	out := &VulnSummary{ScannedAt: p.Metadata.ScanFinishedOn}
	if out.ScannedAt.IsZero() {
		out.ScannedAt = p.Metadata.ScanStartedOn
	}
	for _, v := range vulns {
		switch strings.ToLower(v.Severity) {
		case "critical":
			out.Critical++
		case "high":
			out.High++
		case "medium":
			out.Medium++
		case "low", "negligible":
			out.Low++
		}
	}
	if out.Critical+out.High+out.Medium+out.Low == 0 && out.ScannedAt.IsZero() {
		return nil
	}
	return out
}

// DecodeDSSE pulls the in-toto statement out of a DSSE envelope. Cosign
// attestations are wrapped in DSSE envelopes (`{"payload": "<base64>",
// "signatures": [...]}`). The payload's base64-decoded form is the
// in-toto statement we hand to SummarizeAttestations.
//
// We do not verify the DSSE signature here — Phase 5 is informational.
// Trust derives from the image-level signature having been verified in
// Phase 4 by the same identity that produced these attestations.
func DecodeDSSE(envelope []byte) (RawAttestation, error) {
	var dsse struct {
		PayloadType string `json:"payloadType"`
		Payload     string `json:"payload"`
	}
	if err := json.Unmarshal(envelope, &dsse); err != nil {
		return RawAttestation{}, err
	}
	stmt, err := base64.StdEncoding.DecodeString(dsse.Payload)
	if err != nil {
		// Cosign sometimes emits already-decoded payload bytes (no
		// base64 wrap) when bundled in newer formats. Try parsing raw.
		stmt = []byte(dsse.Payload)
	}
	var s statementShape
	if err := json.Unmarshal(stmt, &s); err != nil {
		return RawAttestation{}, err
	}
	return RawAttestation{PredicateType: s.PredicateType, Statement: stmt}, nil
}
