// Package blobtest is an in-memory blob.ObjectStore with fault injection, for
// the unit tests of the packages that use the blob store. It follows the
// behaviour the conformance check asks of a real store (strong consistency,
// atomic keys, multipart objects visible on completion only) and the rules of
// S3 that matter to trove: parts below 5 MiB except the last are refused, and a
// payload checksum that does not match fails the request.
package blobtest

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // S3 ETags and Content-MD5
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/guided-traffic/trove/internal/blob"
)

type object struct {
	data     []byte
	modified time.Time
}

type upload struct {
	key       string
	initiated time.Time
	parts     map[int][]byte
}

// Store is the in-memory object store. The zero value is not usable: use New.
type Store struct {
	// MinPartSize is the smallest part but the last; New sets 5 MiB, a test may
	// lower it to keep its data small.
	MinPartSize int
	// Now is the clock of LastModified and upload initiation.
	Now func() time.Time

	mu      sync.Mutex
	objects map[string]*object
	uploads map[string]*upload
	nextID  int
	fault   func(op, key string) error
	getBody func(io.ReadCloser) io.ReadCloser
	counts  map[string]int
}

var _ blob.ObjectStore = (*Store)(nil)

// New returns an empty store.
func New() *Store {
	return &Store{MinPartSize: 5 << 20, Now: time.Now, objects: map[string]*object{}, uploads: map[string]*upload{}, counts: map[string]int{}}
}

// SetFault installs a function called before every operation with its name
// ("put", "create_multipart", "upload_part", "complete_multipart",
// "abort_multipart", "head", "get", "delete", "delete_many", "list",
// "list_multipart") and key; a non-nil error is returned instead of the
// operation. Pass nil to remove it.
func (s *Store) SetFault(f func(op, key string) error) {
	s.mu.Lock()
	s.fault = f
	s.mu.Unlock()
}

// SetGetBody wraps the body of every Get, to inject a failure in the middle of
// a download. Pass nil to remove it.
func (s *Store) SetGetBody(f func(io.ReadCloser) io.ReadCloser) {
	s.mu.Lock()
	s.getBody = f
	s.mu.Unlock()
}

// Count returns how many times an operation ran (faulted calls included).
func (s *Store) Count(op string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.counts[op]
}

// Keys lists the object keys with a prefix, sorted.
func (s *Store) Keys(prefix string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for k := range s.objects {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// Data returns a copy of an object's bytes.
func (s *Store) Data(key string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.objects[key]
	if !ok {
		return nil, false
	}
	return bytes.Clone(o.data), true
}

// Set writes an object directly, without counting: tests plant orphans and
// corrupt content with it.
func (s *Store) Set(key string, data []byte, modified time.Time) {
	s.mu.Lock()
	s.objects[key] = &object{data: bytes.Clone(data), modified: modified}
	s.mu.Unlock()
}

// OpenUploads returns the number of unfinished multipart uploads.
func (s *Store) OpenUploads() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.uploads)
}

func (s *Store) enter(op, key string) error {
	s.mu.Lock()
	s.counts[op]++
	f := s.fault
	s.mu.Unlock()
	if f != nil {
		return f(op, key)
	}
	return nil
}

func badDigest(op, key string) error {
	return &blob.StoreError{Op: op, Key: key, Code: "BadDigest", Status: 400, Kind: blob.KindOther}
}

func checkSum(op, key string, data []byte, sum blob.Checksum) error {
	switch sum.Algorithm {
	case "sha256":
		h := sha256.Sum256(data)
		if !bytes.Equal(h[:], sum.Value) {
			return badDigest(op, key)
		}
	case "md5":
		h := md5.Sum(data) //nolint:gosec // see imports
		if !bytes.Equal(h[:], sum.Value) {
			return badDigest(op, key)
		}
	}
	return nil
}

func etag(data []byte) string {
	h := md5.Sum(data) //nolint:gosec // see imports
	return `"` + hex.EncodeToString(h[:]) + `"`
}

