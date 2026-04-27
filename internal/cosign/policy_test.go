package cosign

import (
	"testing"
)

func TestPolicyValidate(t *testing.T) {
	cases := []struct {
		name    string
		policy  Policy
		wantErr bool
	}{
		{"valid", Policy{Issuer: "https://issuer", IdentityRegex: ".*"}, false},
		{"missing issuer", Policy{IdentityRegex: ".*"}, true},
		{"missing regex", Policy{Issuer: "https://issuer"}, true},
		{"bad regex", Policy{Issuer: "https://x", IdentityRegex: "[unbalanced"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.policy.Validate()
			if (err != nil) != tc.wantErr {
				t.Errorf("Validate: got err=%v, wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

func TestPolicyValidateCachesCompiledRegex(t *testing.T) {
	p := Policy{Issuer: "https://x", IdentityRegex: ".*"}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	first := p.compiled
	if first == nil {
		t.Fatal("compiled regex not cached after Validate")
	}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	if p.compiled != first {
		t.Error("Validate recompiled the regex; should be cached")
	}
}

func TestPolicyMatch(t *testing.T) {
	p := Policy{
		Issuer:        "https://token.actions.githubusercontent.com",
		IdentityRegex: "https://github.com/chainguard-images/images/.*",
	}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		issuer   string
		identity string
		want     bool
	}{
		{p.Issuer, "https://github.com/chainguard-images/images/.github/workflows/release.yaml@refs/heads/main", true},
		{p.Issuer, "https://github.com/some-other-org/images/...", false},
		{"https://wrong.example", "https://github.com/chainguard-images/images/whatever", false},
	}
	for _, tc := range cases {
		got := p.Match(tc.issuer, tc.identity)
		if got != tc.want {
			t.Errorf("Match(%q, %q) = %v, want %v", tc.issuer, tc.identity, got, tc.want)
		}
	}
}

func TestApplyTemplate(t *testing.T) {
	chain, err := ApplyTemplate("chainguard")
	if err != nil {
		t.Fatal(err)
	}
	if chain.Issuer == "" || chain.IdentityRegex == "" {
		t.Errorf("chainguard template should be fully formed, got %+v", chain)
	}

	gh, err := ApplyTemplate("github")
	if err != nil {
		t.Fatal(err)
	}
	if gh.Issuer != "https://token.actions.githubusercontent.com" {
		t.Errorf("github issuer wrong: %q", gh.Issuer)
	}
	// github template intentionally has no IdentityRegex; caller must fill in.
	if gh.IdentityRegex != "" {
		t.Errorf("github template should leave IdentityRegex empty for caller, got %q", gh.IdentityRegex)
	}

	if _, err := ApplyTemplate("bogus"); err == nil {
		t.Error("ApplyTemplate should error on unknown template")
	}
}
