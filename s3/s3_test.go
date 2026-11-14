package s3_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/rum-go-framework/storage"
	"github.com/rum-go-framework/storage/s3"
	"github.com/rum-go-framework/storage/storagetest"
)

func newClient(endpoint string) *awss3.Client {
	return awss3.New(awss3.Options{
		Region:       "us-east-1",
		BaseEndpoint: aws.String(endpoint),
		UsePathStyle: true,
		Credentials:  credentials.NewStaticCredentialsProvider("minioadmin", "minioadmin", ""),
		// Fail fast on the fake server instead of retrying.
		RetryMaxAttempts: 1,
	})
}

// minio returns a client and bucket for the S3-compatible server named by
// STORAGE_S3_TEST_ENDPOINT, skipping the test when it is unset. Start one with:
//
//	docker run -d -p 9000:9000 minio/minio server /data
//	STORAGE_S3_TEST_ENDPOINT=http://localhost:9000 go test ./...
//
// STORAGE_S3_TEST_ACCESS_KEY and _SECRET_KEY default to minioadmin.
func minio(t *testing.T) (*awss3.Client, string) {
	t.Helper()
	endpoint := os.Getenv("STORAGE_S3_TEST_ENDPOINT")
	if endpoint == "" {
		t.Skip("STORAGE_S3_TEST_ENDPOINT not set")
	}
	client := awss3.New(awss3.Options{
		Region:       "us-east-1",
		BaseEndpoint: aws.String(endpoint),
		UsePathStyle: true,
		Credentials: credentials.NewStaticCredentialsProvider(
			envOr("STORAGE_S3_TEST_ACCESS_KEY", "minioadmin"), envOr("STORAGE_S3_TEST_SECRET_KEY", "minioadmin"), ""),
	})
	const bucket = "storage-conformance"
	_, err := client.CreateBucket(t.Context(), &awss3.CreateBucketInput{Bucket: aws.String(bucket)})
	var owned *types.BucketAlreadyOwnedByYou
	if err != nil && !errors.As(err, &owned) {
		t.Fatalf("create bucket: %v", err)
	}
	return client, bucket
}

// newIsolated returns a Storage under a random prefix so tests can share a
// bucket.
func newIsolated(client *awss3.Client, bucket string) *s3.Storage {
	var b [8]byte
	rand.Read(b[:])
	return s3.New(client, bucket, s3.WithPrefix("t-"+hex.EncodeToString(b[:])))
}

func TestConformance(t *testing.T) {
	client, bucket := minio(t)
	storagetest.Run(t, func(testing.TB) storage.Storage { return newIsolated(client, bucket) })
}

// TestPresignRoundTrip uses the presigned URLs the way a browser would.
func TestPresignRoundTrip(t *testing.T) {
	client, bucket := minio(t)
	s := newIsolated(client, bucket)
	ctx := t.Context()

	put, err := s.PresignPut(ctx, "up/a.txt", time.Minute, storage.PutOptions{ContentType: "text/plain"})
	if err != nil {
		t.Fatal(err)
	}
	do := func(method, url, ct string, body io.Reader) *http.Response {
		t.Helper()
		req, _ := http.NewRequestWithContext(ctx, method, url, body)
		if ct != "" {
			req.Header.Set("Content-Type", ct)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { resp.Body.Close() })
		return resp
	}
	if resp := do(http.MethodPut, put, "application/json", strings.NewReader("{}")); resp.StatusCode < 400 {
		t.Errorf("PUT with another Content-Type: status %d", resp.StatusCode)
	}
	if resp := do(http.MethodPut, put, "text/plain", strings.NewReader("hello")); resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT status %d", resp.StatusCode)
	}
	info, err := s.Stat(ctx, "up/a.txt")
	if err != nil || info.Size != 5 || info.ContentType != "text/plain" {
		t.Fatalf("Stat = %+v, %v", info, err)
	}

	get, err := s.PresignGet(ctx, "up/a.txt", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	resp := do(http.MethodGet, get, "", nil)
	if body, _ := io.ReadAll(resp.Body); resp.StatusCode != http.StatusOK || string(body) != "hello" {
		t.Errorf("GET status %d body %q", resp.StatusCode, body)
	}
}

