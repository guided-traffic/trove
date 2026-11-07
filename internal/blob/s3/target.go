package s3

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/guided-traffic/trove/internal/backup/target"
	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/outbound"
)

// maxCopySingle is the largest object CopyObject moves in one request (R-BAK-006).
const maxCopySingle int64 = 5 << 30

// The defaults of the bound and the keep-alive of a server-side copy.
const (
	defaultCopyBase      = 2 * time.Minute
	defaultCopyRate      = 8 << 20
	defaultProgressEvery = 10 * time.Second
	defaultAbortGrace    = 10 * time.Second
)

// deleteBatch is the number of keys of one DeleteObjects request.
const deleteBatch = 1000

// minPart is the smallest part of a multipart upload but the last.
const minPart int64 = 5 << 20

// Target is the target.Store of the backup lane (R-BAK-002) over one bucket. It has its own client, credential chain and
// retry settings, built from backup.target only: it never reads storage.s3 and so never uses the runtime blob-store
// credential (R-BAK-015). Every write carries backup.target.sse (R-BAK-026).
//
// Keys are the full object keys; the caller joins backup.target.prefix, as the pool and backup paths of R-BAK-002 do.
type Target struct {
	c        *Client
	sseMode  string
	kmsKeyID string
	partSize int64
	copyPart int64
	copyMax  int64
	// copyBase and copyRate bound a server-side copy: base plus size at rate (bytes per second). A copy that has not finished
	// by then is given up and reported as ErrServerCopyUnavailable, so that the caller streams instead.
	copyBase time.Duration
	copyRate int64
	// progressEvery is how often ObjectRef.Progress is called while a request of a copy is outstanding.
	progressEvery time.Duration
	// abortGrace bounds the abort of an unfinished multipart upload, which runs after the caller's context is gone.
	abortGrace time.Duration
	// noCond is set once the store rejected a conditional write; IfAbsent then checks with a HEAD first.
	noCond atomic.Bool
}

var _ target.Store = (*Target)(nil)

// TargetOption changes a Target; tests use them to reach the large-object paths with small objects.
type TargetOption func(*Target)

// WithWritePartSize overrides backup.database.part_size, the part size of Writer.
func WithWritePartSize(n int64) TargetOption { return func(t *Target) { t.partSize = n } }

// WithCopyPartSize overrides backup.copy.part_size, the part size of UploadPartCopy.
func WithCopyPartSize(n int64) TargetOption { return func(t *Target) { t.copyPart = n } }

// WithCopyBound sets the time a server-side copy may take: base plus size divided by rate (bytes per second). The defaults are
// 2 minutes and 8 MiB/s.
func WithCopyBound(base time.Duration, rate int64) TargetOption {
	return func(t *Target) { t.copyBase, t.copyRate = base, rate }
}

// WithProgressInterval sets how often ObjectRef.Progress is called while a copy request is outstanding (default 10 s).
func WithProgressInterval(d time.Duration) TargetOption {
	return func(t *Target) { t.progressEvery = d }
}

// WithIdleTimeout overrides the time without a moved byte after which an upload or download is cancelled (default one
// minute).
func WithIdleTimeout(d time.Duration) TargetOption { return func(t *Target) { t.c.idle = d } }

// WithSingleCopyLimit overrides the 5 GiB above which Copy switches from CopyObject to UploadPartCopy.
func WithSingleCopyLimit(n int64) TargetOption { return func(t *Target) { t.copyMax = n } }

// targetStorage maps backup.target to the object-store section the shared client code reads. The transport, retry and
// checksum settings are the defaults of storage.s3 (the target has no keys for them); nothing is taken from the live
// storage.s3 section.
func targetStorage(b config.Backup) config.StorageS3 {
	t := b.Target
	return config.StorageS3{
		Endpoint: t.Endpoint, Region: t.Region, Bucket: t.Bucket, Prefix: t.Prefix, ForcePathStyle: t.ForcePathStyle,
		AllowInsecureEndpoint: t.AllowInsecureEndpoint, CAFile: t.CAFile, StorageClass: t.StorageClass,
		PayloadChecksum: "none",
		Checksums:       config.StorageS3Checksums{RequestCalculation: "when_supported", ResponseValidation: "when_supported"},
		Credentials:     t.Credentials,
		SSE:             config.StorageS3SSE{Mode: t.SSE.Mode, KMSKeyID: t.SSE.KMSKeyID, BucketKey: t.SSE.BucketKey},
		HTTP: config.StorageS3HTTP{
			DialTimeout: 5 * time.Second, ResponseHeaderTimeout: 30 * time.Second, IdleTransferTimeout: time.Minute,
			ReceiveWindow: 256 * config.KiB,
		},
		Retry: config.StorageS3Retry{MaxAttempts: 5, MaxBackoff: 20 * time.Second},
	}
}

