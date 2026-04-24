package registry

import (
	"github.com/google/go-containerregistry/pkg/authn"
)

// dropzoneKeychain is an authn.Keychain backed by ~/.dropzone/auth.json.
// It's the first tier of our chained keychain; misses fall through to
// authn.DefaultKeychain which reads ~/.docker/config.json plus any
// credential helpers the user already has configured.
type dropzoneKeychain struct {
	store *AuthStore
}

// NewChainedKeychain returns the dropzone-first, Docker-fallback keychain
// used by the OCI client. An empty path disables the dropzone tier; that's
// what tests use so they exercise only the default (anonymous) path.
func NewChainedKeychain(authFilePath string) authn.Keychain {
	if authFilePath == "" {
		return authn.DefaultKeychain
	}
	return authn.NewMultiKeychain(
		&dropzoneKeychain{store: NewAuthStore(authFilePath)},
		authn.DefaultKeychain,
	)
}

// Resolve implements authn.Keychain. Matching happens against the resource's
// full string (e.g., "registry.example/namespace") first, then against its
// registry host only. A miss returns authn.Anonymous rather than an error
// so the multi-keychain falls through to the next tier.
func (k *dropzoneKeychain) Resolve(res authn.Resource) (authn.Authenticator, error) {
	if user, pass, ok := k.store.lookup(res.String()); ok {
		return &authn.Basic{Username: user, Password: pass}, nil
	}
	if user, pass, ok := k.store.lookup(res.RegistryStr()); ok {
		return &authn.Basic{Username: user, Password: pass}, nil
	}
	return authn.Anonymous, nil
}
