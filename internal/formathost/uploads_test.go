package formathost

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/packages"
	"github.com/guided-traffic/trove/internal/qname"
	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/internal/uploads"
	vt "github.com/guided-traffic/trove/internal/virtual/virtualtest"
)

// A host without a session service answers not_implemented: a format never meets a session that silently does not exist
// (R-STOR-011, ADR-161).
func TestTheUploadSessionPortAnswersNotImplementedWithoutAService(t *testing.T) {
	h := newTestHost(t, &fakeDecider{})
	rq, id := testRequest(formats.Upload), formats.SessionRef{ID: uuid.New(), Binding: "app"}
	u := h.Uploads()
	ctx := context.Background()
	check := func(what string, err error) {
		t.Helper()
		if errs.KindOf(err) != errs.NotImplemented {
			t.Errorf("%s: %v", what, err)
		}
	}
	_, err := u.Start(ctx, rq, formats.RepoRef{}, "app")
	check("Start", err)
	_, err = u.Get(ctx, rq, id)
	check("Get", err)
	_, err = u.Append(ctx, rq, id, 0, strings.NewReader("x"))
	check("Append", err)
	_, err = u.Finalize(ctx, rq, id, [32]byte{}, nil, formats.Commit{})
	check("Finalize", err)
	check("Abort", u.Abort(ctx, rq, id))
}

// fakeSessions is the session service: it records the scope of every call and answers what the test set.
type fakeSessions struct {
	session  uploads.Session
	scope    uploads.Scope
	calls    []string
	appendTo int64
	limit    int64
	err      error
	// finalize, when set, is called with the commit function the host handed in.
	finalize func(commit uploads.CommitFunc) (uploads.Result, error)
	replayed bool
}

func (f *fakeSessions) note(call string, sc uploads.Scope) {
	f.calls, f.scope = append(f.calls, call), sc
}

func (f *fakeSessions) Start(_ context.Context, target int64, sc uploads.Scope) (uploads.Session, error) {
	f.note("start", sc)
	s := f.session
	s.Target = target
	return s, f.err
}

func (f *fakeSessions) Lookup(_ context.Context, _ uuid.UUID, sc uploads.Scope) (uploads.Session, error) {
	f.note("lookup", sc)
	return f.session, f.err
}

func (f *fakeSessions) Get(_ context.Context, _ uuid.UUID, sc uploads.Scope) (uploads.Session, error) {
	f.note("get", sc)
	return f.session, f.err
}

func (f *fakeSessions) Append(_ context.Context, _ uuid.UUID, sc uploads.Scope, _ int64, _ io.Reader, limit int64) (int64, error) {
	f.note("append", sc)
	f.limit = limit
	return f.appendTo, f.err
}

func (f *fakeSessions) Finalize(_ context.Context, _ uuid.UUID, sc uploads.Scope, _ [32]byte, _ io.Reader, limit int64, commit uploads.CommitFunc) (uploads.Result, error) {
	f.note("finalize", sc)
	f.limit = limit
	if f.finalize != nil {
		return f.finalize(commit)
	}
	return uploads.Result{ItemID: 5, Replayed: f.replayed}, f.err
}

func (f *fakeSessions) Abort(_ context.Context, _ uuid.UUID, sc uploads.Scope) error {
	f.note("abort", sc)
	return f.err
}

// knowingDecider is the decision point that also knows other repositories.
type knowingDecider struct {
	fakeDecider
	known []qname.Repo
}

func (d *knowingDecider) Know(_ context.Context, idx *authz.Index, _ authz.Subject, key qname.Repo) (*authz.Index, error) {
	d.known = append(d.known, key)
	return idx, nil
}

// commitItems is the item store that runs the step of the caller in the commit transaction.
type commitItems struct {
	ItemOps
	spec items.CommitSpec
	ran  bool
}

func (c *commitItems) Commit(ctx context.Context, spec items.CommitSpec) (items.CommitResult, error) {
	c.spec, c.ran = spec, true
	if spec.Extra != nil {
		if err := spec.Extra(ctx, nil, items.Ref{ID: 9}); err != nil {
			return items.CommitResult{}, err
		}
	}
	return items.CommitResult{Created: true, Item: items.Item{ID: 9}}, nil
}

type sessionFixture struct {
	h    *Host
	d    *knowingDecider
	svc  *fakeSessions
	it   *commitItems
	rq   *formats.Request
	user string
}

