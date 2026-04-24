package registry

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTar returns a tar byte stream built from the provided entries.
// Each entry is a tar header with body. Bodies may be nil for non-regular
// entries (dirs, symlinks, hardlinks).
type tarEntry struct {
	hdr  tar.Header
	body []byte
}

func buildTar(t *testing.T, entries []tarEntry) *bytes.Reader {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		if e.body != nil {
			e.hdr.Size = int64(len(e.body))
		}
		if err := tw.WriteHeader(&e.hdr); err != nil {
			t.Fatalf("tar header %q: %v", e.hdr.Name, err)
		}
		if e.body != nil {
			if _, err := tw.Write(e.body); err != nil {
				t.Fatalf("tar body %q: %v", e.hdr.Name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	return bytes.NewReader(buf.Bytes())
}

func TestExtractRegularFilesAndDirectories(t *testing.T) {
	tmp := t.TempDir()
	r := buildTar(t, []tarEntry{
		{hdr: tar.Header{Name: "etc/", Typeflag: tar.TypeDir, Mode: 0o755}},
		{hdr: tar.Header{Name: "etc/config", Typeflag: tar.TypeReg, Mode: 0o644}, body: []byte("hello")},
		{hdr: tar.Header{Name: "usr/bin/tool", Typeflag: tar.TypeReg, Mode: 0o755}, body: []byte("binary")},
	})

	if err := extractTar(r, tmp); err != nil {
		t.Fatalf("extractTar: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(tmp, "etc/config"))
	if err != nil || string(got) != "hello" {
		t.Errorf("etc/config: body=%q err=%v", got, err)
	}

	info, err := os.Stat(filepath.Join(tmp, "usr/bin/tool"))
	if err != nil {
		t.Fatalf("usr/bin/tool stat: %v", err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Errorf("usr/bin/tool mode: got %o, want 0755", info.Mode().Perm())
	}
}

func TestExtractCreatesParentDirsForFilesWithoutExplicitDirEntry(t *testing.T) {
	tmp := t.TempDir()
	// The tar has a file but no explicit parent dir entry — common in real
	// OCI layers. The extractor must mkdir the parents implicitly.
	r := buildTar(t, []tarEntry{
		{hdr: tar.Header{Name: "a/b/c/file.txt", Typeflag: tar.TypeReg, Mode: 0o644}, body: []byte("x")},
	})
	if err := extractTar(r, tmp); err != nil {
		t.Fatalf("extractTar: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmp, "a/b/c/file.txt")); err != nil {
		t.Errorf("file.txt: %v", err)
	}
}

func TestExtractSymlink(t *testing.T) {
	tmp := t.TempDir()
	r := buildTar(t, []tarEntry{
		{hdr: tar.Header{Name: "real", Typeflag: tar.TypeReg, Mode: 0o644}, body: []byte("target")},
		{hdr: tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "real"}},
	})
	if err := extractTar(r, tmp); err != nil {
		t.Fatalf("extractTar: %v", err)
	}
	link := filepath.Join(tmp, "link")
	dst, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if dst != "real" {
		t.Errorf("symlink target: got %q, want %q", dst, "real")
	}
}

func TestExtractHardlink(t *testing.T) {
	tmp := t.TempDir()
	r := buildTar(t, []tarEntry{
		{hdr: tar.Header{Name: "orig", Typeflag: tar.TypeReg, Mode: 0o644}, body: []byte("shared")},
		{hdr: tar.Header{Name: "dup", Typeflag: tar.TypeLink, Linkname: "orig"}},
	})
	if err := extractTar(r, tmp); err != nil {
		t.Fatalf("extractTar: %v", err)
	}
	a, err1 := os.Stat(filepath.Join(tmp, "orig"))
	b, err2 := os.Stat(filepath.Join(tmp, "dup"))
	if err1 != nil || err2 != nil {
		t.Fatalf("stat: %v %v", err1, err2)
	}
	if !os.SameFile(a, b) {
		t.Error("hardlink did not produce the same inode")
	}
}

func TestExtractRejectsPathTraversal(t *testing.T) {
	tmp := t.TempDir()
	cases := []string{"../escape", "a/../../escape", "/abs/path"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			r := buildTar(t, []tarEntry{
				{hdr: tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644}, body: []byte("x")},
			})
			err := extractTar(r, tmp)
			if err == nil {
				t.Fatalf("expected error for path %q", name)
			}
			if !strings.Contains(err.Error(), "path") && !strings.Contains(err.Error(), "absolute") {
				t.Errorf("expected path-related error, got: %v", err)
			}
		})
	}
}

func TestExtractRejectsHardlinkEscape(t *testing.T) {
	tmp := t.TempDir()
	r := buildTar(t, []tarEntry{
		{hdr: tar.Header{Name: "orig", Typeflag: tar.TypeReg, Mode: 0o644}, body: []byte("x")},
		{hdr: tar.Header{Name: "escape", Typeflag: tar.TypeLink, Linkname: "../etc/passwd"}},
	})
	if err := extractTar(r, tmp); err == nil {
		t.Fatal("expected error on hardlink escape")
	}
}

func TestExtractMasksModeTo0o777(t *testing.T) {
	tmp := t.TempDir()
	// Setuid (04000) and sticky (01000) bits must be dropped.
	r := buildTar(t, []tarEntry{
		{hdr: tar.Header{Name: "suid", Typeflag: tar.TypeReg, Mode: 0o4755}, body: []byte("x")},
	})
	if err := extractTar(r, tmp); err != nil {
		t.Fatalf("extractTar: %v", err)
	}
	info, err := os.Stat(filepath.Join(tmp, "suid"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSetuid != 0 {
		t.Error("setuid bit leaked through extraction")
	}
	if info.Mode().Perm() != 0o755 {
		t.Errorf("perm: got %o, want 0755", info.Mode().Perm())
	}
}

func TestExtractSkipsSpecialFiles(t *testing.T) {
	tmp := t.TempDir()
	// Character/block devices and FIFOs get silently dropped — creating
	// them requires root on most systems and they're useless for host install.
	r := buildTar(t, []tarEntry{
		{hdr: tar.Header{Name: "dev/null", Typeflag: tar.TypeChar, Mode: 0o666}},
		{hdr: tar.Header{Name: "dev/sda", Typeflag: tar.TypeBlock, Mode: 0o660}},
		{hdr: tar.Header{Name: "dev/pipe", Typeflag: tar.TypeFifo, Mode: 0o644}},
		{hdr: tar.Header{Name: "real", Typeflag: tar.TypeReg, Mode: 0o644}, body: []byte("x")},
	})
	if err := extractTar(r, tmp); err != nil {
		t.Fatalf("extractTar: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmp, "real")); err != nil {
		t.Errorf("real file missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmp, "dev/null")); !os.IsNotExist(err) {
		t.Error("expected special file to be skipped")
	}
}
