//go:build linux

package filemanager

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestSanitize(t *testing.T) {
	m := NewManager(t.TempDir())

	cases := map[string]string{
		"/":             m.Root,
		"":              m.Root,
		"/a/b":          filepath.Join(m.Root, "a/b"),
		"../../etc":     filepath.Join(m.Root, "etc"),
		"/a/../../../x": filepath.Join(m.Root, "x"),
		"..":            m.Root,
	}
	for in, want := range cases {
		got, err := m.sanitize(in)
		if err != nil {
			t.Fatalf("sanitize(%q): %v", in, err)
		}
		if got != want {
			t.Fatalf("sanitize(%q) = %q, want %q", in, got, want)
		}
	}
}

func upload(t *testing.T, m *Manager, dir, filename string, content []byte) error {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	fw, err := w.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	fw.Write(content)
	w.Close()

	req := httptest.NewRequest(http.MethodPost, "/", &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	if err := req.ParseMultipartForm(1 << 20); err != nil {
		t.Fatal(err)
	}
	f, hdr, err := req.FormFile("file")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	// multipart strips directories from the name; restore the raw value to
	// exercise our own sanitising.
	hdr.Filename = filename
	return m.Upload(dir, f, hdr)
}

func TestUploadTraversal(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	m := NewManager(root)

	if err := upload(t, m, "/sub", "../../evil.txt", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(base, "evil.txt")); err == nil {
		t.Fatal("upload escaped the root directory")
	}
	if _, err := os.Stat(filepath.Join(root, "sub", "evil.txt")); err != nil {
		t.Fatalf("expected file inside root: %v", err)
	}

	if err := upload(t, m, "/", "..", []byte("x")); err == nil {
		t.Fatal("expected error for invalid name")
	}

	m.MaxSize = 4
	if err := upload(t, m, "/", "big.bin", []byte("12345")); err == nil {
		t.Fatal("expected size error")
	}
	if _, err := os.Stat(filepath.Join(root, "big.bin")); err == nil {
		t.Fatal("oversized upload left a file behind")
	}
}

func TestDeleteRenameRoot(t *testing.T) {
	m := NewManager(t.TempDir())
	if err := m.Delete("/"); err == nil {
		t.Fatal("deleting root must fail")
	}
	if err := m.Delete("/../.."); err == nil {
		t.Fatal("deleting root via traversal must fail")
	}
	os.WriteFile(filepath.Join(m.Root, "a"), []byte("1"), 0644)
	os.WriteFile(filepath.Join(m.Root, "b"), []byte("2"), 0644)
	if err := m.Rename("/a", "/b"); err == nil {
		t.Fatal("rename over existing file must fail")
	}
	if err := m.Rename("/a", "/c"); err != nil {
		t.Fatal(err)
	}
}