func TestContentTypeIsPersisted(t *testing.T) {
	client, bucket := minio(t)
	s := newIsolated(client, bucket)
	if err := s.Put(t.Context(), "k", strings.NewReader("x"), storage.PutOptions{ContentType: "image/png"}); err != nil {
		t.Fatal(err)
	}
	_, info, err := s.Get(t.Context(), "k")
	if err != nil || info.ContentType != "image/png" {
		t.Errorf("Get info = %+v, %v", info, err)
	}
	if err := s.Put(t.Context(), "k", strings.NewReader("x"), storage.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	if info, _ := s.Stat(t.Context(), "k"); info.ContentType != "application/octet-stream" {
		t.Errorf("default ContentType = %q", info.ContentType)
	}
}

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

type recorder struct {
	mu       sync.Mutex
	requests []string // "METHOD /path"
	headers  http.Header
}

func (r *recorder) last() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.requests[len(r.requests)-1]
}

// fakeS3 answers like S3 for a missing object and accepts writes, which is
// enough to check request shaping and error mapping without a real server.
func fakeS3(t *testing.T) (*awss3.Client, *recorder) {
	t.Helper()
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		rec.mu.Lock()
		rec.requests = append(rec.requests, r.Method+" "+r.URL.Path)
		rec.headers = r.Header.Clone()
		rec.mu.Unlock()
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `<?xml version="1.0"?><Error><Code>NoSuchKey</Code><Message>gone</Message></Error>`)
		case http.MethodHead:
			w.WriteHeader(http.StatusNotFound)
		case http.MethodPut:
			w.Header().Set("ETag", `"abc"`)
		case http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	t.Cleanup(srv.Close)
	return newClient(srv.URL), rec
}

func TestMissingMapsToErrNotFound(t *testing.T) {
	client, _ := fakeS3(t)
	s := s3.New(client, "bkt")
	if _, _, err := s.Get(t.Context(), "k"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("Get err = %v", err)
	}
	if _, err := s.Stat(t.Context(), "k"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("Stat err = %v", err)
	}
}

func TestPrefixIsAppliedAndNormalized(t *testing.T) {
	client, rec := fakeS3(t)
	s := s3.New(client, "bkt", s3.WithPrefix("/app/prod/"))
	ctx := t.Context()

	if err := s.Put(ctx, "a/b.txt", strings.NewReader("x"), storage.PutOptions{ContentType: "text/plain"}); err != nil {
		t.Fatal(err)
	}
	if got, want := rec.last(), "PUT /bkt/app/prod/a/b.txt"; got != want {
		t.Errorf("Put request = %q, want %q", got, want)
	}
	if got := rec.headers.Get("Content-Type"); got != "text/plain" {
		t.Errorf("Content-Type = %q", got)
	}
	if err := s.Delete(ctx, "a/b.txt"); err != nil {
		t.Fatal(err)
	}
	if got, want := rec.last(), "DELETE /bkt/app/prod/a/b.txt"; got != want {
		t.Errorf("Delete request = %q, want %q", got, want)
	}
}

func TestInvalidKeyNeverReachesServer(t *testing.T) {
	client, rec := fakeS3(t)
	s := s3.New(client, "bkt")
	if err := s.Put(t.Context(), "../x", strings.NewReader("x"), storage.PutOptions{}); !errors.Is(err, storage.ErrInvalidKey) {
		t.Errorf("err = %v", err)
	}
	if _, err := s.PresignGet(t.Context(), "/abs", time.Minute); !errors.Is(err, storage.ErrInvalidKey) {
		t.Errorf("presign err = %v", err)
	}
	if len(rec.requests) != 0 {
		t.Errorf("requests = %v", rec.requests)
	}
}

