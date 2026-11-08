package formathost

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/guided-traffic/trove/internal/ci/claims"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/internal/uploads"
)

// classFormat names the first segment of a path as its package, the way the OCI format names the image.
type classFormat struct{ portFormat }

func (classFormat) Classify(p formats.Path) formats.PathInfo {
	pkg, _, _ := strings.Cut(p.Path, "/")
	return formats.PathInfo{Kind: formats.Content, Package: pkg}
}

func TestR_STOR_011_ASessionOfACIJobIsKeyedOnItsToken(t *testing.T) {
	f := newSessionFixture(t, localRepo())
	f.rq.Subject.CI = &claims.Frozen{}
	f.rq.Subject.TokenID = uuid.NewString()
	f.svc.session = uploads.Session{Target: 42, Binding: "app"}
	if _, err := f.h.Uploads().Get(t.Context(), f.rq, formats.SessionRef{ID: uuid.New(), Binding: "app"}); err != nil {
		t.Fatal(err)
	}
	if f.svc.scope.Principal != f.rq.Subject.TokenID {
		t.Fatalf("principal = %q, want the token %q", f.svc.scope.Principal, f.rq.Subject.TokenID)
	}
	f.rq.Subject.CI = nil
	if _, err := f.h.Uploads().Get(t.Context(), f.rq, formats.SessionRef{ID: uuid.New(), Binding: "app"}); err != nil {
		t.Fatal(err)
	}
	if f.svc.scope.Principal != f.user {
		t.Fatalf("principal = %q, want the subject %q", f.svc.scope.Principal, f.user)
	}
}

func TestR_STOR_011_AFinalizeCommitsOnlyAPathOfThePackageTheSessionWasStartedFor(t *testing.T) {
	withFormat := func(o *Options) {
		o.Formats = func(string) (formats.Format, bool) { return classFormat{portFormat{id: "pkgfmt"}}, true }
	}
	commit := func(t *testing.T, name string) (*sessionFixture, error) {
		f := newSessionFixture(t, localRepo(), withFormat)
		f.svc.session = uploads.Session{Target: 42, Binding: "app"}
		path, err := repository.CleanPath(name)
		if err != nil {
			t.Fatal(err)
		}
		_, err = f.h.Uploads().Finalize(t.Context(), f.rq, formats.SessionRef{ID: uuid.New(), Binding: "app"}, [32]byte{1}, nil, formats.Commit{Path: path})
		return f, err
	}
	t.Run("a path of the package is decided on that package", func(t *testing.T) {
		f, err := commit(t, "app/_blobs/sha256:00")
		if err != nil {
			t.Fatal(err)
		}
		if f.d.got.Package != "app" || f.d.got.Path != "app/_blobs/sha256:00" {
			t.Fatalf("decided on %+v", f.d.got)
		}
	})
	t.Run("a path of another package is refused before anything is committed", func(t *testing.T) {
		f, err := commit(t, "other/_blobs/sha256:00")
		if errs.KindOf(err) != errs.Internal || f.it.ran {
			t.Fatalf("err = %v, commit ran %v", err, f.it.ran)
		}
	})
}
