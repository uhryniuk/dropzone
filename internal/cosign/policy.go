package cosign

import (
	"errors"
	"fmt"
	"regexp"
)

// Policy pins what identity is acceptable for a verified signature on
// images from a given registry. Both fields are required; an empty
// policy is rejected at use time.
//
// Policy mirrors config.CosignPolicy at runtime so the cosign package
// doesn't depend on the config package directly.
type Policy struct {
	Issuer        string
	IdentityRegex string

	// compiled is lazy: parsed on first Validate() and reused.
	compiled *regexp.Regexp
}

// Validate parses the identity regex and reports any policy issue. Must
// be called before Match(). Cheap to call repeatedly; the compiled
// regex is cached on the receiver.
func (p *Policy) Validate() error {
	if p.Issuer == "" {
		return errors.New("policy issuer is required")
	}
	if p.IdentityRegex == "" {
		return errors.New("policy identity_regex is required")
	}
	if p.compiled != nil {
		return nil
	}
	re, err := regexp.Compile(p.IdentityRegex)
	if err != nil {
		return fmt.Errorf("compile identity regex %q: %w", p.IdentityRegex, err)
	}
	p.compiled = re
	return nil
}

// Match reports whether (issuer, identity) satisfy the policy. Validate()
// must have been called first.
func (p *Policy) Match(issuer, identity string) bool {
	if issuer != p.Issuer {
		return false
	}
	if p.compiled == nil {
		return false
	}
	return p.compiled.MatchString(identity)
}

// templates lists pre-baked policies for the common Sigstore-keyless
// providers. Each is shipped with the right OIDC issuer; the caller
// supplies an IdentityRegex (except chainguard, which knows its own).
var templates = map[string]Policy{
	"github": {
		Issuer: "https://token.actions.githubusercontent.com",
	},
	"gitlab": {
		Issuer: "https://gitlab.com",
	},
	"google": {
		Issuer: "https://accounts.google.com",
	},
	"chainguard": {
		Issuer:        "https://token.actions.githubusercontent.com",
		IdentityRegex: "https://github.com/chainguard-images/images/.*",
	},
}

// ApplyTemplate returns the prefab policy for a known provider name.
// Unknown names yield an error. Callers requesting "github" or "gitlab"
// must populate IdentityRegex on the returned Policy themselves;
// "chainguard" is fully-formed.
func ApplyTemplate(name string) (Policy, error) {
	t, ok := templates[name]
	if !ok {
		return Policy{}, fmt.Errorf("unknown policy template %q (known: github, gitlab, google, chainguard)", name)
	}
	return t, nil
}
