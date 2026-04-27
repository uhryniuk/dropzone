package packagehandler

import "testing"

// newerTagsThan filtering. Most of the integration coverage is in
// internal/app/cli_update_test.go, which exercises the full live-
// registry path. These are unit checks for the noise-suppression
// rules: cosign sidecar tags, floating tags, and dev-variant siblings
// of the installed tag should never show up as "newer".

func TestNewerTagsThanFiltersCosignSidecarTags(t *testing.T) {
	current := "1.0.0"
	tags := []string{
		"1.0.1",
		"sha256-abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789.sig",
		"sha256-0011223344556677889900112233445566778899001122334455667788990011.att",
		"sha256-aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899.sbom",
		"sha256-fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210", // no extension
		"2.0.0",
	}
	got := newerTagsThan(current, tags)
	want := []string{"1.0.1", "2.0.0"}
	if len(got) != len(want) {
		t.Fatalf("filtered tags: got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("at %d: got %q, want %q", i, got[i], want[i])
		}
	}
}

func TestNewerTagsThanFiltersInstalledTagVariants(t *testing.T) {
	// When the user installed `latest`, we shouldn't suggest
	// `latest-dev` as a newer version. Same for any tag with the
	// installed tag as a "name-" prefix.
	cases := []struct {
		current string
		tags    []string
		want    []string
	}{
		{
			current: "latest",
			tags:    []string{"latest", "latest-dev", "1.0", "8.6", "8.6-dev"},
			// "latest" is floating + identical, "latest-dev" is a
			// variant of installed; everything else < "latest"
			// lexicographically (digits sort before lowercase).
			want: nil,
		},
		{
			current: "1.0",
			tags:    []string{"1.0", "1.0-dev", "1.0.1", "1.0.1-dev", "2.0"},
			// "1.0" identical, "1.0-dev" is the installed variant
			// (skipped), "1.0.1-dev" is a variant of a different tag
			// (kept). 2.0 > 1.0 lexicographically.
			want: []string{"1.0.1", "1.0.1-dev", "2.0"},
		},
	}
	for _, tc := range cases {
		got := newerTagsThan(tc.current, tc.tags)
		if !equalSlices(got, tc.want) {
			t.Errorf("current=%q got=%v want=%v", tc.current, got, tc.want)
		}
	}
}

func TestNewerTagsThanFiltersFloating(t *testing.T) {
	got := newerTagsThan("8.0", []string{"latest", "stable", "edge", "main", "master", "dev", "9.0"})
	if !equalSlices(got, []string{"9.0"}) {
		t.Errorf("expected only '9.0' after filtering floating tags, got %v", got)
	}
}

func TestCosignSidecarPatternBoundaries(t *testing.T) {
	// Boundary tests for the regex. False positives here would silence
	// real tags; false negatives would let cosign noise through.
	cases := map[string]bool{
		"sha256-" + repeat("a", 64):                  true, // bare digest tag
		"sha256-" + repeat("a", 64) + ".sig":         true, // signature
		"sha256-" + repeat("0", 64) + ".att":         true, // attestation
		"sha256-" + repeat("f", 64) + ".sbom":        true, // SBOM
		"sha256-" + repeat("a", 63):                  false, // 63 hex chars, not 64
		"sha256-" + repeat("a", 65):                  false, // 65 hex chars
		"sha256-" + repeat("g", 64):                  false, // non-hex
		"sha512-" + repeat("a", 64):                  false, // wrong algorithm prefix
		"sha256-something":                           false, // not the pattern
		"v1.0":                                       false, // real semver tag
		"1.0":                                        false, // real semver tag
		"latest":                                     false, // floating tag
	}
	for in, want := range cases {
		got := cosignSidecarTagPattern.MatchString(in)
		if got != want {
			t.Errorf("pattern.MatchString(%q) = %v, want %v", in, got, want)
		}
	}
}

func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func repeat(s string, n int) string {
	out := make([]byte, 0, len(s)*n)
	for i := 0; i < n; i++ {
		out = append(out, s...)
	}
	return string(out)
}