// NewTarget builds the backup target from backup.target through the outbound factory, so that backup.target.ca_file is its
// own trust store. It is how cmd/trove gets a target.Store from the configuration. The server-side copy requests go through a
// second HTTP client without a response-header timeout (see Client.copyAPI).
func NewTarget(ctx context.Context, cfg config.Backup, f *outbound.Factory, opts ...TargetOption) (*Target, error) {
	s := targetStorage(cfg)
	if s.Bucket == "" {
		return nil, errors.New("backup.target.bucket is required")
	}
	c, err := NewFromFactory(ctx, s, f)
	if err != nil {
		return nil, fmt.Errorf("backup.target: %w", err)
	}
	return newTarget(c, cfg, opts), nil
}

// NewTargetWithClient is NewTarget for a caller that has its HTTP client from internal/outbound already. A copy request is
// then subject to that client's response-header timeout.
func NewTargetWithClient(ctx context.Context, cfg config.Backup, hc *http.Client, opts ...TargetOption) (*Target, error) {
	s := targetStorage(cfg)
	if s.Bucket == "" {
		return nil, errors.New("backup.target.bucket is required")
	}
	c, err := New(ctx, s, hc)
	if err != nil {
		return nil, fmt.Errorf("backup.target: %w", err)
	}
	return newTarget(c, cfg, opts), nil
}

func newTarget(c *Client, cfg config.Backup, opts []TargetOption) *Target {
	t := newCopier(c, int64(cfg.Copy.PartSize))
	t.sseMode, t.kmsKeyID, t.partSize = cfg.Target.SSE.Mode, cfg.Target.SSE.KMSKeyID, int64(cfg.Database.PartSize)
	for _, o := range opts {
		o(t)
	}
	if t.partSize <= 0 {
		t.partSize = 16 << 20
	}
	return t
}

// newCopier is a Target that only copies server-side: the part of the adapter the restore's BlobSink shares.
func newCopier(c *Client, copyPart int64) *Target {
	if copyPart <= 0 {
		copyPart = 256 << 20
	}
	return &Target{
		c: c, copyPart: copyPart, copyMax: maxCopySingle, copyBase: defaultCopyBase, copyRate: defaultCopyRate,
		progressEvery: defaultProgressEvery, abortGrace: defaultAbortGrace,
	}
}

// Endpoint is the normalized endpoint of the target, "" for AWS resolution. A copy source on another endpoint cannot be
// copied server-side.
func (t *Target) Endpoint() string { return t.c.cfg.Endpoint }

// Bucket is the target bucket.
func (t *Target) Bucket() string { return t.c.bucket }

// CheckSSE compares what the store reported for an object with backup.target.sse (R-BAK-026) and returns an error naming
// backup.target.sse.mode on a mismatch. With mode none anything is accepted.
func (t *Target) CheckSSE(o target.Object) error {
	switch t.sseMode {
	case "s3":
		if o.SSE != "AES256" {
			return fmt.Errorf("the target reports server-side encryption %q for %s but backup.target.sse.mode is s3: the store ignores SSE-S3, change backup.target.sse.mode", o.SSE, o.Key)
		}
	case "kms":
		if o.SSE != "aws:kms" {
			return fmt.Errorf("the target reports server-side encryption %q for %s but backup.target.sse.mode is kms", o.SSE, o.Key)
		}
		if t.kmsKeyID != "" && o.KMSKeyID != "" && o.KMSKeyID != t.kmsKeyID && !strings.HasSuffix(o.KMSKeyID, "/"+t.kmsKeyID) {
			return fmt.Errorf("the target reports KMS key %q for %s but backup.target.sse.kms_key_id is %q", o.KMSKeyID, o.Key, t.kmsKeyID)
		}
	}
	return nil
}

// terr maps the classified errors of the client to the port's.
func terr(err error) error {
	if errors.Is(err, blob.ErrNotFound) {
		return target.ErrNotFound
	}
	return err
}

func apiCode(err error) (code string, status int) {
	var se *blob.StoreError
	if errors.As(err, &se) {
		return se.Code, se.Status
	}
	return "", 0
}

