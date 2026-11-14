package storage

import (
	"bytes"
	"context"
	"io"
	"sync"
	"time"
)

// Memory is an in-memory Storage for tests. The zero value is not usable;
// create it with NewMemory.
type Memory struct {
	mu      sync.RWMutex
	objects map[string]memObject
}

type memObject struct {
	data []byte
	info ObjectInfo
}

var _ Storage = (*Memory)(nil)

// NewMemory returns an empty Memory.
func NewMemory() *Memory {
	return &Memory{objects: make(map[string]memObject)}
}

func (m *Memory) Put(ctx context.Context, key string, r io.Reader, opts PutOptions) error {
	if err := check(ctx, key); err != nil {
		return err
	}
	// Read before taking the lock: r may block.
	data, err := io.ReadAll(ctxReader{ctx, r})
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[key] = memObject{data, ObjectInfo{
		Key:         key,
		Size:        int64(len(data)),
		ContentType: contentType(opts.ContentType),
		ModTime:     time.Now(),
	}}
	return nil
}

// Get returns a reader over the stored bytes. They are never modified after
// Put, so readers share them without copying.
func (m *Memory) Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
	if err := check(ctx, key); err != nil {
		return nil, ObjectInfo{}, err
	}
	o, ok := m.lookup(key)
	if !ok {
		return nil, ObjectInfo{}, notFound(key)
	}
	return io.NopCloser(bytes.NewReader(o.data)), o.info, nil
}

func (m *Memory) Stat(ctx context.Context, key string) (ObjectInfo, error) {
	if err := check(ctx, key); err != nil {
		return ObjectInfo{}, err
	}
	o, ok := m.lookup(key)
	if !ok {
		return ObjectInfo{}, notFound(key)
	}
	return o.info, nil
}

func (m *Memory) Delete(ctx context.Context, key string) error {
	if err := check(ctx, key); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objects, key)
	return nil
}

func (m *Memory) lookup(key string) (memObject, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	o, ok := m.objects[key]
	return o, ok
}

func check(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return ValidateKey(key)
}
