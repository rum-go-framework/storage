// Package s3 is a storage.Storage backed by Amazon S3 or any S3-compatible
// server such as MinIO. It lives in its own module so that only apps using S3
// pull in aws-sdk-go-v2.
package s3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/rum-go-framework/storage"
)

const defaultContentType = "application/octet-stream"

// Storage keeps objects in one bucket, optionally below a key prefix.
type Storage struct {
	client   *awss3.Client
	uploader *transfermanager.Client
	presign  *awss3.PresignClient
	bucket   string
	prefix   string
}

var _ storage.Storage = (*Storage)(nil)

// Option configures a Storage.
type Option func(*Storage)

// WithPrefix confines the Storage to keys below prefix, so several apps or
// environments can share a bucket. The prefix is invisible to callers: keys
// they pass in and get back never contain it.
func WithPrefix(prefix string) Option {
	return func(s *Storage) {
		if prefix = strings.Trim(prefix, "/"); prefix != "" {
			s.prefix = prefix + "/"
		}
	}
}

// New returns a Storage on bucket. The app builds the client, which is where
// credentials, region and custom endpoints live; for MinIO set BaseEndpoint
// and UsePathStyle (see the package example).
func New(client *awss3.Client, bucket string, opts ...Option) *Storage {
	s := &Storage{
		client:   client,
		uploader: transfermanager.New(client),
		presign:  awss3.NewPresignClient(client),
		bucket:   bucket,
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Put streams r to S3. The length need not be known: the transfer manager
// switches to a multipart upload for large bodies, buffering one part at a
// time, and aborts it on error so no partial object appears.
func (s *Storage) Put(ctx context.Context, key string, r io.Reader, opts storage.PutOptions) error {
	if err := storage.ValidateKey(key); err != nil {
		return err
	}
	_, err := s.uploader.UploadObject(ctx, &transfermanager.UploadObjectInput{
		Bucket:      aws.String(s.bucket),
		Key:         aws.String(s.prefix + key),
		Body:        r,
		ContentType: aws.String(contentType(opts.ContentType)),
	})
	if err != nil {
		return fmt.Errorf("storage/s3: put %q: %w", key, err)
	}
	return nil
}

func (s *Storage) Get(ctx context.Context, key string) (io.ReadCloser, storage.ObjectInfo, error) {
	if err := storage.ValidateKey(key); err != nil {
		return nil, storage.ObjectInfo{}, err
	}
	out, err := s.client.GetObject(ctx, &awss3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.prefix + key),
	})
	if err != nil {
		return nil, storage.ObjectInfo{}, mapErr("get", key, err)
	}
	return out.Body, info(key, out.ContentLength, out.ContentType, out.LastModified), nil
}

func (s *Storage) Stat(ctx context.Context, key string) (storage.ObjectInfo, error) {
	if err := storage.ValidateKey(key); err != nil {
		return storage.ObjectInfo{}, err
	}
	out, err := s.client.HeadObject(ctx, &awss3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.prefix + key),
	})
	if err != nil {
		return storage.ObjectInfo{}, mapErr("stat", key, err)
	}
	return info(key, out.ContentLength, out.ContentType, out.LastModified), nil
}

// Delete is idempotent because S3's DeleteObject succeeds for missing keys.
func (s *Storage) Delete(ctx context.Context, key string) error {
	if err := storage.ValidateKey(key); err != nil {
		return err
	}
	_, err := s.client.DeleteObject(ctx, &awss3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.prefix + key),
	})
	if err != nil {
		return fmt.Errorf("storage/s3: delete %q: %w", key, err)
	}
	return nil
}

// PresignGet returns a URL that lets a client download the object with a
// plain GET for ttl, without credentials. It does not check that the object
// exists.
func (s *Storage) PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error) {
	if err := validatePresign(key, ttl); err != nil {
		return "", err
	}
	req, err := s.presign.PresignGetObject(ctx, &awss3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.prefix + key),
	}, awss3.WithPresignExpires(ttl))
	if err != nil {
		return "", fmt.Errorf("storage/s3: presign get %q: %w", key, err)
	}
	return req.URL, nil
}

// PresignPut returns a URL that lets a client upload the object with a plain
// PUT for ttl, without credentials. The content type is part of the signature:
// the client must send exactly that Content-Type header, which stops it
// storing a different type than the app intended.
func (s *Storage) PresignPut(ctx context.Context, key string, ttl time.Duration, opts storage.PutOptions) (string, error) {
	if err := validatePresign(key, ttl); err != nil {
		return "", err
	}
	req, err := s.presign.PresignPutObject(ctx, &awss3.PutObjectInput{
		Bucket:      aws.String(s.bucket),
		Key:         aws.String(s.prefix + key),
		ContentType: aws.String(contentType(opts.ContentType)),
	}, awss3.WithPresignExpires(ttl))
	if err != nil {
		return "", fmt.Errorf("storage/s3: presign put %q: %w", key, err)
	}
	return req.URL, nil
}

func validatePresign(key string, ttl time.Duration) error {
	if err := storage.ValidateKey(key); err != nil {
		return err
	}
	if ttl <= 0 {
		return errors.New("storage/s3: presign ttl must be positive")
	}
	return nil
}

func info(key string, size *int64, ct *string, mod *time.Time) storage.ObjectInfo {
	return storage.ObjectInfo{
		Key:         key,
		Size:        aws.ToInt64(size),
		ContentType: contentType(aws.ToString(ct)),
		ModTime:     aws.ToTime(mod),
	}
}

func contentType(ct string) string {
	if ct == "" {
		return defaultContentType
	}
	return ct
}

// mapErr turns S3's "no such key" answers into storage.ErrNotFound. GetObject
// reports NoSuchKey while HeadObject, which has no body to carry an error
// code, reports NotFound.
func mapErr(op, key string, err error) error {
	var noKey *types.NoSuchKey
	var notFound *types.NotFound
	if errors.As(err, &noKey) || errors.As(err, &notFound) {
		return fmt.Errorf("%w: %s", storage.ErrNotFound, key)
	}
	return fmt.Errorf("storage/s3: %s %q: %w", op, key, err)
}
