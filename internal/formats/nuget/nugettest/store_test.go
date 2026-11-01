package nugettest

import (
	"context"
	"encoding/hex"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/nuget/rows"
	"github.com/guided-traffic/trove/internal/meta"
)

// fakeTx is a transaction whose end the test decides.
type fakeTx struct{ rollbacks []func() }

func (*fakeTx) Before(func(context.Context, meta.Tx) error) {}
func (*fakeTx) After(func())                                {}
func (t *fakeTx) OnRollback(fn func())                      { t.rollbacks = append(t.rollbacks, fn) }
func (*fakeTx) Value(any) any                               { return nil }
func (*fakeTx) SetValue(any, any)                           {}

func (t *fakeTx) rollBack() {
	for _, fn := range t.rollbacks {
		fn()
	}
}

func versionRow(id, v string, sha byte, opts ...func(*rows.VersionInput)) rows.VersionInput {
	in := rows.VersionInput{
		RepoID: 1, VersionID: int64(sha), LowerID: id, DeclaredID: id, OriginalVersion: v, LowerVersion: v, SortKey: []byte(v),
		SHA512: []byte{sha, sha}, Origin: rows.OriginPush, Files: []rows.File{{Path: id + "/" + v, SHA256: hex.EncodeToString([]byte{sha}), Size: 1}},
	}
	for _, o := range opts {
		o(&in)
	}
	return in
}

// TestR_NUGET_006_ATwinWriteLeavesNoTraceWhenItsTransactionRollsBack: the twin gives the tests
// of the other packages the state of the database rows, including the part that matters most:
// what a transaction wrote and lost is gone.
func TestR_NUGET_006_ATwinWriteLeavesNoTraceWhenItsTransactionRollsBack(t *testing.T) {
	ctx := context.Background()
	s := New()
	tx := &fakeTx{}
	if err := s.InsertVersion(ctx, tx, versionRow("acme.widgets", "1.0.0", 1)); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := s.VersionFacts(ctx, 1, "acme.widgets", "1.0.0"); !found {
		t.Fatal("the version is not visible in its own transaction")
	}
	tx.rollBack()
	if _, found, _ := s.VersionFacts(ctx, 1, "acme.widgets", "1.0.0"); found || len(s.Published) != 0 || len(s.Journal) != 0 {
		t.Error("a rolled-back insert left a trace")
	}
}

// TestR_NUGET_006_ATwinTombstoneReadsTheDigestOfTheRemovedBlobAndOutlivesTheVersion: the
// tombstone holds the SHA-512 of the blob the SHA-256 names, keeps its first record, drops the
// NuGet row as the cascade of the database does, and refuses to be written for a blob it does
// not know.
func TestR_NUGET_006_ATwinTombstoneReadsTheDigestOfTheRemovedBlobAndOutlivesTheVersion(t *testing.T) {
	ctx := context.Background()
	s := New()
	in := versionRow("acme.widgets", "1.0.0", 1)
	if err := s.InsertVersion(ctx, &fakeTx{}, in); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertTombstone(ctx, &fakeTx{}, rows.TombstoneInput{RepoID: 1, LowerID: "acme.widgets", LowerVersion: "1.0.0", SHA256: []byte{9}}); err == nil {
		t.Fatal("a tombstone was written for a blob the twin does not know")
	}
	tx := &fakeTx{}
	sum, err := s.InsertTombstone(ctx, tx, rows.TombstoneInput{RepoID: 1, LowerID: "acme.widgets", LowerVersion: "1.0.0", SHA256: []byte{1}})
	if err != nil || string(sum) != string(in.SHA512) {
		t.Fatalf("InsertTombstone = %x, %v", sum, err)
	}
	if st, _ := s.ServeState(ctx, 1, "acme.widgets", "1.0.0"); st.Stored || !st.Tombstoned {
		t.Errorf("serve state after the removal = %+v", st)
	}
	tx.rollBack()
	if st, _ := s.ServeState(ctx, 1, "acme.widgets", "1.0.0"); !st.Stored || st.Tombstoned {
		t.Errorf("serve state after the rollback = %+v", st)
	}
}

