package s3

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/outbound"
)

// Client is the blob.ObjectStore of one bucket.
type Client struct {
	api     *awss3.Client
	presign *awss3.PresignClient
	http    *http.Client
	bucket  string
	cfg     config.StorageS3
	idle    time.Duration
}

var _ blob.ObjectStore = (*Client)(nil)

// NewFromFactory builds a client whose HTTP goes through the outbound factory
// with the purpose objectstore and the timeouts of storage.s3.http.
//
// storage.s3.ca_file, when set, is the object store's whole trust store: the
// client verifies the endpoint against that bundle only, not against the system
// roots or proxy.outbound.tls.ca_files, and the factory re-reads the file
// without a restart (R-ARCH-035, R-STOR-030). A bundle that does not load at
// start is a configuration error.
//
// Three settings of storage.s3.http need options the factory does not have yet
// (max_connections, tls_handshake_timeout, idle_conn_timeout use its defaults),
// and storage.s3.http.proxy_credentials_file needs a credential the proxy hook
// can re-read; a proxy_credentials_file is refused rather than ignored: until
// the outbound layer takes a proxy credential, use a proxy that authenticates by
// address.
func NewFromFactory(ctx context.Context, cfg config.StorageS3, f *outbound.Factory) (*Client, error) {
	var opts []outbound.ClientOption
	if cfg.CAFile != "" {
		trust, err := f.TrustStore(outbound.PurposeObjectStore, cfg.CAFile)
		if err != nil {
			return nil, fmt.Errorf("storage.s3.ca_file: %w", err)
		}
		opts = append(opts, outbound.WithRootCAs(trust))
	}
	if cfg.HTTP.ProxyCredentialsFile != "" {
		return nil, errors.New("storage.s3.http.proxy_credentials_file is not supported yet: the outbound layer cannot send a proxy credential read from a file; use a proxy that authenticates by source address")
	}
	opts = append(opts,
		outbound.WithConnectTimeout(cfg.HTTP.DialTimeout),
		outbound.WithResponseHeaderTimeout(cfg.HTTP.ResponseHeaderTimeout),
	)
	if cfg.HTTP.ProxyURL != "" {
		u, err := url.Parse(cfg.HTTP.ProxyURL)
		if err != nil {
			return nil, fmt.Errorf("storage.s3.http.proxy_url: %w", err)
		}
		var bypass []string
		for _, h := range strings.Split(cfg.HTTP.NoProxy, ",") {
			if h = strings.TrimSpace(h); h != "" {
				bypass = append(bypass, h)
			}
		}
		opts = append(opts, outbound.WithProxyURL(u, bypass...))
	}
	return New(ctx, cfg, f.Client(outbound.PurposeObjectStore, opts...))
}

// New builds a client that sends its requests with hc, which must come from
// internal/outbound.
func New(ctx context.Context, cfg config.StorageS3, hc *http.Client) (*Client, error) {
	if cfg.Bucket == "" {
		return nil, errors.New("storage.s3.bucket is required")
	}
	// An endpoint without a scheme is a host and means https:// (R-STOR-030).
	cfg.Endpoint, cfg.Presign.Endpoint = config.NormalizeEndpoint(cfg.Endpoint), config.NormalizeEndpoint(cfg.Presign.Endpoint)
	if err := checkEndpoint(cfg); err != nil {
		return nil, err
	}
	awsCfg, err := awsConfig(ctx, cfg, hc)
	if err != nil {
		return nil, err
	}
	mk := func(endpoint string) *awss3.Client {
		return awss3.NewFromConfig(awsCfg, func(o *awss3.Options) {
			o.UsePathStyle = cfg.ForcePathStyle
			if endpoint != "" {
				o.BaseEndpoint = aws.String(endpoint)
			}
		})
	}
	c := &Client{api: mk(cfg.Endpoint), http: hc, bucket: cfg.Bucket, cfg: cfg, idle: cfg.HTTP.IdleTransferTimeout}
	pe := cfg.Presign.Endpoint
	if pe == "" {
		c.presign = awss3.NewPresignClient(c.api)
	} else {
		c.presign = awss3.NewPresignClient(mk(pe))
	}
	return c, nil
}

// HTTPClient returns the HTTP client the object store is reached with: the same
// trust store (storage.s3.ca_file or the factory's), proxy and timeouts. A caller
// that fetches a presigned URL itself, such as the conformance check of
// R-STOR-035, uses it, so the fetch verifies the store the way the client does.
func (c *Client) HTTPClient() *http.Client { return c.http }

