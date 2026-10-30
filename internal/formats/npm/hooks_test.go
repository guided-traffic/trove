package npm

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/npm/manifest"
	"github.com/guided-traffic/trove/internal/formats/npm/name"
	"github.com/guided-traffic/trove/internal/formats/npm/rows"
	"github.com/guided-traffic/trove/internal/formats/npm/tarball"
)

// fakeWriter records the calls a hook makes on the package writer, in order.
type fakeWriter struct {
	calls   []string
	live    []formats.VersionRow
	created bool
	claim   error
	auto    formats.AutoClaimResult
	claims  []formats.OwnershipWrite
	inserts []formats.NewVersion
	props   map[string]string
	notes   []string
}

var _ formats.PackageWriter = (*fakeWriter)(nil)

func (w *fakeWriter) note(s string) { w.calls = append(w.calls, s) }

func (w *fakeWriter) EnsureLocked(_ context.Context, repo formats.RepoRef, n, _ string) (formats.PackageRef, bool, error) {
	w.note("EnsureLocked")
	return formats.PackageRef{ID: 42, Repo: repo, Name: n, RevSeq: 1}, w.created, nil
}

func (w *fakeWriter) Versions(context.Context, formats.PackageRef) ([]formats.VersionRow, error) {
	w.note("Versions")
	return w.live, nil
}

func (w *fakeWriter) InsertVersion(_ context.Context, _ formats.PackageRef, v formats.NewVersion) (formats.VersionID, error) {
	w.note("InsertVersion")
	w.inserts = append(w.inserts, v)
	return 77, nil
}

func (w *fakeWriter) TrashOrDeleteVersion(context.Context, formats.VersionID, bool) error { return nil }

func (w *fakeWriter) SetPackageAttributes(context.Context, formats.PackageRef, json.RawMessage) error {
	return nil
}

func (w *fakeWriter) SetVersionAttributes(context.Context, formats.VersionID, json.RawMessage) error {
	return nil
}

func (w *fakeWriter) LinkItem(_ context.Context, _ formats.ItemRef, v formats.VersionID, lead bool) error {
	w.note("LinkItem")
	if v != 5 || lead {
		return errors.New("wrong link")
	}
	return nil
}

func (w *fakeWriter) SetProperties(_ context.Context, _ formats.ItemRef, p map[string]string) error {
	w.note("SetProperties")
	w.props = p
	return nil
}

func (w *fakeWriter) BumpRevision(context.Context, formats.PackageRef) (formats.RevSeq, time.Time, error) {
	w.note("BumpRevision")
	return 3, time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC), nil
}

func (w *fakeWriter) Claim(_ context.Context, _ formats.Target, ow formats.OwnershipWrite) error {
	w.note("Claim")
	w.claims = append(w.claims, ow)
	return w.claim
}

func (w *fakeWriter) AutoClaim(context.Context, string) (formats.AutoClaimResult, error) {
	w.note("AutoClaim")
	return w.auto, nil
}

func (w *fakeWriter) Notify(area string, payload any) error {
	w.note("Notify")
	w.notes = append(w.notes, area+":"+payload.(string))
	return nil
}
func (w *fakeWriter) Enqueue(context.Context, formats.JobSpec) error { return nil }

func (w *fakeWriter) DeleteItems(context.Context, formats.RepoRef, ...string) error {
	w.note("DeleteItems")
	return nil
}

// fakeStore records the rows a hook hands the data port.
type fakeStore struct {
	Store
	tags      map[string]string
	published []rows.PublishInput
	err       error
}

func (s *fakeStore) Tags(context.Context, formats.Tx, int64) (map[string]string, error) {
	return s.tags, nil
}

func (s *fakeStore) Publish(_ context.Context, _ formats.Tx, in rows.PublishInput) error {
	s.published = append(s.published, in)
	return s.err
}

func publishEvent(t *testing.T, w *fakeWriter, st Store, pc *publishCommit) formats.CommitEvent {
	t.Helper()
	return formats.CommitEvent{
		Origin: formats.OriginUpload, Repo: formats.RepoRef{ID: 7, Key: "npm-local", Class: formats.Local, Format: "npm"},
		Item: formats.ItemRef{ID: 9, Path: TarballPath(pc.name, pc.version)}, Actor: "11111111-1111-1111-1111-111111111111",
		Payload: pc, Packages: w, Data: st,
	}
}

