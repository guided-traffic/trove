package files

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/qname"
	"github.com/guided-traffic/trove/internal/repository"
)

// jobDecider allows every path but those in deny and records the subjects it was asked for; bypass is whether the subject holds
// content:bypass.
type jobDecider struct {
	deny   map[string]bool
	bypass bool
	stale  bool
	begun  []authz.Subject
	named  []qname.Repo
	asked  []authz.Subject
	bases  []authz.Coordinate
	failOn error
}

func (d *jobDecider) Begin(_ context.Context, sub authz.Subject, names ...qname.Repo) (*authz.Index, error) {
	d.begun = append(d.begun, sub)
	d.named = append(d.named, names...)
	return nil, d.failOn
}

func (d *jobDecider) PathsAllowedOn(_ context.Context, _ *authz.Index, sub authz.Subject, base authz.Coordinate, _ authz.Action, paths []string, _ authz.Classifier) ([]bool, error) {
	d.asked = append(d.asked, sub)
	d.bases = append(d.bases, base)
	out := make([]bool, len(paths))
	for i, p := range paths {
		out[i] = !d.deny[p]
	}
	return out, nil
}

func (d *jobDecider) HoldsOn(_ *authz.Index, _ authz.Subject, permission string) bool {
	return d.bypass && permission == string(authz.ContentBypass)
}

// RepositoryIDOn answers the IDs of jobService's repositories under their names; with stale set, the index still holds another
// repository under the moved one's name.
func (d *jobDecider) RepositoryIDOn(_ *authz.Index, name qname.Repo) (int64, bool) {
	if d.stale {
		return 42, true
	}
	id, ok := indexed[name]
	return id, ok
}

// indexed are the IDs the index holds for the names of jobService's repositories.
var indexed = map[qname.Repo]int64{
	{Project: "other", Key: "generic-local"}: 5, qr("deleting"): 8, qr("disabled"): 9, qr("pkg-local"): 7,
}

type standings struct {
	now   authz.Standing
	err   error
	asked []authz.JobSubject
}

func (s *standings) Standing(_ context.Context, j authz.JobSubject) (authz.Standing, error) {
	s.asked = append(s.asked, j)
	return s.now, s.err
}