// checkEndpoint applies R-STOR-030: an absolute https URL, http only when
// allowed.
func checkEndpoint(cfg config.StorageS3) error {
	if cfg.Endpoint == "" {
		return nil
	}
	u, err := url.Parse(cfg.Endpoint)
	if err != nil || u.Host == "" {
		return fmt.Errorf("storage.s3.endpoint %q is not an absolute URL", cfg.Endpoint)
	}
	if u.Scheme == "http" && !cfg.AllowInsecureEndpoint {
		return errors.New("storage.s3.endpoint uses http: set storage.s3.allow_insecure_endpoint to allow it")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("storage.s3.endpoint %q must be an https URL", cfg.Endpoint)
	}
	return nil
}

func retryer(cfg config.StorageS3Retry) func() aws.Retryer {
	return func() aws.Retryer {
		return retry.NewStandard(func(o *retry.StandardOptions) {
			o.MaxAttempts = cfg.MaxAttempts
			o.MaxBackoff = cfg.MaxBackoff
		})
	}
}

func b64(b []byte) *string { return aws.String(base64.StdEncoding.EncodeToString(b)) }

// sse applies storage.s3.sse to a write.
type sseFields struct {
	mode   types.ServerSideEncryption
	key    *string
	bucket *bool
}

func (c *Client) sse() sseFields {
	switch c.cfg.SSE.Mode {
	case "s3":
		return sseFields{mode: types.ServerSideEncryptionAes256}
	case "kms":
		return sseFields{mode: types.ServerSideEncryptionAwsKms, key: aws.String(c.cfg.SSE.KMSKeyID), bucket: aws.Bool(c.cfg.SSE.BucketKey)}
	}
	return sseFields{}
}

// call runs an upload under the stall detector.
func (c *Client) watch(ctx context.Context) (context.Context, *idle, context.CancelFunc) {
	ctx, cancel := context.WithCancel(ctx)
	w := newIdle(c.idle, cancel)
	return ctx, w, func() { w.stop(); cancel() }
}

// Put implements blob.ObjectStore.
func (c *Client) Put(ctx context.Context, key string, rs io.ReadSeeker, size int64, sum blob.Checksum) error {
	ctx, w, done := c.watch(ctx)
	defer done()
	w.touch()
	s := c.sse()
	in := &awss3.PutObjectInput{
		Bucket: &c.bucket, Key: &key, Body: &seeker{rs: rs, w: w}, ContentLength: aws.Int64(size),
		ContentType: aws.String("application/octet-stream"), StorageClass: types.StorageClass(c.cfg.StorageClass),
		ServerSideEncryption: s.mode, SSEKMSKeyId: s.key, BucketKeyEnabled: s.bucket,
	}
	switch sum.Algorithm {
	case "sha256":
		in.ChecksumSHA256 = b64(sum.Value)
	case "md5":
		in.ContentMD5 = b64(sum.Value)
	}
	_, err := c.api.PutObject(ctx, in)
	if serr := w.stalled("PutObject", key); serr != nil {
		return serr
	}
	return mapError(ctx, "PutObject", key, err)
}

// CreateMultipart implements blob.ObjectStore.
func (c *Client) CreateMultipart(ctx context.Context, key, algorithm string) (string, error) {
	s := c.sse()
	in := &awss3.CreateMultipartUploadInput{
		Bucket: &c.bucket, Key: &key, ContentType: aws.String("application/octet-stream"),
		StorageClass:         types.StorageClass(c.cfg.StorageClass),
		ServerSideEncryption: s.mode, SSEKMSKeyId: s.key, BucketKeyEnabled: s.bucket,
	}
	if algorithm == "sha256" {
		in.ChecksumAlgorithm = types.ChecksumAlgorithmSha256
	}
	out, err := c.api.CreateMultipartUpload(ctx, in)
	if err != nil {
		return "", mapError(ctx, "CreateMultipartUpload", key, err)
	}
	return aws.ToString(out.UploadId), nil
}