func newPublish(t *testing.T, pkg, version, tag string) *publishCommit {
	t.Helper()
	n, err := name.Parse(pkg)
	if err != nil {
		t.Fatal(err)
	}
	return &publishCommit{
		name: n, version: version, tag: tag, principal: "11111111-1111-1111-1111-111111111111",
		stored: manifest.Manifest{"name": pkg, "version": version},
		res:    &tarball.Result{Manifest: manifest.Manifest{"name": pkg}, FileCount: 3, UnpackedSize: 1200, Readme: "# hi", ReadmeFilename: "README.md"},
		audit:  rows.Audit{RepoKey: "npm-local"}, latestGuard: true,
	}
}

func TestR_NPM_007_OnCommitOfAFirstPublish(t *testing.T) {
	f := New()
	w := &fakeWriter{created: true, auto: formats.AutoClaimResult{Created: true, Pattern: "@acme/ui"}}
	st := &fakeStore{}
	pc := newPublish(t, "@acme/ui", "1.2.0", "latest")
	if err := f.OnCommit(context.Background(), nil, publishEvent(t, w, st, pc)); err != nil {
		t.Fatal(err)
	}
	// The package row is locked first, ownership is checked under that lock, the version
	// and its revision follow, and the npm rows are written last.
	order := strings.Join(w.calls, " ")
	want := "EnsureLocked Versions Claim AutoClaim InsertVersion BumpRevision SetProperties Notify"
	if order != want {
		t.Fatalf("calls %q, want %q", order, want)
	}
	if len(w.claims) != 1 || !w.claims[0].Publish || !w.claims[0].FirstVersion {
		t.Fatalf("ownership %+v", w.claims)
	}
	if v := w.inserts[0]; v.Version != "1.2.0" || v.LeadItem == nil || v.LeadItem.ID != 9 || v.CreatedBy != "11111111-1111-1111-1111-111111111111" {
		t.Fatalf("version %+v", v)
	}
	if w.props["npm.name"] != "@acme/ui" || w.props["npm.version"] != "1.2.0" || w.props["npm.scope"] != "@acme" {
		t.Fatalf("properties %v", w.props)
	}
	if len(st.published) != 1 {
		t.Fatalf("published %d", len(st.published))
	}
	in := st.published[0]
	if in.PackageID != 42 || in.VersionID != 77 || in.RepoID != 7 || in.NameFold != "@acme/ui" || in.Tag != "latest" ||
		in.Tags["latest"] != "1.2.0" || in.LatestKept || in.Claim != `created "@acme/ui"` {
		t.Fatalf("rows %+v", in)
	}
	if pc.rev != 3 || pc.pkg != 42 || len(w.notes) != 1 || w.notes[0] != "metadata_changed:"+formats.PackageChanged(7, "@acme/ui") {
		t.Fatalf("result rev %d pkg %d notes %v", pc.rev, pc.pkg, w.notes)
	}
}

func TestR_NPM_007_OnCommitOfALaterVersion(t *testing.T) {
	f := New()
	w := &fakeWriter{live: []formats.VersionRow{{ID: 1, Version: "1.0.0"}}}
	st := &fakeStore{tags: map[string]string{"latest": "2.0.0", "next": "2.1.0-rc.1"}}
	pc := newPublish(t, "left-pad", "1.5.0", "latest")
	if err := f.OnCommit(context.Background(), nil, publishEvent(t, w, st, pc)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(w.calls, " "), "AutoClaim") {
		t.Fatal("the auto claim runs for a later version")
	}
	if w.claims[0].FirstVersion {
		t.Fatal("a later version is a first version")
	}
	in := st.published[0]
	if !in.LatestKept || in.Tags["latest"] != "2.0.0" || in.Tags["next"] != "2.1.0-rc.1" || in.Tag != "latest" {
		t.Fatalf("the latest guard did not hold: %+v", in)
	}
	if pc.latest != "2.0.0" || !pc.kept {
		t.Fatalf("result %q %v", pc.latest, pc.kept)
	}
}

