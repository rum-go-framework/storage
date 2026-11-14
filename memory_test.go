package storage_test

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/rum-go-framework/storage"
	"github.com/rum-go-framework/storage/storagetest"
)

func TestMemory(t *testing.T) {
	storagetest.Run(t, func(testing.TB) storage.Storage { return storage.NewMemory() })
}

func TestMemoryKeepsContentType(t *testing.T) {
	m := storage.NewMemory()
	if err := m.Put(t.Context(), "k", strings.NewReader("x"), storage.PutOptions{ContentType: "image/png"}); err != nil {
		t.Fatal(err)
	}
	if info, _ := m.Stat(t.Context(), "k"); info.ContentType != "image/png" {
		t.Errorf("ContentType = %q", info.ContentType)
	}
	if err := m.Put(t.Context(), "k", strings.NewReader("x"), storage.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	if info, _ := m.Stat(t.Context(), "k"); info.ContentType != "application/octet-stream" {
		t.Errorf("default ContentType = %q", info.ContentType)
	}
}

func TestMemoryIsolatesCallerBuffers(t *testing.T) {
	m := storage.NewMemory()
	buf := []byte("hello")
	if err := m.Put(t.Context(), "k", strings.NewReader(string(buf)), storage.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	buf[0] = 'J'
	rc, _, _ := m.Get(t.Context(), "k")
	got, _ := io.ReadAll(rc)
	if string(got) != "hello" {
		t.Errorf("got %q", got)
	}
}

func TestMemoryConcurrent(t *testing.T) {
	m := storage.NewMemory()
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() {
			key := fmt.Sprintf("k%d", i%4)
			_ = m.Put(t.Context(), key, strings.NewReader("data"), storage.PutOptions{})
			_, _ = m.Stat(t.Context(), key)
			if rc, _, err := m.Get(t.Context(), key); err == nil {
				rc.Close()
			}
			_ = m.Delete(t.Context(), key)
		})
	}
	wg.Wait()
}