// jobService is a generic layer over repositories the delete job meets: the repository of ID 5 now lives under another name (it was
// moved), 8 is being deleted, 9 is disabled, 7 is a package format behind the seam.
func jobService(t *testing.T, seam *packageSeam) *Service {
	t.Helper()
	o := Options{Items: &store{}, Repos: repos{
		"moved":    {ID: "5", Name: qname.Repo{Project: "other", Key: "generic-local"}, Class: repository.Local, Format: "generic"},
		"deleting": {ID: "8", Name: qr("deleting"), Class: repository.Local, Format: "generic", Lifecycle: repository.Deleting},
		"disabled": {ID: "9", Name: qr("disabled"), Class: repository.Local, Format: "generic", Disabled: true},
		"pkg":      {ID: "7", Name: qr("pkg-local"), Class: repository.Local, Format: "pkgfmt"},
	}}
	if seam != nil {
		o.Formats = seam
	}
	svc, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func TestDeleteJobGuard(t *testing.T) {
	ctx := context.Background()
	j := authz.JobSubject{Kind: principal.KindUser, ID: "u-1", Name: "deleter", TokenID: "tok-1", Endpoint: "partners"}
	raw, err := json.Marshal(j)
	if err != nil {
		t.Fatal(err)
	}
	args := func(id int64, key string) items.DeleteArgs {
		return items.DeleteArgs{RepoID: id, Key: qr(key), Path: "tree", Subject: raw, MaxID: 100}
	}
	devs := []principal.GroupRef{{ID: "g-1", Name: "developers"}}
	current := authz.Standing{Kind: principal.KindUser, Name: "deleter-renamed", Groups: devs}

	// R-AUTHZ-002, R-EP-013: the groups and the name are those of the standing, the endpoint is the request's.
	t.Run("R-STOR-018 every batch decides on the standing it read, with the endpoint of the request", func(t *testing.T) {
		d, st := &jobDecider{deny: map[string]bool{"tree/b": true}}, &standings{now: current}
		g, err := jobService(t, nil).DeleteJobGuard(d, st)(ctx, args(5, "generic-local"))
		if err != nil {
			t.Fatal(err)
		}
		if err := g.Check(ctx, []string{"tree/a"}); err != nil {
			t.Fatalf("an allowed path: %v", err)
		}
		if len(st.asked) != 1 || st.asked[0] != j {
			t.Fatalf("the standing was read %d times for %+v", len(st.asked), st.asked)
		}
		for _, sub := range append(d.begun, d.asked...) {
			if sub.ID != "u-1" || sub.Name != "deleter-renamed" || sub.Endpoint != "partners" || len(sub.Groups) != 1 || sub.Groups[0].ID != "g-1" {
				t.Fatalf("decided for %+v", sub)
			}
		}
		if err := g.Removal(ctx, nil, []string{"tree/a"}); err != nil {
			t.Fatalf("a layer without a format seam asks nothing: %v", err)
		}
	})

	// R-AUTHZ-016: the refused item may be one the subject cannot read, and others read the job's error.
	t.Run("R-STOR-018 a refusal names the path the request named, never the refused item", func(t *testing.T) {
		g, err := jobService(t, nil).DeleteJobGuard(&jobDecider{deny: map[string]bool{"tree/secret/b": true}}, &standings{now: current})(ctx, args(5, "generic-local"))
		if err != nil {
			t.Fatal(err)
		}
		err = g.Check(ctx, []string{"tree/a", "tree/secret/b"})
		if errs.KindOf(err) != errs.Forbidden || !strings.Contains(err.Error(), "other/generic-local/tree.") || strings.Contains(err.Error(), "secret") {
			t.Fatalf("want the 403 of the root path: %v", err)
		}
	})

	t.Run("R-STOR-018 the repository is found by its ID: a moved repository is decided, and named, where it is now", func(t *testing.T) {
		d := &jobDecider{}
		g, err := jobService(t, nil).DeleteJobGuard(d, &standings{now: current})(ctx, args(5, "generic-local"))
		if err != nil {
			t.Fatal(err)
		}
		if err := g.Check(ctx, []string{"tree/a"}); err != nil {
			t.Fatal(err)
		}
		moved := qname.Repo{Project: "other", Key: "generic-local"}
		if g.Name != moved || len(d.named) != 1 || d.named[0] != moved || d.bases[0].Repository != moved {
			t.Fatalf("decided on %v, begun on %v, named %v; want %v", d.bases, d.named, g.Name, moved)
		}
	})

	// Q-STOR-191: the snapshot and the index can disagree for a moment after a move.
	t.Run("R-STOR-018 an index that holds another repository under the current name makes the batch wait for the next attempt", func(t *testing.T) {
		_, err := jobService(t, nil).DeleteJobGuard(&jobDecider{stale: true}, &standings{now: current})(ctx, args(5, "generic-local"))
		if errs.KindOf(err) != errs.Unavailable {
			t.Fatalf("want a retry, got %v", err)
		}
	})

	t.Run("R-STOR-018 a repository that is gone or being deleted ends the job as done, before the standing is read", func(t *testing.T) {
		for _, id := range []int64{99, 8} {
			st := &standings{now: current}
			_, err := jobService(t, nil).DeleteJobGuard(&jobDecider{}, st)(ctx, args(id, "x"))
			if !errors.Is(err, items.ErrRepositoryGone) || len(st.asked) != 0 {
				t.Fatalf("repository %d: %v, standing read %d times", id, err, len(st.asked))
			}
		}
	})

	// Q-AUTHZ-162: only content:bypass deletes in a disabled repository.
	t.Run("R-STOR-018 a disabled repository stops the job unless the subject holds content:bypass", func(t *testing.T) {
		_, err := jobService(t, nil).DeleteJobGuard(&jobDecider{}, &standings{now: current})(ctx, args(9, "disabled"))
		if errs.KindOf(err) != errs.Forbidden || !strings.Contains(err.Error(), "disabled") {
			t.Fatalf("without content:bypass: %v", err)
		}
		if _, err := jobService(t, nil).DeleteJobGuard(&jobDecider{bypass: true}, &standings{now: current})(ctx, args(9, "disabled")); err != nil {
			t.Fatalf("with content:bypass: %v", err)
		}
	})

	// R-AUTHZ-018: the batch asks the format about its files on the batch's index and subject.
	t.Run("R-STOR-018 the format is asked about the files of the batch, for the subject of the batch", func(t *testing.T) {
		seam := &packageSeam{refuse: errs.E(errs.Forbidden, "package_owned_by_other_owner", "owned by another owner")}
		g, err := jobService(t, seam).DeleteJobGuard(&jobDecider{}, &standings{now: current})(ctx, args(7, "pkg-local"))
		if err != nil {
			t.Fatal(err)
		}
		if err := g.Removal(ctx, nil, []string{"ui/-/ui-1.0.0.tgz"}); errs.KindOf(err) != errs.Forbidden {
			t.Fatalf("the seam's refusal: %v", err)
		}
		if len(seam.removals) != 1 || seam.removals[0].Subject.Name != "deleter-renamed" || seam.removals[0].Repo.Name != qr("pkg-local") ||
			len(seam.removals[0].Files) != 1 {
			t.Fatalf("removals = %+v", seam.removals)
		}
	})

	// R-AUTH-023: a withdrawal is seen before anything is decided.
	t.Run("R-STOR-018 a withdrawn subject fails the batch before anything is decided", func(t *testing.T) {
		d := &jobDecider{}
		gone := errs.E(errs.Unauthenticated, errs.CodeInvalidToken, "The token that asked for this delete was revoked; the job stopped.")
		_, err := jobService(t, nil).DeleteJobGuard(d, &standings{err: gone})(ctx, args(5, "generic-local"))
		if errs.KindOf(err) != errs.Unauthenticated || len(d.begun)+len(d.asked) != 0 {
			t.Fatalf("err = %v, decider asked %d times", err, len(d.begun)+len(d.asked))
		}
	})

	t.Run("R-STOR-018 a job whose subject does not decode is refused", func(t *testing.T) {
		bad := args(5, "generic-local")
		bad.Subject = json.RawMessage(`[1]`)
		if _, err := jobService(t, nil).DeleteJobGuard(&jobDecider{}, &standings{})(ctx, bad); errs.KindOf(err) != errs.Forbidden {
			t.Fatalf("err = %v", err)
		}
	})
}
