package s3

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"slices"
	"sort"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/guided-traffic/trove/internal/blob"
)

// maxJournalObject bounds what a read accepts. An entry is a few hundred bytes, an update entry a few kilobytes
// (R-HA-054); a larger object under the journal prefix is not one of ours.
const maxJournalObject = 4 << 20

// JournalStore is the revocation journal's object store (R-HA-054, the port internal/journal.Store): the blob bucket under
// the same credential as the blob store, not the backup target. The writer uses the runtime credential, which is why it
// can be asked to rewrite an object but never to delete one; the reader of a restore may be built with the credentials of
// R-BAK-015 "Journal reader".
//
// It satisfies journal.Store without importing it (an infrastructure package does not import a domain package).
type JournalStore struct {
	c        *Client
	versions bool

	mu     sync.Mutex
	latest map[string]string   // key -> version ID of the newest version that is not a delete marker
	all    map[string][]string // key -> the version IDs that are not delete markers, oldest first
}

// NewJournalStore wraps a client. With versions set, List also returns the keys whose newest version is a delete marker,
// and Get then reads the newest version that is not one, so a delete marker set with the runtime credential cannot hide an
// entry. It needs a credential that may list object versions and read them (s3:ListBucketVersions, s3:GetObjectVersion);
// without versioning on the bucket it behaves as the plain listing.
func NewJournalStore(c *Client, versions bool) *JournalStore {
	return &JournalStore{c: c, versions: versions, latest: map[string]string{}, all: map[string][]string{}}
}

// Put writes an object. The same key with the same bytes is harmless; the payload checksum makes the store verify them.
func (s *JournalStore) Put(ctx context.Context, key string, body []byte) error {
	h := sha256.Sum256(body)
	return s.c.Put(ctx, key, bytes.NewReader(body), int64(len(body)), blob.Checksum{Algorithm: "sha256", Value: h[:]})
}

// List calls fn for every key below prefix in lexicographic order.
func (s *JournalStore) List(ctx context.Context, prefix string, fn func(key string) error) error {
	if s.versions {
		return s.listVersions(ctx, prefix, fn)
	}
	var token string
	for {
		page, err := s.c.List(ctx, prefix, blob.ListOptions{Token: token})
		if err != nil {
			return err
		}
		for _, o := range page.Objects {
			if err := fn(o.Key); err != nil {
				return err
			}
		}
		if page.Next == "" {
			return nil
		}
		token = page.Next
	}
}

type versionAt struct {
	key, id string
	when    int64
}

type journalVersion struct {
	id     string
	marker bool
	when   int64
}

func (s *JournalStore) listVersions(ctx context.Context, prefix string, fn func(key string) error) error {
	newest := map[string]journalVersion{} // newest version of any kind
	live := map[string]journalVersion{}   // newest version that is not a delete marker
	var versions []versionAt
	var keyMarker, versionMarker *string
	for {
		out, err := s.c.api.ListObjectVersions(ctx, &awss3.ListObjectVersionsInput{
			Bucket: &s.c.bucket, Prefix: &prefix, KeyMarker: keyMarker, VersionIdMarker: versionMarker,
		})
		if err != nil {
			return mapError(ctx, "ListObjectVersions", prefix, err)
		}
		for _, v := range out.Versions {
			versions = append(versions, versionAt{key: aws.ToString(v.Key), id: aws.ToString(v.VersionId), when: aws.ToTime(v.LastModified).UnixNano()})
			note(newest, live, aws.ToString(v.Key), journalVersion{id: aws.ToString(v.VersionId), when: aws.ToTime(v.LastModified).UnixNano()})
		}
		for _, m := range out.DeleteMarkers {
			note(newest, live, aws.ToString(m.Key), journalVersion{id: aws.ToString(m.VersionId), marker: true, when: aws.ToTime(m.LastModified).UnixNano()})
		}
		if !aws.ToBool(out.IsTruncated) {
			break
		}
		keyMarker, versionMarker = out.NextKeyMarker, out.NextVersionIdMarker
	}
	sort.SliceStable(versions, func(i, j int) bool { return versions[i].when < versions[j].when })
	readable := map[string][]string{}
	for _, v := range versions {
		readable[v.key] = append(readable[v.key], v.id)
	}
	keys := make([]string, 0, len(live))
	s.mu.Lock()
	for k, v := range live {
		keys = append(keys, k)
		s.latest[k] = v.id
		s.all[k] = readable[k]
	}
	s.mu.Unlock()
	sort.Strings(keys)
	for _, k := range keys {
		if err := fn(k); err != nil {
			return err
		}
	}
	return nil
}

// note records v as the newest version of its key and, unless it is a delete marker, as the newest readable one.
func note(newest, live map[string]journalVersion, key string, v journalVersion) {
	if cur, ok := newest[key]; !ok || v.when > cur.when {
		newest[key] = v
	}
	if v.marker {
		return
	}
	if cur, ok := live[key]; !ok || v.when > cur.when {
		live[key] = v
	}
}

// Get reads an object, the newest version that is not a delete marker when the store was listed with versions.
func (s *JournalStore) Get(ctx context.Context, key string) ([]byte, error) {
	s.mu.Lock()
	version := s.latest[key]
	s.mu.Unlock()
	return s.read(ctx, key, version)
}

// GetAll returns every distinct content the key held in a version that is not a delete marker, oldest first, when the store
// was listed with versions; otherwise the one content. trove rewrites a key only with the same bytes, so a second content is
// a write of somebody else, and the reader replays each (R-HA-054, audit finding H2).
func (s *JournalStore) GetAll(ctx context.Context, key string) ([][]byte, error) {
	s.mu.Lock()
	ids := append([]string(nil), s.all[key]...)
	s.mu.Unlock()
	if len(ids) == 0 {
		b, err := s.Get(ctx, key)
		if err != nil {
			return nil, err
		}
		return [][]byte{b}, nil
	}
	var out [][]byte
	for _, id := range ids {
		b, err := s.read(ctx, key, id)
		if err != nil {
			return nil, err
		}
		if !slices.ContainsFunc(out, func(o []byte) bool { return bytes.Equal(o, b) }) {
			out = append(out, b)
		}
	}
	return out, nil
}

func (s *JournalStore) read(ctx context.Context, key, version string) ([]byte, error) {
	var rc io.ReadCloser
	if version != "" {
		out, err := s.c.api.GetObject(ctx, &awss3.GetObjectInput{Bucket: &s.c.bucket, Key: &key, VersionId: &version})
		if err != nil {
			return nil, mapError(ctx, "GetObject", key, err)
		}
		rc = out.Body
	} else {
		r, _, err := s.c.Get(ctx, key, nil)
		if err != nil {
			return nil, err
		}
		rc = r
	}
	defer func() { _ = rc.Close() }()
	b, err := io.ReadAll(io.LimitReader(rc, maxJournalObject+1))
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", key, err)
	}
	if len(b) > maxJournalObject {
		return nil, fmt.Errorf("journal object %s is larger than %d bytes", key, maxJournalObject)
	}
	return b, nil
}
