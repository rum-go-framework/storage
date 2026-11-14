package storage_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/rum-go-framework/storage"
)

func TestValidateKey(t *testing.T) {
	seg := strings.Repeat("a", 255)
	maxLen := strings.Join([]string{seg, seg, seg, seg[:254], "a"}, "/") // 1024 bytes
	valid := []string{"a", "a/b", "dir/file.txt", strings.Repeat("a", 255), strings.Repeat("a", 255) + "/" + strings.Repeat("b", 255), "a b/ü.png", ".hidden", "a..b", "..a/b", maxLen}
	for _, k := range valid {
		if err := storage.ValidateKey(k); err != nil {
			t.Errorf("ValidateKey(%q) = %v", k, err)
		}
	}
	invalid := []string{
		"", "/", "/abs", "a/", "a//b", ".", "..", "../x", "a/..", "a/../../x", "a/./b",
		`a\b`, `..\x`, "a\x00b", "\xff", strings.Repeat("a", 1025),
		strings.Repeat("a", 256), "x/" + strings.Repeat("a", 256),
	}
	for _, k := range invalid {
		if err := storage.ValidateKey(k); !errors.Is(err, storage.ErrInvalidKey) {
			t.Errorf("ValidateKey(%q) = %v, want ErrInvalidKey", k, err)
		}
	}
}