// UploadPart implements blob.ObjectStore.
func (c *Client) UploadPart(ctx context.Context, key, uploadID string, number int, rs io.ReadSeeker, size int64, sum blob.Checksum) (blob.Part, error) {
	ctx, w, done := c.watch(ctx)
	defer done()
	w.touch()
	in := &awss3.UploadPartInput{
		Bucket: &c.bucket, Key: &key, UploadId: &uploadID, PartNumber: aws.Int32(int32(number)), //nolint:gosec // at most 10,000
		Body: &seeker{rs: rs, w: w}, ContentLength: aws.Int64(size),
	}
	switch sum.Algorithm {
	case "sha256":
		in.ChecksumSHA256 = b64(sum.Value)
		in.ChecksumAlgorithm = types.ChecksumAlgorithmSha256
	case "md5":
		in.ContentMD5 = b64(sum.Value)
	}
	out, err := c.api.UploadPart(ctx, in)
	if serr := w.stalled("UploadPart", key); serr != nil {
		return blob.Part{}, serr
	}
	if err != nil {
		return blob.Part{}, mapError(ctx, "UploadPart", key, err)
	}
	return blob.Part{Number: number, ETag: aws.ToString(out.ETag), Size: size, Checksum: sum}, nil
}

// CompleteMultipart implements blob.ObjectStore.
func (c *Client) CompleteMultipart(ctx context.Context, key, uploadID string, parts []blob.Part) error {
	cp := make([]types.CompletedPart, len(parts))
	for i, p := range parts {
		cp[i] = types.CompletedPart{ETag: aws.String(p.ETag), PartNumber: aws.Int32(int32(p.Number))} //nolint:gosec // at most 10,000
		if p.Checksum.Algorithm == "sha256" {
			cp[i].ChecksumSHA256 = b64(p.Checksum.Value)
		}
	}
	_, err := c.api.CompleteMultipartUpload(ctx, &awss3.CompleteMultipartUploadInput{
		Bucket: &c.bucket, Key: &key, UploadId: &uploadID,
		MultipartUpload: &types.CompletedMultipartUpload{Parts: cp},
	})
	return mapError(ctx, "CompleteMultipartUpload", key, err)
}

// AbortMultipart implements blob.ObjectStore.
func (c *Client) AbortMultipart(ctx context.Context, key, uploadID string) error {
	_, err := c.api.AbortMultipartUpload(ctx, &awss3.AbortMultipartUploadInput{Bucket: &c.bucket, Key: &key, UploadId: &uploadID})
	var se *blob.StoreError
	if err = mapError(ctx, "AbortMultipartUpload", key, err); errors.As(err, &se) && se.Code == "NoSuchUpload" {
		return nil // already gone
	}
	return err
}

// sizeOf is the size a response announced; a response without Content-Length
// has an unknown size (-1), not an empty object.
func sizeOf(contentLength *int64) int64 {
	if contentLength == nil {
		return -1
	}
	return *contentLength
}

// Head implements blob.ObjectStore.
func (c *Client) Head(ctx context.Context, key string) (blob.ObjectInfo, error) {
	out, err := c.api.HeadObject(ctx, &awss3.HeadObjectInput{Bucket: &c.bucket, Key: &key})
	if err != nil {
		return blob.ObjectInfo{}, mapError(ctx, "HeadObject", key, err)
	}
	return blob.ObjectInfo{Key: key, Size: sizeOf(out.ContentLength), ETag: aws.ToString(out.ETag), LastModified: aws.ToTime(out.LastModified)}, nil
}

// Get implements blob.ObjectStore.
func (c *Client) Get(ctx context.Context, key string, rng *blob.ByteRange) (io.ReadCloser, blob.ObjectInfo, error) {
	rctx, cancel := context.WithCancel(ctx)
	in := &awss3.GetObjectInput{Bucket: &c.bucket, Key: &key}
	if rng != nil {
		if rng.Length < 0 {
			in.Range = aws.String("bytes=" + strconv.FormatInt(rng.Offset, 10) + "-")
		} else {
			in.Range = aws.String("bytes=" + strconv.FormatInt(rng.Offset, 10) + "-" + strconv.FormatInt(rng.Offset+rng.Length-1, 10))
		}
	}
	out, err := c.api.GetObject(rctx, in)
	if err != nil {
		cancel()
		return nil, blob.ObjectInfo{}, mapError(ctx, "GetObject", key, err)
	}
	info := blob.ObjectInfo{Key: key, Size: -1, ETag: aws.ToString(out.ETag), LastModified: aws.ToTime(out.LastModified)}
	if rng == nil {
		info.Size = sizeOf(out.ContentLength)
	} else if cr := aws.ToString(out.ContentRange); cr != "" {
		if i := strings.LastIndexByte(cr, '/'); i >= 0 {
			if n, err := strconv.ParseInt(cr[i+1:], 10, 64); err == nil {
				info.Size = n
			}
		}
	}
	w := newIdle(c.idle, cancel)
	w.touch()
	return &body{ReadCloser: out.Body, w: w, cancel: cancel, op: "GetObject", key: key}, info, nil
}

