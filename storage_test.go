package storage_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/rum-go-framework/storage"
)

func TestValidateKey(t *testing.T) {
	valid := []string{"a", "a/b", "dir/file.txt", "a b/ü.png", ".hidden", "a..b", "..a/b", strings.Repeat("a", 1024)}
	for _, k := range valid {
		if err := storage.ValidateKey(k); err != nil {
			t.Errorf("ValidateKey(%q) = %v", k, err)
		}
	}
	invalid := []string{
		"", "/", "/abs", "a/", "a//b", ".", "..", "../x", "a/..", "a/../../x", "a/./b",
		`a\b`, `..\x`, "a\x00b", "\xff", strings.Repeat("a", 1025),
	}
	for _, k := range invalid {
		if err := storage.ValidateKey(k); !errors.Is(err, storage.ErrInvalidKey) {
			t.Errorf("ValidateKey(%q) = %v, want ErrInvalidKey", k, err)
		}
	}
}