func newSessionFixture(t *testing.T, repo repository.Repository, opts ...func(*Options)) *sessionFixture {
	t.Helper()
	pk, err := packages.NewStore(nopRunner{})
	if err != nil {
		t.Fatal(err)
	}
	f := &sessionFixture{d: &knowingDecider{}, svc: &fakeSessions{}, it: &commitItems{}, user: uuid.NewString()}
	oo := Options{Runner: nopRunner{}, Decider: f.d, Items: f.it, Packages: pk, Uploads: f.svc}
	for _, o := range opts {
		o(&oo)
	}
	f.h, err = New(oo)
	if err != nil {
		t.Fatal(err)
	}
	f.rq = testRequest(formats.Upload)
	f.rq.Repo = &repo
	f.rq.Subject.ID = f.user
	f.h.Begin(f.rq)
	return f
}

func localRepo() repository.Repository { return vt.LocalRepo(42, "docker-local") }

func TestR_STOR_011_TheHostDecidesDeployOnTheTargetBeforeEveryCall(t *testing.T) {
	ref := formats.SessionRef{ID: uuid.New(), Binding: "team/app"}
	target := localRepo()
	for name, call := range map[string]func(f *sessionFixture) error{
		"Get": func(f *sessionFixture) error { _, err := f.h.Uploads().Get(t.Context(), f.rq, ref); return err },
		"Append": func(f *sessionFixture) error {
			_, err := f.h.Uploads().Append(t.Context(), f.rq, ref, 0, strings.NewReader("x"))
			return err
		},
		"Finalize": func(f *sessionFixture) error {
			_, err := f.h.Uploads().Finalize(t.Context(), f.rq, ref, [32]byte{}, nil, formats.Commit{})
			return err
		},
		"Abort": func(f *sessionFixture) error { return f.h.Uploads().Abort(t.Context(), f.rq, ref) },
	} {
		t.Run(name+" a permission that is gone ends the session", func(t *testing.T) {
			f := newSessionFixture(t, target)
			f.svc.session = uploads.Session{ID: ref.ID, Target: 42, Binding: ref.Binding}
			f.d.decision = authz.Decision{Outcome: authz.DenyVisible}
			err := call(f)
			var e *errs.Error
			if !errors.As(err, &e) || e.Kind != errs.NotFound || e.Code != errs.CodeOCIBlobUploadUnknown {
				t.Fatalf("err = %v", err)
			}
			if got := f.svc.calls; len(got) != 1 || (got[0] != "lookup" && got[0] != "get") {
				t.Fatalf("the service was asked for more than the session: %v", got)
			}
		})
		t.Run(name+" the scope is the principal, the addressed repository and the binding", func(t *testing.T) {
			f := newSessionFixture(t, target)
			f.svc.session = uploads.Session{ID: ref.ID, Target: 42, Binding: ref.Binding}
			if err := call(f); err != nil {
				t.Fatal(err)
			}
			want := uploads.Scope{Addressed: 42, Binding: "team/app", Principal: f.user}
			if f.svc.scope != want {
				t.Fatalf("scope = %+v, want %+v", f.svc.scope, want)
			}
			c := f.d.got
			if c.Repository != target.Name || c.Package != "team/app" || len(c.Actions) != 1 || c.Actions[0] != authz.Deploy {
				t.Fatalf("coordinate = %+v", c)
			}
		})
	}
}

func TestR_STOR_011_ASessionOfAnotherTargetIsNotFound(t *testing.T) {
	f := newSessionFixture(t, localRepo())
	f.svc.session = uploads.Session{Target: 99}
	_, err := f.h.Uploads().Get(t.Context(), f.rq, formats.SessionRef{ID: uuid.New(), Binding: "app"})
	if errs.KindOf(err) != errs.NotFound {
		t.Fatalf("err = %v", err)
	}
}

func TestR_STOR_011_AServiceRefusalPassesThroughAndTheReceivedCountIsReturned(t *testing.T) {
	f := newSessionFixture(t, localRepo())
	f.svc.session = uploads.Session{Target: 42}
	f.svc.appendTo = 4096
	f.svc.err = errs.E(errs.RangeNotSatisfiable, errs.CodeOCIBlobUploadInvalid, "out of order")
	// Lookup fails with the same error; the service's answer is the host's
	got, err := f.h.Uploads().Append(t.Context(), f.rq, formats.SessionRef{ID: uuid.New(), Binding: "app"}, 0, strings.NewReader("x"))
	if errs.KindOf(err) != errs.RangeNotSatisfiable || got != 0 {
		t.Fatalf("received %d, err %v", got, err)
	}
	f.svc.err = nil
	got, err = f.h.Uploads().Append(t.Context(), f.rq, formats.SessionRef{ID: uuid.New(), Binding: "app"}, 0, strings.NewReader("x"))
	if err != nil || got != 4096 {
		t.Fatalf("received %d, err %v", got, err)
	}
}

