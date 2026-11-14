package storage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"mime"
	"os"
	"path"
	"strings"
	"syscall"
)

const (
	dirPerm  = 0o700
	filePerm = 0o600
	// tmpPrefix marks in-flight Put files; keys using it are rejected so a
	// reader can never see a half-written object.
	tmpPrefix = ".storage-tmp-"
)

// FS stores objects as files below a root directory. All access goes through
// os.Root, so neither a key nor a symlink inside the directory can reach
// outside it, even if key validation had a bug.
//
// FS does not persist PutOptions.ContentType: it is derived from the key's
// extension on Get and Stat. Keys map to paths, so "a" and "a/b" cannot both
// exist. Files and directories are created owner-only (0600/0700).
type FS struct {
	root *os.Root
}

var _ Storage = (*FS)(nil)

// NewFS returns an FS rooted at dir, creating it if needed. Call Close when
// done.
func NewFS(dir string) (*FS, error) {
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	return &FS{root}, nil
}

// Close releases the root directory handle.
func (f *FS) Close() error { return f.root.Close() }

// Put writes to a temporary file next to the target and renames it into
// place, so readers see either the old or the new object, never a partial one.
func (f *FS) Put(ctx context.Context, key string, r io.Reader, _ PutOptions) (err error) {
	if err := checkFS(ctx, key); err != nil {
		return err
	}
	dir, _ := path.Split(key)
	if dir != "" {
		if err := f.root.MkdirAll(strings.TrimSuffix(dir, "/"), dirPerm); err != nil {
			return err
		}
	}
	tmp, tmpName, err := f.createTemp(dir)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			tmp.Close()
			f.root.Remove(tmpName)
		}
	}()
	if _, err = io.Copy(tmp, ctxReader{ctx, r}); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return f.root.Rename(tmpName, key)
}

// createTemp returns the file and its name relative to the root: File.Name
// is the joined host path, which Root methods reject.
func (f *FS) createTemp(dir string) (*os.File, string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, "", err
	}
	name := dir + tmpPrefix + hex.EncodeToString(b[:])
	file, err := f.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, filePerm)
	return file, name, err
}

func (f *FS) Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
	if err := checkFS(ctx, key); err != nil {
		return nil, ObjectInfo{}, err
	}
	file, err := f.root.Open(key)
	if err != nil {
		return nil, ObjectInfo{}, mapMissing(key, err)
	}
	fi, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, ObjectInfo{}, err
	}
	if !fi.Mode().IsRegular() {
		file.Close()
		return nil, ObjectInfo{}, notFound(key)
	}
	return file, fsInfo(key, fi), nil
}

func (f *FS) Stat(ctx context.Context, key string) (ObjectInfo, error) {
	if err := checkFS(ctx, key); err != nil {
		return ObjectInfo{}, err
	}
	fi, err := f.root.Stat(key)
	if err != nil {
		return ObjectInfo{}, mapMissing(key, err)
	}
	if !fi.Mode().IsRegular() {
		return ObjectInfo{}, notFound(key)
	}
	return fsInfo(key, fi), nil
}

// Delete removes only regular files: an empty directory left behind by an
// earlier Delete is not an object, so it is treated as missing.
func (f *FS) Delete(ctx context.Context, key string) error {
	if err := checkFS(ctx, key); err != nil {
		return err
	}
	fi, err := f.root.Lstat(key)
	if isMissing(err) || (err == nil && fi.IsDir()) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := f.root.Remove(key); !isMissing(err) {
		return err
	}
	return nil
}

func checkFS(ctx context.Context, key string) error {
	if err := check(ctx, key); err != nil {
		return err
	}
	if strings.HasPrefix(path.Base(key), tmpPrefix) {
		return invalidKey(key, "reserved prefix "+tmpPrefix)
	}
	return nil
}

func fsInfo(key string, fi fs.FileInfo) ObjectInfo {
	return ObjectInfo{
		Key:         key,
		Size:        fi.Size(),
		ContentType: contentType(mime.TypeByExtension(path.Ext(key))),
		ModTime:     fi.ModTime(),
	}
}

// isMissing also covers ENOTDIR, which is what a path through a regular file
// ("a/b" where "a" is a file) yields instead of ENOENT.
func isMissing(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)
}

func mapMissing(key string, err error) error {
	if isMissing(err) {
		return notFound(key)
	}
	return err
}
