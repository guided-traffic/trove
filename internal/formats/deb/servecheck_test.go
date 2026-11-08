package deb

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/deb/rows"
	"github.com/guided-traffic/trove/internal/formats/formattest"
	"github.com/guided-traffic/trove/internal/formats/untrusted"
	"github.com/guided-traffic/trove/internal/servefacts"
)

// markStore answers the mark of one path, as the replay writes it (R-DEB-032).
type markStore struct {
	Store
	withheld string
	asked    int
}

func (m *markStore) Tombstone(_ context.Context, _ int64, path string) (rows.Tombstone, bool, error) {
	m.asked++
	return rows.Tombstone{Path: path}, path == m.withheld, nil
}

func isNotFound(err error) bool {
	var e *errs.Error
	return errors.As(err, &e) && e.Kind == errs.NotFound
}

// A package file the replay withheld is refused with the 404 of a missing file, by the facts the item lookup read with the item and,
// when it read none, by the data port (R-DEB-032, Q-ARCH-381).
func TestServeCheckRefusesAWithheldFile(t *testing.T) {
	f := New()
	const path = "pool/h/hello/hello_2.10-3_amd64.deb"
	st := &markStore{withheld: path}
	h := formattest.NewHost(f, formattest.HostOptions{Data: st})
	repo := h.Ref()
	ctx := context.Background()
	key := f.ServeFactsKey(repo, formats.Path{Path: path})
	if key.Kind != servefacts.DebLocal {
		t.Fatalf("key = %+v", key)
	}

	// Facts read with the item decide without a statement of the check's own.
	if err := f.ServeCheck(ctx, h, repo, formats.ItemRef{Repo: repo, Path: path, Facts: servefacts.Facts{Key: key, Tombstoned: true}}, formats.ServeStored); !isNotFound(err) {
		t.Errorf("withheld by the facts: %v", err)
	}
	if err := f.ServeCheck(ctx, h, repo, formats.ItemRef{Repo: repo, Path: path, Facts: servefacts.Facts{Key: key}}, formats.ServeStored); err != nil {
		t.Errorf("not withheld by the facts: %v", err)
	}
	if st.asked != 0 {
		t.Errorf("the check asked the port %d times although the facts were read for its key", st.asked)
	}
	// Facts read for another key are no facts for this one.
	if err := f.ServeCheck(ctx, h, repo, formats.ItemRef{Repo: repo, Path: path}, formats.ServeStored); !isNotFound(err) || st.asked != 1 {
		t.Errorf("withheld by the port: %v (asked %d)", err, st.asked)
	}
	if err := f.ServeCheck(ctx, h, repo, formats.ItemRef{Repo: repo, Path: "pool/h/hello/hello_2.10-4_amd64.deb"}, formats.ServeStored); err != nil {
		t.Errorf("another file: %v", err)
	}
	// A generated file is no package.
	if err := f.ServeCheck(ctx, h, repo, formats.ItemRef{Repo: repo, Path: "dists/stable/InRelease"}, formats.ServeStored); err != nil || st.asked != 2 {
		t.Errorf("a generated file: %v (asked %d)", err, st.asked)
	}
}

func TestServeFactsKey(t *testing.T) {
	f := New()
	local := formats.RepoRef{ID: 1, Class: formats.Local}
	remote := formats.RepoRef{ID: 2, Class: formats.Remote}
	virtual := formats.RepoRef{ID: 3, Class: formats.Virtual}
	cases := []struct {
		name string
		repo formats.RepoRef
		path string
		want servefacts.Kind
	}{
		{"a package file of a local repository", local, "pool/h/hello/hello_2.10-3_amd64.deb", servefacts.DebLocal},
		{"a pool file outside the form", local, "pool/h/hello/hello.deb", servefacts.DebLocal},
		{"a generated file of a local repository", local, "dists/stable/InRelease", servefacts.None},
		{"the signing key", local, "signing-key.asc", servefacts.None},
		{"a pool file of a remote", remote, "pool/main/h/hello/hello_2.10-3_amd64.deb", servefacts.DebRemote},
		{"a flat file of a remote", remote, "binary/trove-hello_1.0_all.deb", servefacts.DebRemote},
		{"a release document of a remote", remote, "dists/bookworm/InRelease", servefacts.None},
		{"an index of a flat remote", remote, "binary/Packages.gz", servefacts.None},
		{"a by-hash file of a remote", remote, "dists/bookworm/main/binary-amd64/by-hash/SHA256/" + zeros, servefacts.None},
		{"a virtual repository", virtual, "members/deb-local/pool/h/hello/hello_2.10-3_amd64.deb", servefacts.None},
	}
	for _, c := range cases {
		if got := f.ServeFactsKey(c.repo, formats.Path{Path: c.path}).Kind; got != c.want {
			t.Errorf("%s: kind %q, want %q", c.name, got, c.want)
		}
	}
}

// Until the local class reads package files, Inspect checks the ar signature (R-DEB-007): input that is no ar archive is 422
// deb_invalid_archive, an archive is not read yet.
func TestInspectOfTheSkeleton(t *testing.T) {
	in := func(body []byte) formats.InspectInput {
		return formats.InspectInput{Body: bytesSpool(body), Limits: untrusted.DefaultLimits()}
	}
	_, err := New().Inspect(context.Background(), in([]byte("not an archive")))
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != errs.CodeDebInvalidArchive || e.Status() != 422 {
		t.Errorf("not an archive: %v", err)
	}
	_, err = New().Inspect(context.Background(), in(nil))
	if !errors.As(err, &e) || e.Code != errs.CodeDebInvalidArchive {
		t.Errorf("empty: %v", err)
	}
	_, err = New().Inspect(context.Background(), in([]byte("!<arch>\ndebian-binary")))
	if !errors.As(err, &e) || e.Code != errs.CodeDebNotImplemented {
		t.Errorf("an archive: %v", err)
	}
}

type bytesSpool []byte

func (b bytesSpool) Open() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(b)), nil }

func (b bytesSpool) ReaderAt() (io.ReaderAt, error) { return bytes.NewReader(b), nil }

func (b bytesSpool) Digests() formats.Digests { return formats.Digests{} }

func (b bytesSpool) Size() int64 { return int64(len(b)) }