func TestPresign(t *testing.T) {
	s := s3.New(newClient("http://minio.test:9000"), "bkt", s3.WithPrefix("p"))

	raw, err := s.PresignGet(t.Context(), "a/b.txt", 90*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(raw)
	if u.Host != "minio.test:9000" || u.Path != "/bkt/p/a/b.txt" {
		t.Errorf("url = %s", raw)
	}
	if got := u.Query().Get("X-Amz-Expires"); got != "90" {
		t.Errorf("X-Amz-Expires = %q", got)
	}
	if u.Query().Get("X-Amz-Signature") == "" {
		t.Error("URL is not signed")
	}

	raw, err = s.PresignPut(t.Context(), "up.png", time.Hour, storage.PutOptions{ContentType: "image/png"})
	if err != nil {
		t.Fatal(err)
	}
	u, _ = url.Parse(raw)
	if !strings.Contains(u.Query().Get("X-Amz-SignedHeaders"), "content-type") {
		t.Errorf("content-type not signed: %s", raw)
	}

	for _, ttl := range []time.Duration{0, -time.Second} {
		if _, err := s.PresignGet(t.Context(), "k", ttl); err == nil {
			t.Errorf("ttl %v accepted", ttl)
		}
	}
}

func TestPrefixCountsTowardKeyLimit(t *testing.T) {
	client, rec := fakeS3(t)
	s := s3.New(client, "bkt", s3.WithPrefix(strings.Repeat("p", 99))) // stored as 100 bytes with "/"
	ctx := t.Context()
	seg := strings.Repeat("a", 255)
	fits := strings.Join([]string{seg, seg, seg, strings.Repeat("a", 1024-100-3*255-3)}, "/")
	if len(fits) != 1024-100 {
		t.Fatalf("bad fixture: %d", len(fits))
	}
	if err := s.Put(ctx, fits, strings.NewReader("x"), storage.PutOptions{}); err != nil {
		t.Fatalf("key at the limit rejected: %v", err)
	}
	n := len(rec.requests)

	tooLong := fits + "a"
	if err := storage.ValidateKey(tooLong); err != nil {
		t.Fatalf("fixture must pass core validation: %v", err)
	}
	checks := map[string]error{
		"Put":        s.Put(ctx, tooLong, strings.NewReader("x"), storage.PutOptions{}),
		"Delete":     s.Delete(ctx, tooLong),
		"Stat":       func() error { _, err := s.Stat(ctx, tooLong); return err }(),
		"Get":        func() error { _, _, err := s.Get(ctx, tooLong); return err }(),
		"PresignGet": func() error { _, err := s.PresignGet(ctx, tooLong, time.Minute); return err }(),
		"PresignPut": func() error { _, err := s.PresignPut(ctx, tooLong, time.Minute, storage.PutOptions{}); return err }(),
	}
	for op, err := range checks {
		if !errors.Is(err, storage.ErrInvalidKey) {
			t.Errorf("%s err = %v, want ErrInvalidKey", op, err)
		}
	}
	if len(rec.requests) != n {
		t.Errorf("overflowing keys reached the server: %v", rec.requests[n:])
	}
}

// endless yields data forever in small chunks and cancels ctx once enough was
// read, like a slow upload whose request is abandoned. It counts the Reads
// that still arrive afterwards.
type endless struct {
	cancel    context.CancelFunc
	read      int
	cancelled bool
	after     int
}

func (e *endless) Read(p []byte) (int, error) {
	if e.cancelled {
		e.after++
	}
	n := min(len(p), 32<<10)
	if e.read += n; e.read > 1<<20 && !e.cancelled {
		e.cancelled = true
		e.cancel()
	}
	return n, nil
}

func TestPutStopsReadingOnCancel(t *testing.T) {
	client, _ := fakeS3(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	src := &endless{cancel: cancel}

	err := s3.New(client, "bkt").Put(ctx, "k", src, storage.PutOptions{})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if src.after != 0 {
		t.Errorf("source was read %d more times after cancellation", src.after)
	}
}
