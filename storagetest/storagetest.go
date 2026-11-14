// Package storagetest is a conformance suite for storage.Storage backends.
package storagetest

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/rum-go-framework/storage"
)

// Run exercises the behavior every backend must share. newStorage is called
// once per subtest and must return an empty, isolated Storage (use t.Cleanup
// to release it).
func Run(t *testing.T, newStorage func(t testing.TB) storage.Storage) {
	t.Helper()
	tests := map[string]func(*testing.T, storage.Storage){
		"RoundTrip":          roundTrip,
		"Overwrite":          overwrite,
		"EmptyObject":        emptyObject,
		"LargeObject":        largeObject,
		"Missing":            missing,
		"DeleteIsIdempotent": deleteIsIdempotent,
		"InvalidKeys":        invalidKeys,
		"CancelledContext":   cancelledContext,
		"FailedPutKeepsOld":  failedPutKeepsOld,
	}
	for name, fn := range tests {
		t.Run(name, func(t *testing.T) { fn(t, newStorage(t)) })
	}
}

func put(t *testing.T, s storage.Storage, key, body string) {
	t.Helper()
	if err := s.Put(t.Context(), key, strings.NewReader(body), storage.PutOptions{ContentType: "text/plain"}); err != nil {
		t.Fatalf("Put(%q): %v", key, err)
	}
}

func read(t *testing.T, s storage.Storage, key string) (string, storage.ObjectInfo) {
	t.Helper()
	rc, info, err := s.Get(t.Context(), key)
	if err != nil {
		t.Fatalf("Get(%q): %v", key, err)
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read %q: %v", key, err)
	}
	return string(b), info
}

func roundTrip(t *testing.T, s storage.Storage) {
	const key = "docs/2026/report.txt"
	put(t, s, key, "hello")

	got, info := read(t, s, key)
	if got != "hello" {
		t.Errorf("body = %q", got)
	}
	st, err := s.Stat(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	if st.Key != key || st.Size != 5 || st.ContentType == "" || st.ModTime.IsZero() {
		t.Errorf("Stat = %+v", st)
	}
	if info.Key != st.Key || info.Size != st.Size {
		t.Errorf("Get info %+v differs from Stat %+v", info, st)
	}
}

func overwrite(t *testing.T, s storage.Storage) {
	put(t, s, "k", "one")
	put(t, s, "k", "second")
	if got, info := read(t, s, "k"); got != "second" || info.Size != 6 {
		t.Errorf("got %q, %+v", got, info)
	}
}

func emptyObject(t *testing.T, s storage.Storage) {
	put(t, s, "empty", "")
	if got, info := read(t, s, "empty"); got != "" || info.Size != 0 {
		t.Errorf("got %q, %+v", got, info)
	}
}

// largeObject is bigger than any single-part upload threshold in the
// backends, and is fed through a reader of unknown length.
func largeObject(t *testing.T, s storage.Storage) {
	body := bytes.Repeat([]byte("0123456789abcdef"), 1<<20) // 16 MiB
	err := s.Put(t.Context(), "big/blob.bin", io.MultiReader(bytes.NewReader(body)), storage.PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got, info := read(t, s, "big/blob.bin")
	if info.Size != int64(len(body)) || got != string(body) {
		t.Errorf("size = %d, equal = %v", info.Size, got == string(body))
	}
}

func missing(t *testing.T, s storage.Storage) {
	// "dir/x" makes "dir" a prefix, which must not count as an object.
	put(t, s, "dir/x", "x")
	for _, key := range []string{"nope", "dir", "dir/x/y", "dir/y"} {
		if _, _, err := s.Get(t.Context(), key); !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("Get(%q) err = %v", key, err)
		}
		if _, err := s.Stat(t.Context(), key); !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("Stat(%q) err = %v", key, err)
		}
	}
}

func deleteIsIdempotent(t *testing.T, s storage.Storage) {
	put(t, s, "a/b", "x")
	for range 2 {
		if err := s.Delete(t.Context(), "a/b"); err != nil {
			t.Fatalf("Delete: %v", err)
		}
	}
	if _, err := s.Stat(t.Context(), "a/b"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("Stat after Delete err = %v", err)
	}
	for _, key := range []string{"never-existed", "a", "a/b/c"} {
		if err := s.Delete(t.Context(), key); err != nil {
			t.Errorf("Delete(%q) = %v", key, err)
		}
	}
}

func invalidKeys(t *testing.T, s storage.Storage) {
	for _, key := range []string{"", "/abs", "a/", "a//b", "../x", "a/../../x", "a/./b", `a\b`, "a\x00b"} {
		ctx := t.Context()
		if err := s.Put(ctx, key, strings.NewReader("x"), storage.PutOptions{}); !errors.Is(err, storage.ErrInvalidKey) {
			t.Errorf("Put(%q) err = %v", key, err)
		}
		if _, _, err := s.Get(ctx, key); !errors.Is(err, storage.ErrInvalidKey) {
			t.Errorf("Get(%q) err = %v", key, err)
		}
		if _, err := s.Stat(ctx, key); !errors.Is(err, storage.ErrInvalidKey) {
			t.Errorf("Stat(%q) err = %v", key, err)
		}
		if err := s.Delete(ctx, key); !errors.Is(err, storage.ErrInvalidKey) {
			t.Errorf("Delete(%q) err = %v", key, err)
		}
	}
}

func cancelledContext(t *testing.T, s storage.Storage) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := s.Put(ctx, "k", strings.NewReader("x"), storage.PutOptions{}); !errors.Is(err, context.Canceled) {
		t.Errorf("Put err = %v", err)
	}
	if _, err := s.Stat(t.Context(), "k"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("object exists after cancelled Put: %v", err)
	}
	if _, _, err := s.Get(ctx, "k"); !errors.Is(err, context.Canceled) {
		t.Errorf("Get err = %v", err)
	}
}

var errBoom = errors.New("boom")

func failedPutKeepsOld(t *testing.T, s storage.Storage) {
	put(t, s, "k", "old")
	failing := func() io.Reader { return io.MultiReader(strings.NewReader("partial"), errReader{}) }
	if err := s.Put(t.Context(), "k", failing(), storage.PutOptions{}); err == nil {
		t.Fatal("Put with failing reader succeeded")
	}
	if got, _ := read(t, s, "k"); got != "old" {
		t.Errorf("body = %q, want old", got)
	}
	if err := s.Put(t.Context(), "fresh", failing(), storage.PutOptions{}); err == nil {
		t.Fatal("Put with failing reader succeeded")
	}
	if _, err := s.Stat(t.Context(), "fresh"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("partial object created: %v", err)
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errBoom }
