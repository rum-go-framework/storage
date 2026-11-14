// Package storage defines a small interface for storing blobs by key, plus
// the backends that need nothing beyond the standard library: a local
// filesystem (FS) and an in-memory one (Memory) for tests. Backends with heavy
// SDKs live in submodules (see storage/s3).
package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"
)

// ErrNotFound is returned (wrapped) by every backend when a key does not
// exist. Test for it with errors.Is.
var ErrNotFound = errors.New("storage: object not found")

// ErrInvalidKey is returned (wrapped) when a key fails ValidateKey.
var ErrInvalidKey = errors.New("storage: invalid key")

// Limits enforced everywhere to keep keys portable: S3's key length and the
// usual filesystem file name length.
const (
	maxKeyLen     = 1024
	maxSegmentLen = 255
)

// Storage keeps immutable-by-convention blobs addressed by a key. Keys are
// forward-slash separated paths (see ValidateKey). Implementations must be
// safe for concurrent use.
type Storage interface {
	// Put stores the contents of r under key, replacing any existing object.
	// A failed Put leaves any previous object intact.
	Put(ctx context.Context, key string, r io.Reader, opts PutOptions) error
	// Get returns the object's contents; the caller must close the reader.
	// A missing key yields ErrNotFound.
	Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error)
	// Stat returns the object's metadata. A missing key yields ErrNotFound.
	Stat(ctx context.Context, key string) (ObjectInfo, error)
	// Delete removes the object. Deleting a missing key is not an error, so
	// callers can retry safely.
	Delete(ctx context.Context, key string) error
}

// PutOptions tunes a Put.
type PutOptions struct {
	// ContentType is the MIME type to store with the object. Backends that
	// cannot persist it (FS) derive it from the key's extension instead.
	ContentType string
}

// ObjectInfo describes a stored object.
type ObjectInfo struct {
	Key  string
	Size int64
	// ContentType is never empty: it falls back to application/octet-stream.
	ContentType string
	ModTime     time.Time
}

const defaultContentType = "application/octet-stream"

// ValidateKey reports whether key is a safe, portable object key: a non-empty,
// valid UTF-8 string of at most 1024 bytes made of non-empty segments of at
// most 255 bytes joined by "/". It must not start or end with "/", contain "."
// or ".." segments, backslashes or NUL bytes. Every backend calls it, so a key
// accepted by one is accepted by all; the limits are those of S3 (key) and of
// common filesystems (segment). A backend may add its own budget: s3's key
// prefix counts towards the 1024 bytes.
func ValidateKey(key string) error {
	switch {
	case key == "":
		return invalidKey(key, "empty")
	case len(key) > maxKeyLen:
		return invalidKey(key, "too long")
	case !utf8.ValidString(key):
		return invalidKey(key, "not valid UTF-8")
	case strings.ContainsAny(key, "\\\x00"):
		return invalidKey(key, "contains backslash or NUL")
	case key[0] == '/':
		return invalidKey(key, "absolute")
	case key[len(key)-1] == '/':
		return invalidKey(key, "trailing slash")
	}
	for seg := range strings.SplitSeq(key, "/") {
		switch seg {
		case "":
			return invalidKey(key, "empty segment")
		case ".", "..":
			return invalidKey(key, "relative segment")
		}
		if len(seg) > maxSegmentLen {
			return invalidKey(key, "segment too long")
		}
	}
	return nil
}

func invalidKey(key, why string) error {
	return fmt.Errorf("%w: %s: %q", ErrInvalidKey, why, key)
}

func notFound(key string) error {
	return fmt.Errorf("%w: %s", ErrNotFound, key)
}

// contentType returns ct, or the default when it is empty.
func contentType(ct string) string {
	if ct == "" {
		return defaultContentType
	}
	return ct
}

// ctxReader makes a copy loop stop when ctx is cancelled, since io.Reader
// itself knows nothing about contexts.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
