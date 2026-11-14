package storage_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rum-go-framework/storage"
	"github.com/rum-go-framework/storage/storagetest"
)

func newFS(t testing.TB) (*storage.FS, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "root")
	f, err := storage.NewFS(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f, dir
}

func TestFS(t *testing.T) {
	storagetest.Run(t, func(t testing.TB) storage.Storage {
		f, _ := newFS(t)
		return f
	})
}

func TestFSContentTypeFromExtension(t *testing.T) {
	f, _ := newFS(t)
	put := func(key string) storage.ObjectInfo {
		t.Helper()
		// The stored type is ignored by FS; the extension wins.
		if err := f.Put(t.Context(), key, strings.NewReader("x"), storage.PutOptions{ContentType: "image/png"}); err != nil {
			t.Fatal(err)
		}
		info, err := f.Stat(t.Context(), key)
		if err != nil {
			t.Fatal(err)
		}
		return info
	}
	if got := put("a.json").ContentType; got != "application/json" {
		t.Errorf("a.json = %q", got)
	}
	if got := put("noext").ContentType; got != "application/octet-stream" {
		t.Errorf("noext = %q", got)
	}
}

func TestFSLeavesNoTempFiles(t *testing.T) {
	f, dir := newFS(t)
	for range 3 {
		if err := f.Put(t.Context(), "a/b.txt", strings.NewReader("x"), storage.PutOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Put(t.Context(), "a/c.txt", errReader{}, storage.PutOptions{}); err == nil {
		t.Fatal("expected error")
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "a"))
	if len(entries) != 1 || entries[0].Name() != "b.txt" {
		t.Errorf("dir = %v", entries)
	}
}

func TestFSRejectsReservedTempPrefix(t *testing.T) {
	f, _ := newFS(t)
	err := f.Put(t.Context(), "a/.storage-tmp-x", strings.NewReader("x"), storage.PutOptions{})
	if !errors.Is(err, storage.ErrInvalidKey) {
		t.Errorf("err = %v", err)
	}
}

func TestFSTraversal(t *testing.T) {
	f, dir := newFS(t)
	outside := filepath.Join(filepath.Dir(dir), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"../secret.txt", "a/../../secret.txt", outside, "/etc/passwd", `..\secret.txt`} {
		if _, _, err := f.Get(t.Context(), key); err == nil {
			t.Errorf("Get(%q) succeeded", key)
		}
		if err := f.Put(t.Context(), key, strings.NewReader("pwn"), storage.PutOptions{}); err == nil {
			t.Errorf("Put(%q) succeeded", key)
		}
		if err := f.Delete(t.Context(), key); err == nil {
			t.Errorf("Delete(%q) succeeded", key)
		}
	}
	assertOutsideIntact(t, outside)
}

// TestFSSymlinkEscape plants symlinks pointing outside the root, as an
// attacker with write access to the directory (or a bug elsewhere) could.
func TestFSSymlinkEscape(t *testing.T) {
	f, dir := newFS(t)
	outsideDir := t.TempDir()
	outside := filepath.Join(outsideDir, "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	for link, target := range map[string]string{"dirlink": outsideDir, "filelink": outside} {
		if err := os.Symlink(target, filepath.Join(dir, link)); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
	}

	for _, key := range []string{"dirlink/secret.txt", "filelink"} {
		if _, _, err := f.Get(t.Context(), key); err == nil {
			t.Errorf("Get(%q) followed a symlink out of the root", key)
		}
		if _, err := f.Stat(t.Context(), key); err == nil {
			t.Errorf("Stat(%q) followed a symlink out of the root", key)
		}
	}
	for _, key := range []string{"dirlink/secret.txt", "dirlink/new.txt", "dirlink/sub/new.txt"} {
		if err := f.Put(t.Context(), key, strings.NewReader("pwn"), storage.PutOptions{}); err == nil {
			t.Errorf("Put(%q) wrote through a symlink out of the root", key)
		}
		if err := f.Delete(t.Context(), key); err == nil {
			t.Errorf("Delete(%q) acted through a symlink out of the root", key)
		}
	}
	assertOutsideIntact(t, outside)
	if _, err := os.Stat(filepath.Join(outsideDir, "new.txt")); err == nil {
		t.Error("file created outside the root")
	}

	// Replacing a file symlink swaps the link itself, never its target.
	if err := f.Put(t.Context(), "filelink", strings.NewReader("mine"), storage.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	assertOutsideIntact(t, outside)
}

func assertOutsideIntact(t *testing.T, path string) {
	t.Helper()
	if b, err := os.ReadFile(path); err != nil || string(b) != "secret" {
		t.Errorf("outside file changed: %q, %v", b, err)
	}
}

func TestFSPersistsAcrossInstances(t *testing.T) {
	f, dir := newFS(t)
	if err := f.Put(t.Context(), "k", strings.NewReader("x"), storage.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	g, err := storage.NewFS(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	if _, err := g.Stat(t.Context(), "k"); err != nil {
		t.Error(err)
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("boom") }

// TestFSAcceptsEveryValidKey guards the portability promise of ValidateKey:
// the longest segment it allows must be storable.
func TestFSAcceptsEveryValidKey(t *testing.T) {
	f, _ := newFS(t)
	key := strings.Repeat("a", 255) + "/" + strings.Repeat("b", 255)
	if err := f.Put(t.Context(), key, strings.NewReader("x"), storage.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Stat(t.Context(), key); err != nil {
		t.Fatal(err)
	}
}
