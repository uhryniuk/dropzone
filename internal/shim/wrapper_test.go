package shim

import (
	"strings"
	"testing"
)

func TestGenerateWrapperLinuxDynamic(t *testing.T) {
	s, err := GenerateWrapper(WrapperInput{
		Name:        "jq",
		Digest:      "sha256:abc",
		PackagesDir: "/home/alice/.dropzone/packages",
		Entrypoint:  &Entrypoint{Path: "usr/bin/jq", BakedArgs: nil, Format: "elf"},
		LoaderRel:   "lib64/ld-linux-x86-64.so.2",
		HostOS:      "linux",
	})
	if err != nil {
		t.Fatalf("GenerateWrapper: %v", err)
	}
	if !strings.HasPrefix(s, "#!/bin/sh\n") {
		t.Errorf("missing shebang: %q", s[:20])
	}
	if !strings.Contains(s, Marker+" jq sha256:abc\n") {
		t.Errorf("marker line missing: %q", s)
	}
	if !strings.Contains(s, "'/home/alice/.dropzone/packages'/'jq'/current") {
		t.Errorf("PKG line not baked: %q", s)
	}
	if !strings.Contains(s, `"$ROOT"/lib64/ld-linux-x86-64.so.2`) {
		t.Errorf("loader invocation missing: %q", s)
	}
	if !strings.Contains(s, `--library-path "$ROOT/usr/lib:$ROOT/lib:$ROOT/usr/local/lib:$ROOT/usr/lib64:$ROOT/lib64"`) {
		t.Errorf("library path missing: %q", s)
	}
	if !strings.Contains(s, `"$ROOT"/usr/bin/jq`) {
		t.Errorf("entrypoint missing: %q", s)
	}
	if !strings.HasSuffix(s, ` "$@"`+"\n") {
		t.Errorf("missing trailing user args: %q", s)
	}
}

func TestGenerateWrapperLinuxStatic(t *testing.T) {
	// Empty LoaderRel means "static binary": wrapper skips the loader path.
	s, err := GenerateWrapper(WrapperInput{
		Name:        "tool",
		Digest:      "sha256:abc",
		PackagesDir: "/p",
		Entrypoint:  &Entrypoint{Path: "bin/tool"},
		LoaderRel:   "",
		HostOS:      "linux",
	})
	if err != nil {
		t.Fatalf("GenerateWrapper: %v", err)
	}
	if strings.Contains(s, "--library-path") {
		t.Errorf("static wrapper should not set --library-path: %q", s)
	}
	if !strings.Contains(s, `exec "$ROOT"/bin/tool "$@"`) {
		t.Errorf("static exec line wrong: %q", s)
	}
}

func TestGenerateWrapperLinuxWithBakedArgs(t *testing.T) {
	s, _ := GenerateWrapper(WrapperInput{
		Name:        "tool",
		Digest:      "sha256:abc",
		PackagesDir: "/p",
		Entrypoint: &Entrypoint{
			Path:      "bin/tool",
			BakedArgs: []string{"--default", "arg with spaces", "it's"},
		},
		LoaderRel: "",
		HostOS:    "linux",
	})
	// Baked args must be single-quoted; the one with spaces must stay one
	// argument; the one with a single quote must use the '\'' escape.
	if !strings.Contains(s, " '--default'") {
		t.Errorf("baked flag not escaped: %q", s)
	}
	if !strings.Contains(s, " 'arg with spaces'") {
		t.Errorf("space arg not escaped: %q", s)
	}
	if !strings.Contains(s, `'it'\''s'`) {
		t.Errorf("single-quote escape wrong: %q", s)
	}
	// Trailing user args marker must still be last.
	if !strings.HasSuffix(s, ` "$@"`+"\n") {
		t.Errorf("user args not last: %q", s)
	}
}

func TestGenerateWrapperDarwin(t *testing.T) {
	s, err := GenerateWrapper(WrapperInput{
		Name:        "jq",
		Digest:      "sha256:abc",
		PackagesDir: "/Users/alice/.dropzone/packages",
		Entrypoint:  &Entrypoint{Path: "usr/bin/jq"},
		HostOS:      "darwin",
	})
	if err != nil {
		t.Fatalf("GenerateWrapper: %v", err)
	}
	// macOS doesn't bundle its own loader; wrapper sets DYLD env vars
	// and execs the binary directly.
	if !strings.Contains(s, "DYLD_FALLBACK_LIBRARY_PATH=") {
		t.Errorf("darwin wrapper missing DYLD env: %q", s)
	}
	if strings.Contains(s, "--library-path") {
		t.Errorf("darwin wrapper should not use --library-path: %q", s)
	}
	if !strings.Contains(s, `exec "$ROOT"/usr/bin/jq`) {
		t.Errorf("entrypoint exec missing: %q", s)
	}
}

func TestGenerateWrapperUnsupportedOS(t *testing.T) {
	_, err := GenerateWrapper(WrapperInput{
		Name: "x", Digest: "d", PackagesDir: "/p",
		Entrypoint: &Entrypoint{Path: "x"},
		HostOS:     "windows",
	})
	if err == nil {
		t.Error("expected error for unsupported HostOS")
	}
}

func TestShellEscape(t *testing.T) {
	cases := map[string]string{
		"plain":              "'plain'",
		"with space":         "'with space'",
		"it's":               `'it'\''s'`,
		`$HOME`:              "'$HOME'",
		``:                   "''",
		`multi'quote'dance`:  `'multi'\''quote'\''dance'`,
	}
	for in, want := range cases {
		got := shellEscape(in)
		if got != want {
			t.Errorf("shellEscape(%q) = %q, want %q", in, got, want)
		}
	}
}