// Put implements blob.ObjectStore.
func (s *Store) Put(_ context.Context, key string, body io.ReadSeeker, size int64, sum blob.Checksum) error {
	if err := s.enter("put", key); err != nil {
		return err
	}
	data, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	if int64(len(data)) != size {
		return fmt.Errorf("body has %d bytes, declared %d", len(data), size)
	}
	if err := checkSum("PutObject", key, data, sum); err != nil {
		return err
	}
	s.mu.Lock()
	s.objects[key] = &object{data: data, modified: s.Now()}
	s.mu.Unlock()
	return nil
}

// CreateMultipart implements blob.ObjectStore.
func (s *Store) CreateMultipart(_ context.Context, key, _ string) (string, error) {
	if err := s.enter("create_multipart", key); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	id := "upload-" + strconv.Itoa(s.nextID)
	s.uploads[id] = &upload{key: key, initiated: s.Now(), parts: map[int][]byte{}}
	return id, nil
}

// UploadPart implements blob.ObjectStore.
func (s *Store) UploadPart(_ context.Context, key, id string, number int, body io.ReadSeeker, size int64, sum blob.Checksum) (blob.Part, error) {
	if err := s.enter("upload_part", key); err != nil {
		return blob.Part{}, err
	}
	data, err := io.ReadAll(body)
	if err != nil {
		return blob.Part{}, err
	}
	if int64(len(data)) != size {
		return blob.Part{}, fmt.Errorf("part has %d bytes, declared %d", len(data), size)
	}
	if err := checkSum("UploadPart", key, data, sum); err != nil {
		return blob.Part{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.uploads[id]
	if !ok || u.key != key {
		return blob.Part{}, &blob.StoreError{Op: "UploadPart", Key: key, Code: "NoSuchUpload", Status: 404, Kind: blob.KindOther}
	}
	u.parts[number] = data
	return blob.Part{Number: number, ETag: etag(data), Size: size}, nil
}

// CompleteMultipart implements blob.ObjectStore.
func (s *Store) CompleteMultipart(_ context.Context, key, id string, parts []blob.Part) error {
	if err := s.enter("complete_multipart", key); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.uploads[id]
	if !ok || u.key != key {
		return &blob.StoreError{Op: "CompleteMultipartUpload", Key: key, Code: "NoSuchUpload", Status: 404, Kind: blob.KindOther}
	}
	var all []byte
	for i, p := range parts {
		if i > 0 && p.Number <= parts[i-1].Number {
			return &blob.StoreError{Op: "CompleteMultipartUpload", Key: key, Code: "InvalidPartOrder", Status: 400, Kind: blob.KindOther}
		}
		data, ok := u.parts[p.Number]
		if !ok || etag(data) != p.ETag {
			return &blob.StoreError{Op: "CompleteMultipartUpload", Key: key, Code: "InvalidPart", Status: 400, Kind: blob.KindOther}
		}
		if i < len(parts)-1 && len(data) < s.MinPartSize {
			return &blob.StoreError{Op: "CompleteMultipartUpload", Key: key, Code: "EntityTooSmall", Status: 400, Kind: blob.KindOther}
		}
		all = append(all, data...)
	}
	delete(s.uploads, id)
	s.objects[key] = &object{data: all, modified: s.Now()}
	return nil
}

// AbortMultipart implements blob.ObjectStore.
func (s *Store) AbortMultipart(_ context.Context, key, id string) error {
	if err := s.enter("abort_multipart", key); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.uploads, id)
	return nil
}

// Head implements blob.ObjectStore.
func (s *Store) Head(_ context.Context, key string) (blob.ObjectInfo, error) {
	if err := s.enter("head", key); err != nil {
		return blob.ObjectInfo{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.objects[key]
	if !ok {
		return blob.ObjectInfo{}, blob.ErrNotFound
	}
	return blob.ObjectInfo{Key: key, Size: int64(len(o.data)), ETag: etag(o.data), LastModified: o.modified}, nil
}

// Get implements blob.ObjectStore.
func (s *Store) Get(_ context.Context, key string, rng *blob.ByteRange) (io.ReadCloser, blob.ObjectInfo, error) {
	if err := s.enter("get", key); err != nil {
		return nil, blob.ObjectInfo{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.objects[key]
	if !ok {
		return nil, blob.ObjectInfo{}, blob.ErrNotFound
	}
	info := blob.ObjectInfo{Key: key, Size: int64(len(o.data)), ETag: etag(o.data), LastModified: o.modified}
	data := o.data
	if rng != nil {
		if rng.Offset >= int64(len(data)) {
			return nil, info, &blob.StoreError{Op: "GetObject", Key: key, Code: "InvalidRange", Status: 416, Kind: blob.KindOther}
		}
		end := int64(len(data))
		if rng.Length >= 0 {
			end = min(end, rng.Offset+rng.Length)
		}
		data = data[rng.Offset:end]
	}
	body := io.NopCloser(bytes.NewReader(bytes.Clone(data)))
	if s.getBody != nil {
		return s.getBody(body), info, nil
	}
	return body, info, nil
}

// Delete implements blob.ObjectStore.
func (s *Store) Delete(_ context.Context, key string) error {
	if err := s.enter("delete", key); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.objects, key)
	s.mu.Unlock()
	return nil
}

// DeleteMany implements blob.ObjectStore.
func (s *Store) DeleteMany(_ context.Context, keys []string) ([]blob.DeleteFailure, error) {
	if err := s.enter("delete_many", ""); err != nil {
		return nil, err
	}
	if len(keys) > 1000 {
		return nil, &blob.StoreError{Op: "DeleteObjects", Code: "MalformedXML", Status: 400, Kind: blob.KindOther}
	}
	s.mu.Lock()
	for _, k := range keys {
		delete(s.objects, k)
	}
	s.mu.Unlock()
	return nil, nil
}

// List implements blob.ObjectStore.
func (s *Store) List(_ context.Context, prefix string, opts blob.ListOptions) (blob.ListPage, error) {
	if err := s.enter("list", prefix); err != nil {
		return blob.ListPage{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var keys []string
	for k := range s.objects {
		if strings.HasPrefix(k, prefix) && k > opts.StartAfter && k > opts.Token {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	limit := opts.Max
	if limit <= 0 {
		limit = 1000
	}
	var page blob.ListPage
	seen := map[string]bool{}
	for i, k := range keys {
		if len(page.Objects)+len(page.Prefixes) >= limit {
			page.Next = keys[i-1]
			break
		}
		if opts.Delimiter != "" {
			if j := strings.Index(k[len(prefix):], opts.Delimiter); j >= 0 {
				p := k[:len(prefix)+j+len(opts.Delimiter)]
				if !seen[p] {
					seen[p] = true
					page.Prefixes = append(page.Prefixes, p)
				}
				continue
			}
		}
		o := s.objects[k]
		page.Objects = append(page.Objects, blob.ObjectInfo{Key: k, Size: int64(len(o.data)), ETag: etag(o.data), LastModified: o.modified})
	}
	return page, nil
}

// ListMultipart implements blob.ObjectStore.
func (s *Store) ListMultipart(_ context.Context, prefix string) ([]blob.MultipartUpload, error) {
	if err := s.enter("list_multipart", prefix); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []blob.MultipartUpload
	for id, u := range s.uploads {
		if strings.HasPrefix(u.key, prefix) {
			out = append(out, blob.MultipartUpload{Key: u.key, UploadID: id, Initiated: u.initiated})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UploadID < out[j].UploadID })
	return out, nil
}

// PresignHost is the host of the URLs Presign returns. The store is also an
// http.RoundTripper that answers GET requests for them, so a test can give a
// client this transport.
const PresignHost = "mem.invalid"

// Presign implements blob.ObjectStore.
func (s *Store) Presign(_ context.Context, key string, _ time.Duration) (string, error) {
	return "http://" + PresignHost + "/" + key, nil
}

// RoundTrip serves a presigned URL from memory.
func (s *Store) RoundTrip(req *http.Request) (*http.Response, error) {
	key := strings.TrimPrefix(req.URL.Path, "/")
	s.mu.Lock()
	o, ok := s.objects[key]
	s.mu.Unlock()
	rec := &http.Response{Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1, Header: http.Header{}, Request: req}
	if !ok {
		rec.StatusCode, rec.Status, rec.Body = 404, "404 Not Found", io.NopCloser(strings.NewReader(""))
		return rec, nil
	}
	rec.StatusCode, rec.Status, rec.Body = 200, "200 OK", io.NopCloser(bytes.NewReader(bytes.Clone(o.data)))
	rec.ContentLength = int64(len(o.data))
	return rec, nil
}