// Delete implements blob.ObjectStore.
func (c *Client) Delete(ctx context.Context, key string) error {
	_, err := c.api.DeleteObject(ctx, &awss3.DeleteObjectInput{Bucket: &c.bucket, Key: &key})
	if err = mapError(ctx, "DeleteObject", key, err); errors.Is(err, blob.ErrNotFound) {
		return nil
	}
	return err
}

// DeleteMany implements blob.ObjectStore.
func (c *Client) DeleteMany(ctx context.Context, keys []string) ([]blob.DeleteFailure, error) {
	ids := make([]types.ObjectIdentifier, len(keys))
	for i := range keys {
		ids[i] = types.ObjectIdentifier{Key: &keys[i]}
	}
	out, err := c.api.DeleteObjects(ctx, &awss3.DeleteObjectsInput{
		Bucket: &c.bucket, Delete: &types.Delete{Objects: ids, Quiet: aws.Bool(true)},
	})
	if err != nil {
		return nil, mapError(ctx, "DeleteObjects", "", err)
	}
	var failed []blob.DeleteFailure
	for _, e := range out.Errors {
		if aws.ToString(e.Code) == "NoSuchKey" {
			continue
		}
		failed = append(failed, blob.DeleteFailure{Key: aws.ToString(e.Key), Code: aws.ToString(e.Code)})
	}
	return failed, nil
}

// List implements blob.ObjectStore.
func (c *Client) List(ctx context.Context, prefix string, opts blob.ListOptions) (blob.ListPage, error) {
	in := &awss3.ListObjectsV2Input{Bucket: &c.bucket, Prefix: &prefix}
	if opts.Delimiter != "" {
		in.Delimiter = &opts.Delimiter
	}
	if opts.StartAfter != "" {
		in.StartAfter = &opts.StartAfter
	}
	if opts.Token != "" {
		in.ContinuationToken = &opts.Token
	}
	if opts.Max > 0 {
		in.MaxKeys = aws.Int32(int32(opts.Max)) //nolint:gosec // bounded by the caller
	}
	out, err := c.api.ListObjectsV2(ctx, in)
	if err != nil {
		return blob.ListPage{}, mapError(ctx, "ListObjectsV2", prefix, err)
	}
	page := blob.ListPage{Next: aws.ToString(out.NextContinuationToken)}
	for _, o := range out.Contents {
		page.Objects = append(page.Objects, blob.ObjectInfo{Key: aws.ToString(o.Key), Size: aws.ToInt64(o.Size), ETag: aws.ToString(o.ETag), LastModified: aws.ToTime(o.LastModified)})
	}
	for _, p := range out.CommonPrefixes {
		page.Prefixes = append(page.Prefixes, aws.ToString(p.Prefix))
	}
	if !aws.ToBool(out.IsTruncated) {
		page.Next = ""
	}
	return page, nil
}

// ListMultipart implements blob.ObjectStore.
func (c *Client) ListMultipart(ctx context.Context, prefix string) ([]blob.MultipartUpload, error) {
	var (
		out       []blob.MultipartUpload
		keyMarker *string
		idMarker  *string
	)
	for {
		page, err := c.api.ListMultipartUploads(ctx, &awss3.ListMultipartUploadsInput{
			Bucket: &c.bucket, Prefix: &prefix, KeyMarker: keyMarker, UploadIdMarker: idMarker,
		})
		if err != nil {
			return nil, mapError(ctx, "ListMultipartUploads", prefix, err)
		}
		for _, u := range page.Uploads {
			out = append(out, blob.MultipartUpload{Key: aws.ToString(u.Key), UploadID: aws.ToString(u.UploadId), Initiated: aws.ToTime(u.Initiated)})
		}
		if !aws.ToBool(page.IsTruncated) {
			return out, nil
		}
		keyMarker, idMarker = page.NextKeyMarker, page.NextUploadIdMarker
	}
}

// Presign implements blob.ObjectStore.
func (c *Client) Presign(ctx context.Context, key string, ttl time.Duration) (string, error) {
	r, err := c.presign.PresignGetObject(ctx, &awss3.GetObjectInput{Bucket: &c.bucket, Key: &key}, func(o *awss3.PresignOptions) {
		o.Expires = ttl
	})
	if err != nil {
		return "", mapError(ctx, "PresignGetObject", key, err)
	}
	return r.URL, nil
}