func TestR_STOR_011_StartChecksTheTargetAndDecidesDeploy(t *testing.T) {
	target := localRepo()
	t.Run("a local repository is its own target", func(t *testing.T) {
		f := newSessionFixture(t, target)
		f.svc.session = uploads.Session{ID: uuid.New(), Binding: "app", PartSize: 16 << 20}
		got, err := f.h.Uploads().Start(t.Context(), f.rq, f.rq.RepoRef(), "app")
		if err != nil {
			t.Fatal(err)
		}
		if got.Target.ID != 42 || got.Binding != "app" || got.PartSize != 16<<20 {
			t.Fatalf("session = %+v", got)
		}
		if f.svc.scope.Principal != f.user {
			t.Fatalf("scope = %+v", f.svc.scope)
		}
	})
	t.Run("another repository as the target is refused", func(t *testing.T) {
		f := newSessionFixture(t, target)
		_, err := f.h.Uploads().Start(t.Context(), f.rq, formats.RepoRef{ID: 43, Name: target.Name}, "app")
		if errs.KindOf(err) != errs.Internal || len(f.svc.calls) != 0 {
			t.Fatalf("err = %v, calls %v", err, f.svc.calls)
		}
	})
	t.Run("a denied deploy is the decision's error and starts nothing", func(t *testing.T) {
		f := newSessionFixture(t, target)
		f.d.decision = authz.Decision{Outcome: authz.DenyVisible}
		_, err := f.h.Uploads().Start(t.Context(), f.rq, f.rq.RepoRef(), "app")
		if errs.KindOf(err) != errs.Forbidden || len(f.svc.calls) != 0 {
			t.Fatalf("err = %v, calls %v", err, f.svc.calls)
		}
	})
	t.Run("a remote repository takes no upload", func(t *testing.T) {
		f := newSessionFixture(t, vt.RemoteRepo(5, "hub"))
		_, err := f.h.Uploads().Start(t.Context(), f.rq, f.rq.RepoRef(), "app")
		if errs.KindOf(err) != errs.Internal || len(f.svc.calls) != 0 {
			t.Fatalf("err = %v, calls %v", err, f.svc.calls)
		}
	})
}

