# storage

A small blob-storage interface for Go apps, with a stdlib-only core and heavy
provider SDKs kept in submodules. The app depends on `storage.Storage` and
picks a backend from its config.

```go
type Storage interface {
	Put(ctx context.Context, key string, r io.Reader, opts storage.PutOptions) error
	Get(ctx context.Context, key string) (io.ReadCloser, storage.ObjectInfo, error)
	Stat(ctx context.Context, key string) (storage.ObjectInfo, error)
	Delete(ctx context.Context, key string) error
}
```

## Backends

| Backend | Module | Notes |
|---|---|---|
| `storage.NewFS(dir)` | `github.com/rum-go-framework/storage` | local directory via `os.Root`; call `Close` when done |
| `storage.NewMemory()` | `github.com/rum-go-framework/storage` | tests: concurrency-safe, nothing persisted |
| `s3.New(client, bucket)` | `github.com/rum-go-framework/storage/s3` | aws-sdk-go-v2; AWS S3 or MinIO |

## Semantics

Every backend behaves the same way, and `storagetest.Run` checks it.

- **Keys** are forward-slash paths. `storage.ValidateKey` (called by every
  backend, errors wrap `ErrInvalidKey`) rejects empty keys, a leading or
  trailing `/`, empty, `.` and `..` segments, backslashes, NUL, invalid UTF-8
  and keys over 1024 bytes.
- **Missing objects** yield an error wrapping `ErrNotFound` from `Get` and
  `Stat`; test it with `errors.Is`. A key that is only a prefix of other keys
  (`dir` when `dir/x` exists) is missing too.
- **`Delete` of a missing key returns nil**, like S3, so retries are safe.
- **`Put` replaces** an existing object. A failed or cancelled `Put` leaves the
  previous object intact and creates no partial one.
- `ObjectInfo.ContentType` is never empty (`application/octet-stream` by
  default). `Memory` and `s3` store `PutOptions.ContentType`; **`FS` does not**:
  it derives the type from the key's extension on `Get`/`Stat`.
- Signed URLs and listing are not in the interface. Backends expose them as
  their own methods (`s3.Storage.PresignGet`, `PresignPut`).

## Filesystem backend

All access goes through `os.Root`, so a bug in key validation, or a symlink
planted inside the directory, still cannot reach outside it. `Put` writes a
temporary file next to the target, syncs it and renames it into place, so
readers never see a partial object. Files and directories are created
owner-only (0600/0700). Because keys map to paths, `a` and `a/b` cannot both
exist, and keys whose last segment starts with `.storage-tmp-` are reserved.

```go
fs, err := storage.NewFS("/var/lib/app/uploads")
if err != nil {
	return err
}
defer fs.Close()
```

## S3 and MinIO

The app builds the client; that is where credentials, region and a custom
endpoint go. For MinIO, set the endpoint and path-style addressing.

```go
cfg, err := config.LoadDefaultConfig(ctx)
client := awss3.NewFromConfig(cfg, func(o *awss3.Options) {
	o.BaseEndpoint = aws.String("http://localhost:9000") // omit for AWS
	o.UsePathStyle = true                                // MinIO
})
st := s3.New(client, "my-bucket", s3.WithPrefix("uploads"))

url, err := st.PresignGet(ctx, "avatars/42.png", 15*time.Minute)
```

`Put` accepts any `io.Reader`: the SDK's transfer manager switches to a
multipart upload for large bodies and aborts it on error. `WithPrefix` confines
the storage to a key prefix that callers never see. `PresignPut` signs the
`Content-Type`, so the uploader must send exactly that header.

## Testing

`storagetest.Run(t, func(t testing.TB) storage.Storage)` is the conformance
suite used by every backend. The `s3` tests that need a real server run only
when `STORAGE_S3_TEST_ENDPOINT` is set:

```sh
docker run -d -p 9000:9000 <minio-image> server /data   # any MinIO image
STORAGE_S3_TEST_ENDPOINT=http://localhost:9000 go test ./...
```

`STORAGE_S3_TEST_ACCESS_KEY` and `STORAGE_S3_TEST_SECRET_KEY` default to
`minioadmin`.

## Local development

The `s3` submodule requires `storage v0.1.0`; until that tag is published,
resolve it with a `go.work` that uses both `./storage` and `./storage/s3`,
plus `replace github.com/rum-go-framework/storage v0.1.0 => ./storage`.

## License

See LICENSE.
