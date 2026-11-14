package storage_test

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/rum-go-framework/storage"
)

// The app depends on storage.Storage; the backend is chosen once at startup.
func Example() {
	var s storage.Storage = storage.NewMemory()
	ctx := context.Background()

	_ = s.Put(ctx, "avatars/42.txt", strings.NewReader("hello"), storage.PutOptions{ContentType: "text/plain"})

	rc, info, _ := s.Get(ctx, "avatars/42.txt")
	defer rc.Close()
	body, _ := io.ReadAll(rc)
	fmt.Println(string(body), info.Size, info.ContentType)
	// Output: hello 5 text/plain
}