// objectOf converts the fields every response carries.
func objectOf(key string, size int64, etag *string, modified *time.Time, sse types.ServerSideEncryption, kms, sum *string, ct types.ChecksumType) target.Object {
	o := target.Object{
		Key: key, Size: size, ETag: strings.Trim(aws.ToString(etag), `"`), LastModified: aws.ToTime(modified),
		SSE: string(sse), KMSKeyID: aws.ToString(kms),
	}
	// A composite checksum (multipart) is a digest of digests, not of the object.
	if sum != nil && ct != types.ChecksumTypeComposite {
		if raw, err := base64.StdEncoding.DecodeString(*sum); err == nil && len(raw) == sha256.Size {
			o.SHA256 = hex.EncodeToString(raw)
		}
	}
	return o
}

// Head implements target.Store.
func (t *Target) Head(ctx context.Context, key string) (target.Object, error) {
	out, err := t.c.api.HeadObject(ctx, &awss3.HeadObjectInput{Bucket: &t.c.bucket, Key: &key, ChecksumMode: types.ChecksumModeEnabled})
	if err != nil {
		return target.Object{}, terr(mapError(ctx, "HeadObject", key, err))
	}
	return objectOf(key, sizeOf(out.ContentLength), out.ETag, out.LastModified, out.ServerSideEncryption, out.SSEKMSKeyId, out.ChecksumSHA256, out.ChecksumType), nil
}

// Get implements target.Store.
func (t *Target) Get(ctx context.Context, key string) (io.ReadCloser, target.Object, error) {
	rctx, cancel := context.WithCancel(ctx)
	out, err := t.c.api.GetObject(rctx, &awss3.GetObjectInput{Bucket: &t.c.bucket, Key: &key, ChecksumMode: types.ChecksumModeEnabled})
	if err != nil {
		cancel()
		return nil, target.Object{}, terr(mapError(ctx, "GetObject", key, err))
	}
	o := objectOf(key, sizeOf(out.ContentLength), out.ETag, out.LastModified, out.ServerSideEncryption, out.SSEKMSKeyId, out.ChecksumSHA256, out.ChecksumType)
	w := newIdle(t.c.idle, cancel)
	return &body{ReadCloser: out.Body, w: w, cancel: cancel, op: "GetObject", key: key}, o, nil
}

// Put implements target.Store.
func (t *Target) Put(ctx context.Context, key string, data []byte, opts target.PutOptions) (target.Object, error) {
	if opts.IfAbsent && t.noCond.Load() {
		if err := t.mustBeAbsent(ctx, key); err != nil {
			return target.Object{}, err
		}
	}
	ctx, w, done := t.c.watch(ctx)
	defer done()
	w.touch()
	sum := sha256.Sum256(data)
	s := t.c.sse()
	in := &awss3.PutObjectInput{
		Bucket: &t.c.bucket, Key: &key, Body: &seeker{rs: bytes.NewReader(data), w: w}, ContentLength: aws.Int64(int64(len(data))),
		ContentType: aws.String(contentType(opts)), StorageClass: types.StorageClass(t.c.cfg.StorageClass),
		ServerSideEncryption: s.mode, SSEKMSKeyId: s.key, BucketKeyEnabled: s.bucket,
		ChecksumSHA256: aws.String(base64.StdEncoding.EncodeToString(sum[:])),
	}
	if opts.IfAbsent && !t.noCond.Load() {
		in.IfNoneMatch = aws.String("*")
	}
	out, err := t.c.api.PutObject(ctx, in)
	if serr := w.stalled("PutObject", key); serr != nil {
		return target.Object{}, serr
	}
	if err != nil {
		if opts.IfAbsent {
			if cerr := t.conditionalError(ctx, key, err); cerr != nil {
				if errors.Is(cerr, errRetryUnconditional) {
					return t.Put(ctx, key, data, opts)
				}
				return target.Object{}, cerr
			}
		}
		return target.Object{}, terr(mapError(ctx, "PutObject", key, err))
	}
	return target.Object{
		Key: key, Size: int64(len(data)), ETag: strings.Trim(aws.ToString(out.ETag), `"`), LastModified: time.Now(),
		SSE: string(out.ServerSideEncryption), KMSKeyID: aws.ToString(out.SSEKMSKeyId), SHA256: hex.EncodeToString(sum[:]),
	}, nil
}

func contentType(o target.PutOptions) string {
	if o.ContentType != "" {
		return o.ContentType
	}
	return "application/octet-stream"
}

var errRetryUnconditional = errors.New("retry without the precondition header")

