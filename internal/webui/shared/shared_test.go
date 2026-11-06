package shared

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"testing"

	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/webui"
)

// The store's statements run against PostgreSQL and the object store in test/integration/uiassets; these are the parts that
// decide before either is asked.

func TestAssetAsksOnlyForHashedNames(t *testing.T) {
	t.Run("R-HA-020 a name that is not of the hashed pattern is never looked up", func(t *testing.T) {
		s := New(nil, nil, "test", nil) // a lookup would dereference the nil database
		for _, p := range []string{"index.html", "favicon.ico", "../main-ABCD1234.js", "main.js", "a/b/c/main-ABCD1234.js"} {
			if _, err := s.Asset(context.Background(), "en", p); !errors.Is(err, webui.ErrNotShared) {
				t.Errorf("Asset(%q) = %v, want ErrNotShared", p, err)
			}
		}
	})
}

// swappedBlobs is a blob store whose objects were replaced in the bucket by other bytes of the same length, as with
// storage.download.verify off nothing below this package would notice. It records the options of every Open.
type swappedBlobs struct {
	body []byte
	opts []blob.OpenOptions
}

func (b *swappedBlobs) Open(_ context.Context, _ blob.Digest, _ int64, o blob.OpenOptions) (io.ReadCloser, error) {
	b.opts = append(b.opts, o)
	return io.NopCloser(bytes.NewReader(b.body)), nil
}

func (b *swappedBlobs) Ingest(context.Context, io.Reader, blob.IngestOptions) (*blob.Spooled, error) {
	return nil, errors.New("not used")
}

func (b *swappedBlobs) CommitAll(context.Context, []*blob.Spooled, blob.LinkFunc) ([]blob.CommitResult, error) {
	return nil, errors.New("not used")
}

func TestReadVerified(t *testing.T) {
	good := []byte("export const release='A'")
	digest := blob.Digest(sha256.Sum256(good))
	t.Run("R-HA-020 a file of the store is read with verification, whatever storage.download.verify says (Q-UI-143)", func(t *testing.T) {
		b := &swappedBlobs{body: good}
		got, err := readVerified(context.Background(), b, digest, int64(len(good)))
		if err != nil || !bytes.Equal(got, good) {
			t.Fatalf("got %q, %v", got, err)
		}
		if len(b.opts) != 1 || !b.opts[0].Verify || b.opts[0].Range != nil {
			t.Fatalf("Open options %+v, want a whole read with Verify", b.opts)
		}
	})
	t.Run("R-HA-020 an object swapped for other bytes of the same length is refused, not served (Q-UI-143)", func(t *testing.T) {
		swapped := bytes.Clone(good)
		swapped[len(swapped)-2] = 'X'
		_, err := readVerified(context.Background(), &swappedBlobs{body: swapped}, digest, int64(len(good)))
		if !errors.Is(err, blob.ErrCorrupt) {
			t.Fatalf("err %v, want ErrCorrupt", err)
		}
	})
	t.Run("R-HA-020 a short or a long object is refused", func(t *testing.T) {
		for _, body := range [][]byte{good[:len(good)-1], append(bytes.Clone(good), '!')} {
			if _, err := readVerified(context.Background(), &swappedBlobs{body: body}, digest, int64(len(good))); err == nil {
				t.Errorf("%d bytes for a row of %d were accepted", len(body), len(good))
			}
		}
	})
}

func TestKeeperLifecycle(t *testing.T) {
	t.Run("R-HA-020 a keeper that never started stops at once, and one that started stops its loop", func(t *testing.T) {
		k := NewKeeper(New(nil, nil, "test", nil), nil, nil, nil)
		if k.Name() != "ui-assets" {
			t.Errorf("name %q", k.Name())
		}
		if err := k.Stop(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := k.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := k.Stop(context.Background()); err != nil {
			t.Fatal(err)
		}
	})
}
