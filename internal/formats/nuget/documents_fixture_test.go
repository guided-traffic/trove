package nuget

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/formattest"
	"github.com/guided-traffic/trove/internal/formats/nuget/nugettest"
	"github.com/guided-traffic/trove/internal/formats/nuget/rows"
	"github.com/guided-traffic/trove/internal/formats/nuget/version"
)

// docStore is the data port the document tests read: the twin, which holds real rows, with a
// counter on the two reads of the documents, so a test can tell a rendition served from the
// cache from one rendered again. The package's revision and display name are the Host's and
// the first version's, as the database reads them in the same statement.
type docStore struct {
	*nugettest.Store
	mu      sync.Mutex
	reads   int
	nextID  int64
	display map[string]string
}

func newDocStore() *docStore {
	return &docStore{Store: nugettest.New(), display: map[string]string{}}
}

func (s *docStore) Versions(ctx context.Context, repo int64, lowerID string) ([]rows.VersionEntry, error) {
	s.count()
	return s.Store.Versions(ctx, repo, lowerID)
}

func (s *docStore) Registration(ctx context.Context, repo int64, lowerID string) (rows.RegistrationPackage, bool, error) {
	s.count()
	return s.Store.Registration(ctx, repo, lowerID)
}

func (s *docStore) count() {
	s.mu.Lock()
	s.reads++
	s.mu.Unlock()
}

func (s *docStore) readCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reads
}

// bind lets the twin read the revision of a package from the Host, the way the database reads it.
func (s *docStore) bind(h *formattest.Host) {
	s.Revisions = func(_ int64, lowerID string) (int64, string) {
		s.mu.Lock()
		defer s.mu.Unlock()
		return int64(h.RevSeq(lowerID)), s.display[lowerID]
	}
}

// docFixture is a Host of the format with a docStore behind it. A change is made through
// put, unlist or remove, which write the store and raise the package's revision in the
// host the way a commit does.
type docFixture struct {
	t     *testing.T
	f     *Format
	host  *formattest.Host
	store *docStore
}

var fixtureTime = time.Date(2026, 10, 28, 9, 14, 3, 0, time.UTC)

func newDocFixture(t *testing.T) *docFixture {
	t.Helper()
	f := New()
	st := newDocStore()
	h := formattest.NewHost(f, formattest.HostOptions{Data: st, BaseURL: "https://trove.example.com"})
	st.bind(h)
	return &docFixture{t: t, f: f, host: h, store: st}
}

// registrationVersion is the row of a version as the store hands it over.
func registrationVersion(id, v string, listed bool, metadata string) rows.RegistrationVersion {
	p := version.MustParse(v)
	return rows.RegistrationVersion{
		VersionEntry: rows.VersionEntry{
			VersionID: 1, LowerVersion: p.URLForm(), FullVersion: p.FullNormalized(), DeclaredID: id, Listed: listed,
			IsPrerelease: p.IsPrerelease(), IsSemver2: p.IsSemver2(), PublishedAt: fixtureTime,
		},
		PackageTypes: []string{"dependency"},
		Metadata:     []byte(metadata),
	}
}

// put stores a version of Acme.Widgets and commits it: the package row's revision rises once.
func (fx *docFixture) put(v string, listed bool, metadata string) {
	fx.t.Helper()
	fx.putAll("acme.widgets", registrationVersion("Acme.Widgets", v, listed, metadata))
}

// putAll commits several versions of one package in one transaction.
func (fx *docFixture) putAll(lowerID string, vs ...rows.RegistrationVersion) {
	fx.t.Helper()
	if err := fx.store.commit(fx.host, lowerID, vs...); err != nil {
		fx.t.Fatal(err)
	}
}

// commit stores versions of a package in the twin and in the host's package rows, the way a
// commit does: the package row's revision rises once.
func (s *docStore) commit(h *formattest.Host, lowerID string, vs ...rows.RegistrationVersion) error {
	err := h.Seed(context.Background(), func(w formats.PackageWriter) error {
		pkg, _, err := w.EnsureLocked(context.Background(), h.Ref(), lowerID, lowerID)
		if err != nil {
			return err
		}
		for _, v := range vs {
			if _, err := w.InsertVersion(context.Background(), pkg, formats.NewVersion{Version: v.LowerVersion}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range vs {
		s.nextID++
		if _, ok := s.display[lowerID]; !ok {
			s.display[lowerID] = v.DeclaredID
		}
		s.Store.Seed(rows.VersionInput{
			RepoID: h.Ref().ID, VersionID: s.nextID, LowerID: lowerID, DeclaredID: v.DeclaredID,
			OriginalVersion: v.FullVersion, LowerVersion: v.LowerVersion, SortKey: version.MustParse(v.LowerVersion).SortKey(), FullVersion: v.FullVersion,
			IsPrerelease: v.IsPrerelease, IsSemver2: v.IsSemver2, PublishedAt: v.PublishedAt,
			PackageTypes: v.PackageTypes, Metadata: v.Metadata, Origin: rows.OriginPush,
		})
		if !v.Listed {
			s.Store.SetListed(context.Background(), nil, rows.ListingInput{VersionID: s.nextID, Listed: false})
		}
	}
	return nil
}

// setListed unlists or relists a version and raises the revision.
func (fx *docFixture) setListed(lowerID, lowerVersion string, listed bool) {
	fx.t.Helper()
	err := fx.host.Seed(context.Background(), func(w formats.PackageWriter) error {
		pkg, _, err := w.EnsureLocked(context.Background(), fx.host.Ref(), lowerID, lowerID)
		if err != nil {
			return err
		}
		_, _, err = w.BumpRevision(context.Background(), pkg)
		return err
	})
	if err != nil {
		fx.t.Fatal(err)
	}
	pkg, ok, err := fx.store.Registration(context.Background(), fx.host.Ref().ID, lowerID)
	if err != nil || !ok {
		fx.t.Fatalf("registration of %s: %v %v", lowerID, ok, err)
	}
	for _, v := range pkg.Versions {
		if v.LowerVersion == lowerVersion {
			if _, err := fx.store.SetListed(context.Background(), nil, rows.ListingInput{VersionID: v.VersionID, Listed: listed}); err != nil {
				fx.t.Fatal(err)
			}
		}
	}
}

// get serves a request and returns the recorded response.
func (fx *docFixture) get(path string, hdr ...string) *formattest.Response {
	return fx.do("GET", path, nil, hdr...)
}

func (fx *docFixture) do(method, path string, endpoint *formats.EndpointFacts, hdr ...string) *formattest.Response {
	fx.t.Helper()
	rq, err := fx.host.NewRequest(context.Background(), method, path, nil)
	if err != nil {
		fx.t.Fatalf("%s %s: %v", method, path, err)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		rq.Header.Set(hdr[i], hdr[i+1])
	}
	if endpoint != nil {
		rq.Endpoint = *endpoint
	}
	return fx.host.ServeRequest(rq, false)
}

// versionsOf builds n listed versions 1.0.0 … 1.0.<n-1>.
func versionsOf(n int) []rows.RegistrationVersion {
	out := make([]rows.RegistrationVersion, n)
	for i := range out {
		out[i] = registrationVersion("Acme.Widgets", fmt.Sprintf("1.0.%d", i), true, `{"description":"d"}`)
	}
	return out
}
