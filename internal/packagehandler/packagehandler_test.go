package packagehandler

import (
	"errors"
	"testing"
)

// These are stub-only tests. The real behavior lands in phases 1-5 per
// docs/roadmap.md; tests for the actual install/list/remove flows will be
// written alongside those implementations.

func TestStubsReturnNotReimplemented(t *testing.T) {
	h := New(nil, nil)

	cases := []struct {
		name string
		run  func() error
	}{
		{"InstallPackage", func() error { return h.InstallPackage("ignored") }},
		{"RemovePackage", func() error { return h.RemovePackage("ignored") }},
		{"ListInstalled", func() error { return h.ListInstalled() }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.run(); !errors.Is(err, errNotReimplemented) {
				t.Fatalf("%s: want errNotReimplemented, got %v", tc.name, err)
			}
		})
	}
}