func TestR_OCI_028_AVirtualRepositoryUploadsToItsDeploymentTarget(t *testing.T) {
	local := vt.LocalRepo(42, "docker-local")
	virt := vt.VirtualRepo(7, "docker", 60, &local, local)
	f := newSessionFixture(t, virt)
	f.svc.session = uploads.Session{ID: uuid.New(), Binding: "app"}
	got, err := f.h.Uploads().Start(t.Context(), f.rq, formats.RepoRef{ID: 42, Name: local.Name, Class: formats.Local, Format: formats.ID(local.Format)}, "app")
	if err != nil {
		t.Fatal(err)
	}
	if got.Target.ID != 42 {
		t.Fatalf("target = %+v", got.Target)
	}
	if f.svc.scope.Addressed != 7 {
		t.Fatalf("the session records the addressed repository %d, want 7", f.svc.scope.Addressed)
	}
	if f.d.got.Repository != local.Name || f.d.got.Addressed != local.Name {
		t.Fatalf("deploy is decided on the target itself: %+v", f.d.got)
	}
	if len(f.d.known) != 1 || f.d.known[0] != local.Name {
		t.Fatalf("the index learned %v", f.d.known)
	}

	t.Run("a virtual repository without a deployment target takes none", func(t *testing.T) {
		f := newSessionFixture(t, vt.VirtualRepo(8, "docker2", 60, nil, local))
		_, err := f.h.Uploads().Start(t.Context(), f.rq, formats.RepoRef{ID: 42, Name: local.Name}, "app")
		if errs.KindOf(err) != errs.Internal {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a session whose deployment target changed is gone", func(t *testing.T) {
		f := newSessionFixture(t, virt)
		f.svc.session = uploads.Session{Target: 99}
		if _, err := f.h.Uploads().Get(t.Context(), f.rq, formats.SessionRef{ID: uuid.New(), Binding: "app"}); errs.KindOf(err) != errs.NotFound {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestR_STOR_011_FinalizeCommitsTheStagedBlobInTheTransactionThatEndsTheSession(t *testing.T) {
	f := newSessionFixture(t, localRepo())
	f.svc.session = uploads.Session{Target: 42, Binding: "app"}
	var doneIn meta.Tx
	ended := false
	var endedItem int64
	sum := [32]byte{1, 2, 3}
	f.svc.finalize = func(commit uploads.CommitFunc) (uploads.Result, error) {
		staged := blob.Staged(blob.Digests{SHA256: blob.Digest(sum), Size: 11}, "p/_uploads/x/data")
		return uploads.Result{}, commit(t.Context(), staged, func(_ context.Context, tx meta.Tx, item int64) error {
			ended, doneIn, endedItem = true, tx, item
			return nil
		})
	}
	path, err := repository.CleanPath("app/_blobs/sha256:00")
	if err != nil {
		t.Fatal(err)
	}
	c := formats.Commit{Path: path, CreateOnly: true}
	got, err := f.h.Uploads().Finalize(t.Context(), f.rq, formats.SessionRef{ID: uuid.New(), Binding: "app"}, sum, nil, c)
	if err != nil {
		t.Fatal(err)
	}
	if endedItem != 9 { // the id of the item the commit made, recorded with the end of the session
		t.Fatalf("the session recorded item %d", endedItem)
	}
	if !f.it.ran || !ended || doneIn != nil {
		t.Fatalf("commit ran %v, session ended in its transaction %v", f.it.ran, ended)
	}
	if f.it.spec.Spooled == nil || f.it.spec.Spooled.StagedKey() != "p/_uploads/x/data" || f.it.spec.Origin != items.OriginUpload {
		t.Fatalf("spec = %+v", f.it.spec)
	}
	if string(f.it.spec.Declared.SHA256) != string(sum[:]) || !f.it.spec.CreateOnly {
		t.Fatalf("declared = %+v", f.it.spec.Declared)
	}
	if got.Item.ID != 9 || !got.Created || got.Item.Repo.ID != 42 {
		t.Fatalf("committed = %+v", got)
	}
	// the decision that allowed the write is recorded in that transaction
	if len(f.d.recorded) != 2 || f.d.recorded[0].Package != "app" || f.d.recorded[1].Path != path.Path { // the binding the session was started for, and the path the commit writes
		t.Fatalf("recorded = %+v", f.d.recorded)
	}
}

func TestR_STOR_011_ARepeatedClosingRequestAnswersFromTheRecordWithoutACommit(t *testing.T) {
	f := newSessionFixture(t, localRepo())
	f.svc.session = uploads.Session{Target: 42, Binding: "app"}
	f.svc.replayed = true
	path, err := repository.CleanPath("app/_blobs/sha256:00")
	if err != nil {
		t.Fatal(err)
	}
	got, err := f.h.Uploads().Finalize(t.Context(), f.rq, formats.SessionRef{ID: uuid.New(), Binding: "app"}, [32]byte{1}, nil, formats.Commit{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if f.it.ran || got.Item.ID != 5 || !got.Identical || got.Item.Path != path.Path {
		t.Fatalf("committed %+v, commit ran %v", got, f.it.ran)
	}
}

func TestR_STOR_011_FinalizeDecidesDeployOnThePathItCommits(t *testing.T) {
	f := newSessionFixture(t, localRepo())
	f.svc.session = uploads.Session{Target: 42, Binding: "app"}
	path, err := repository.CleanPath("other/_blobs/sha256:00")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.h.Uploads().Finalize(t.Context(), f.rq, formats.SessionRef{ID: uuid.New(), Binding: "app"}, [32]byte{1}, nil, formats.Commit{Path: path}); err != nil {
		t.Fatal(err)
	}
	if f.d.got.Path != path.Path || f.d.got.Package != "app" {
		t.Fatalf("the last decision was on %+v", f.d.got)
	}
}

func TestR_STOR_011_ACallerThatIsNoLongerAuthenticatedFindsNoSession(t *testing.T) {
	f := newSessionFixture(t, localRepo())
	f.svc.session = uploads.Session{Target: 42, Binding: "app"}
	f.d.decision = authz.Decision{Outcome: authz.DenyUnauthenticated}
	_, err := f.h.Uploads().Get(t.Context(), f.rq, formats.SessionRef{ID: uuid.New(), Binding: "app"})
	var e *errs.Error
	if !errors.As(err, &e) || e.Kind != errs.NotFound || e.Code != errs.CodeOCIBlobUploadUnknown {
		t.Fatalf("err = %v", err)
	}
}