func TestR_NPM_016_LatestGuardSetting(t *testing.T) {
	f := New()
	publish := func(guard bool) *publishCommit {
		w := &fakeWriter{live: []formats.VersionRow{{ID: 1, Version: "1.0.0"}}}
		st := &fakeStore{tags: map[string]string{"latest": "2.0.0"}}
		pc := newPublish(t, "left-pad", "1.5.0", "latest")
		pc.latestGuard = guard
		if err := f.OnCommit(context.Background(), nil, publishEvent(t, w, st, pc)); err != nil {
			t.Fatal(err)
		}
		return pc
	}
	t.Run("R-NPM-016 npm.latestGuard on keeps latest where it is", func(t *testing.T) {
		if pc := publish(true); pc.latest != "2.0.0" || !pc.kept {
			t.Fatalf("latest %q kept %v", pc.latest, pc.kept)
		}
	})
	t.Run("R-NPM-016 npm.latestGuard off lets the publish move latest", func(t *testing.T) {
		if pc := publish(false); pc.latest != "1.5.0" || pc.kept {
			t.Fatalf("latest %q kept %v", pc.latest, pc.kept)
		}
	})
	t.Run("R-NPM-016 the settings read from the stored document default the guard on", func(t *testing.T) {
		for config, want := range map[string]bool{``: true, `{}`: true, `{"npm":{}}`: true, `{"npm":{"latestGuard":false}}`: false, `{"npm":{"latestGuard":true}}`: true} {
			if got := settingsOf([]byte(config)).LatestGuard; got != want {
				t.Errorf("settingsOf(%q).LatestGuard = %v, want %v", config, got, want)
			}
		}
	})
}