// conditionalError classifies the failure of a write that carried If-None-Match: 412 is an existing object; a store that
// does not know the header (501, 400 InvalidArgument/NotImplemented) is remembered, and the write is repeated behind a HEAD
// check. It returns nil when the error is none of these.
func (t *Target) conditionalError(ctx context.Context, key string, err error) error {
	code, status := apiCode(mapError(ctx, "PutObject", key, err))
	switch {
	case status == http.StatusPreconditionFailed || code == "PreconditionFailed":
		return target.ErrExists
	case status == http.StatusNotImplemented || code == "NotImplemented" || code == "InvalidArgument" && status == http.StatusBadRequest:
		t.noCond.Store(true)
		if herr := t.mustBeAbsent(ctx, key); herr != nil {
			return herr
		}
		return errRetryUnconditional
	}
	return nil
}

// mustBeAbsent is the fallback of a conditional create on a store without conditional writes: a HEAD before the write. It is
// not atomic; the lane never needs the atomic form for correctness, because the one-run rule is held in PostgreSQL
// (R-BAK-010) and every key below the target is immutable (R-BAK-002).
func (t *Target) mustBeAbsent(ctx context.Context, key string) error {
	_, err := t.Head(ctx, key)
	switch {
	case err == nil:
		return target.ErrExists
	case errors.Is(err, target.ErrNotFound):
		return nil
	}
	return err
}

// List implements target.Store.
func (t *Target) List(ctx context.Context, prefix string, fn func(target.Object) error) error {
	var token *string
	for {
		out, err := t.c.api.ListObjectsV2(ctx, &awss3.ListObjectsV2Input{Bucket: &t.c.bucket, Prefix: &prefix, ContinuationToken: token})
		if err != nil {
			return terr(mapError(ctx, "ListObjectsV2", prefix, err))
		}
		for _, o := range out.Contents {
			if err := fn(target.Object{Key: aws.ToString(o.Key), Size: aws.ToInt64(o.Size), ETag: strings.Trim(aws.ToString(o.ETag), `"`), LastModified: aws.ToTime(o.LastModified)}); err != nil {
				return err
			}
		}
		if !aws.ToBool(out.IsTruncated) || out.NextContinuationToken == nil {
			return nil
		}
		token = out.NextContinuationToken
	}
}

// Delete implements target.Store. It sends the keys in requests of 1,000; a missing key is no error.
func (t *Target) Delete(ctx context.Context, keys []string) error {
	for len(keys) > 0 {
		n := min(len(keys), deleteBatch)
		ids := make([]types.ObjectIdentifier, n)
		for i := range n {
			ids[i] = types.ObjectIdentifier{Key: &keys[i]}
		}
		out, err := t.c.api.DeleteObjects(ctx, &awss3.DeleteObjectsInput{Bucket: &t.c.bucket, Delete: &types.Delete{Objects: ids, Quiet: aws.Bool(true)}})
		if err != nil {
			return terr(mapError(ctx, "DeleteObjects", "", err))
		}
		for _, e := range out.Errors {
			if aws.ToString(e.Code) != "NoSuchKey" {
				return &blob.StoreError{
					Op: "DeleteObjects", Key: aws.ToString(e.Key), Code: aws.ToString(e.Code), Kind: blob.KindOther,
					Err: errors.New(aws.ToString(e.Message)),
				}
			}
		}
		keys = keys[n:]
	}
	return nil
}

// Probe implements target.Store: it writes a small object, reads it back, reports what HeadObject says and deletes it.
func (t *Target) Probe(ctx context.Context, key string) (target.Object, error) {
	want := []byte("trove backup target probe")
	if _, err := t.Put(ctx, key, want, target.PutOptions{ContentType: "text/plain"}); err != nil {
		var se *blob.StoreError
		if t.sseMode != "none" && errors.As(err, &se) && se.Status >= http.StatusBadRequest {
			// A store that rejects the encryption headers answers the write with an error (R-BAK-026).
			return target.Object{}, fmt.Errorf("write %s with server-side encryption %q: %w: if the store has no server-side encryption, set backup.target.sse.mode to none", key, t.sseMode, err)
		}
		return target.Object{}, fmt.Errorf("write %s: %w", key, err)
	}
	o, err := t.Head(ctx, key)
	if err == nil {
		var rc io.ReadCloser
		if rc, _, err = t.Get(ctx, key); err == nil {
			var got []byte
			got, err = io.ReadAll(rc)
			_ = rc.Close()
			if err == nil && !bytes.Equal(got, want) {
				err = errors.New("the probe object reads back with other content")
			}
		}
	}
	if derr := t.Delete(ctx, []string{key}); err == nil && derr != nil {
		err = fmt.Errorf("delete %s: %w", key, derr)
	}
	if err != nil {
		return target.Object{}, err
	}
	return o, nil
}