// TestR_NUGET_007_ATwinListsAndUnlists: the listing changes once, reports whether it did, and
// the lists and the registration follow.
func TestR_NUGET_007_ATwinListsAndUnlists(t *testing.T) {
	ctx := context.Background()
	s := New()
	for i, v := range []string{"1.0.0", "1.1.0", "2.0.0-beta"} {
		pre := v == "2.0.0-beta"
		if err := s.InsertVersion(ctx, &fakeTx{}, versionRow("acme.widgets", v, byte(i+1), func(in *rows.VersionInput) { in.IsPrerelease = pre })); err != nil {
			t.Fatal(err)
		}
	}
	changed, err := s.SetListed(ctx, &fakeTx{}, rows.ListingInput{VersionID: 2, Listed: false})
	if err != nil || !changed {
		t.Fatalf("unlist = %v, %v", changed, err)
	}
	if changed, _ := s.SetListed(ctx, &fakeTx{}, rows.ListingInput{VersionID: 2, Listed: false}); changed {
		t.Error("a repeated unlist reports a change")
	}
	vs, _ := s.Versions(ctx, 1, "acme.widgets")
	if len(vs) != 3 || vs[1].Listed || !vs[0].Listed {
		t.Errorf("versions = %+v: unlisted versions stay in the list", vs)
	}
	reg, found, _ := s.Registration(ctx, 1, "acme.widgets")
	if !found || len(reg.Versions) != 3 || reg.DisplayName != "acme.widgets" || reg.Rev != 3 {
		t.Errorf("registration = %+v (found %v)", reg, found)
	}
	hits, _ := s.Search(ctx, rows.SearchQuery{RepoID: 1, Terms: []rows.SearchTerm{{Value: "acme"}}})
	if len(hits) != 1 || hits[0].Latest.LowerVersion != "1.0.0" || len(hits[0].Listed) != 1 {
		t.Errorf("search without pre-releases = %+v, want the listed release only", hits)
	}
	hits, _ = s.Search(ctx, rows.SearchQuery{RepoID: 1, Prerelease: true, Terms: []rows.SearchTerm{{Value: "acme"}}})
	if len(hits) != 1 || hits[0].Latest.LowerVersion != "2.0.0-beta" || len(hits[0].Listed) != 2 {
		t.Errorf("search with pre-releases = %+v", hits)
	}
	if hits, _ := s.Search(ctx, rows.SearchQuery{RepoID: 1, Terms: []rows.SearchTerm{{Value: "nothing"}}}); len(hits) != 0 {
		t.Errorf("a term that matches nothing found %+v", hits)
	}
	if _, found, _ := s.Registration(ctx, 1, "acme.nothing"); found {
		t.Error("an ID without a version has a registration")
	}
}

// TestR_NUGET_006_ATwinRefusesASecondRowForOneNumberAndPromotesOnlyReservations: the unique
// index of the database is the twin's, and only a reservation becomes a removal.
func TestR_NUGET_006_ATwinRefusesASecondRowForOneNumberAndPromotesOnlyReservations(t *testing.T) {
	ctx := context.Background()
	s := New()
	if err := s.InsertVersion(ctx, &fakeTx{}, versionRow("acme.widgets", "1.0.0", 1)); err != nil {
		t.Fatal(err)
	}
	err := s.InsertVersion(ctx, &fakeTx{}, versionRow("acme.widgets", "1.0.0", 2))
	if e, ok := errs.As(err); !ok || e.Code != errs.CodeNuGetVersionExists {
		t.Errorf("second row = %v, want nuget_version_exists", err)
	}
	s.SeedTombstone(1, "acme.widgets", "2.0.0", []byte{7}, rows.TombstoneReserved)
	s.SeedTombstone(1, "acme.widgets", "3.0.0", []byte{8}, rows.TombstoneRemoved)
	if _, ok, _ := s.PromoteReserved(ctx, &fakeTx{}, rows.ReservedInput{RepoID: 1, LowerID: "acme.widgets", LowerVersion: "3.0.0"}); ok {
		t.Error("a removal was promoted")
	}
	tx := &fakeTx{}
	if sum, ok, _ := s.PromoteReserved(ctx, tx, rows.ReservedInput{RepoID: 1, LowerID: "acme.widgets", LowerVersion: "2.0.0"}); !ok || string(sum) != "\x07" {
		t.Error("a reservation was not promoted")
	}
	if tomb, _, _ := s.Tombstone(ctx, nil, 1, "acme.widgets", "2.0.0"); tomb.Kind != rows.TombstoneRemoved {
		t.Errorf("kind = %q", tomb.Kind)
	}
	tx.rollBack()
	if tomb, _, _ := s.Tombstone(ctx, nil, 1, "acme.widgets", "2.0.0"); tomb.Kind != rows.TombstoneReserved {
		t.Errorf("kind after the rollback = %q", tomb.Kind)
	}
}