func TestR_NPM_007_OnCommitRefusals(t *testing.T) {
	f := New()
	t.Run("R-NPM-008 a number that is live is 409 and nothing is written", func(t *testing.T) {
		w := &fakeWriter{live: []formats.VersionRow{{ID: 1, Version: "1.0.0"}}}
		st := &fakeStore{}
		err := f.OnCommit(context.Background(), nil, publishEvent(t, w, st, newPublish(t, "p", "1.0.0", "latest")))
		if e, _ := errs.As(err); e == nil || e.Code != "npm_version_exists" || len(st.published) != 0 || len(w.inserts) != 0 {
			t.Fatalf("%v", err)
		}
	})
	t.Run("R-REPO-048 a first version whose shadowing check was skipped is refused and the client retries", func(t *testing.T) {
		// The check found a live version and asked nothing; an unpublish removed it before the commit.
		w := &fakeWriter{}
		st := &fakeStore{}
		pc := newPublish(t, "p", "2.0.0", "latest")
		pc.shadowSkipped = true
		err := f.OnCommit(context.Background(), nil, publishEvent(t, w, st, pc))
		e, _ := errs.As(err)
		if e == nil || e.Code != errs.CodeShadowCheckUnavailable || e.Kind != errs.Unavailable || e.RetryAfter <= 0 {
			t.Fatalf("%v", err)
		}
		if len(w.inserts) != 0 || len(st.published) != 0 || strings.Contains(strings.Join(w.calls, " "), "Claim") {
			t.Fatalf("calls %v", w.calls)
		}
		// A later version after a skipped check is the ordinary case.
		w = &fakeWriter{live: []formats.VersionRow{{ID: 1, Version: "1.0.0"}}}
		pc = newPublish(t, "p", "2.0.0", "latest")
		pc.shadowSkipped = true
		if err := f.OnCommit(context.Background(), nil, publishEvent(t, w, &fakeStore{}, pc)); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("R-AUTHZ-018 a refused ownership stops the publish before any row", func(t *testing.T) {
		w := &fakeWriter{claim: errs.E(errs.Forbidden, errs.CodePackageOwnedByOtherOwner, "owned")}
		st := &fakeStore{}
		err := f.OnCommit(context.Background(), nil, publishEvent(t, w, st, newPublish(t, "p", "1.0.0", "latest")))
		if e, _ := errs.As(err); e == nil || e.Code != "package_owned_by_other_owner" {
			t.Fatalf("%v", err)
		}
		if len(w.inserts) != 0 || len(st.published) != 0 || strings.Contains(strings.Join(w.calls, " "), "AutoClaim") {
			t.Fatalf("calls %v", w.calls)
		}
	})
	t.Run("R-NPM-007 an error of the data port rolls the commit back", func(t *testing.T) {
		boom := errs.E(errs.Invalid, errs.CodeNPMInvalidName, "look-alike")
		w, st := &fakeWriter{}, &fakeStore{err: boom}
		if err := f.OnCommit(context.Background(), nil, publishEvent(t, w, st, newPublish(t, "p", "1.0.0", "latest"))); !errors.Is(err, boom) {
			t.Fatalf("%v", err)
		}
	})
	t.Run("R-NPM-008 a raw upload is refused, a fill is not served yet", func(t *testing.T) {
		w := &fakeWriter{}
		ev := formats.CommitEvent{Origin: formats.OriginUpload, Packages: w}
		if e, _ := errs.As(f.OnCommit(context.Background(), nil, ev)); e == nil || e.Code != "npm_bad_publish" {
			t.Fatalf("%v", e)
		}
		ev.Origin = formats.OriginFill
		if err := f.OnCommit(context.Background(), nil, ev); err != nil {
			t.Fatal(err)
		}
		if len(w.calls) != 0 {
			t.Fatalf("calls %v", w.calls)
		}
	})
	t.Run("R-NPM-007 without the bound port the hook says so", func(t *testing.T) {
		w := &fakeWriter{}
		err := f.OnCommit(context.Background(), nil, publishEvent(t, w, nil, newPublish(t, "p", "1.0.0", "latest")))
		if errs.KindOf(err) != errs.Internal {
			t.Fatalf("%v", err)
		}
	})
}

func TestR_NPM_007_OnCommitOfAProvenanceBundle(t *testing.T) {
	f := New()
	n, _ := name.Parse("p")
	ev := func(w *fakeWriter, sc *sigstoreCommit) formats.CommitEvent {
		return formats.CommitEvent{
			Origin: formats.OriginUpload, Repo: formats.RepoRef{ID: 7}, Item: formats.ItemRef{ID: 10, Path: SigstorePath(n, "1.0.0")},
			Payload: sc, Packages: w,
		}
	}
	t.Run("it is linked to the live version, not as its lead item", func(t *testing.T) {
		w := &fakeWriter{live: []formats.VersionRow{{ID: 5, Version: "1.0.0"}}}
		sc := &sigstoreCommit{name: n, version: "1.0.0"}
		if err := f.OnCommit(context.Background(), nil, ev(w, sc)); err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(w.calls, " "); got != "EnsureLocked Versions Claim LinkItem BumpRevision Notify" || sc.rev != 3 {
			t.Fatalf("calls %q rev %d", got, sc.rev)
		}
	})
	t.Run("a version that is not live any more is a conflict", func(t *testing.T) {
		w := &fakeWriter{live: []formats.VersionRow{{ID: 5, Version: "0.9.0"}}}
		err := f.OnCommit(context.Background(), nil, ev(w, &sigstoreCommit{name: n, version: "1.0.0"}))
		if e, _ := errs.As(err); e == nil || e.Code != "npm_version_unpublished" {
			t.Fatalf("%v", err)
		}
	})
}

// TestR_AUTHZ_007_OwnershipHelperPerOperation: the helper every write of npm asks the
// ownership question with: a publish adds a version (and claims a first one), a tag
// change, a deprecation and an unpublish do not; unpublish asks for delete.
func TestR_AUTHZ_007_OwnershipHelperPerOperation(t *testing.T) {
	f := New()
	for _, c := range []struct {
		op      operation
		publish bool
		first   bool
		action  formats.Action
	}{
		{opPublish, true, true, formats.ActionDeploy},
		{opPublish, true, false, formats.ActionDeploy},
		{opDistTag, false, false, formats.ActionDeploy},
		{opDeprecate, false, false, formats.ActionDeploy},
		{opUnpublish, false, false, formats.ActionDelete},
	} {
		var got formats.Target
		var gotW formats.OwnershipWrite
		w := &claimRecorder{fn: func(t formats.Target, ow formats.OwnershipWrite) { got, gotW = t, ow }}
		if err := f.claim(context.Background(), w, c.op, "p", "1.0.0", c.first); err != nil {
			t.Fatal(err)
		}
		if gotW.Publish != c.publish || gotW.FirstVersion != c.first || len(got.Actions) != 1 || got.Actions[0] != c.action || got.Package != "p" {
			t.Errorf("op %d: %+v %+v", c.op, got, gotW)
		}
	}
}

type claimRecorder struct {
	fakeWriter
	fn func(formats.Target, formats.OwnershipWrite)
}

func (c *claimRecorder) Claim(_ context.Context, t formats.Target, ow formats.OwnershipWrite) error {
	c.fn(t, ow)
	return nil
}
