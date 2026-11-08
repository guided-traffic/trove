package items

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/meta"
)

func TestBatchGuardOfTheDeleteJob(t *testing.T) {
	ctx := context.Background()
	subject := json.RawMessage(`{"kind":"user","id":"u-1","tokenId":"t-1"}`)
	job := DeleteArgs{RepoID: 5, Path: "tree", MaxID: 10, Subject: subject}
	guarded := func(g JobGuard) *Store {
		s := &Store{}
		s.SetJobGuard(g)
		return s
	}
	allow := func(context.Context, []string) error { return nil }

	// Q-STOR-192: a job is never run unguarded.
	t.Run("R-STOR-018 a job on a replica without a guard, or without a subject, fails without a further attempt", func(t *testing.T) {
		if _, err := (&Store{}).batchGuard(ctx, job); !jobs.IsPermanent(err) {
			t.Fatalf("no guard: %v", err)
		}
		bare := job
		bare.Subject = nil
		if _, err := guarded(func(context.Context, DeleteArgs) (BatchGuard, error) { return BatchGuard{Check: allow}, nil }).batchGuard(ctx, bare); !jobs.IsPermanent(err) {
			t.Fatalf("no subject: %v", err)
		}
	})

	t.Run("R-STOR-018 the erasure of a cache, which no request asks for, is not guarded", func(t *testing.T) {
		g, err := (&Store{}).batchGuard(ctx, DeleteArgs{RepoID: 5, all: true})
		if err != nil || g != nil {
			t.Fatalf("guard %v, %v", g, err)
		}
	})

	t.Run("R-STOR-018 a guard that decides nothing fails the job", func(t *testing.T) {
		s := guarded(func(context.Context, DeleteArgs) (BatchGuard, error) { return BatchGuard{}, nil })
		if _, err := s.batchGuard(ctx, job); !jobs.IsPermanent(err) {
			t.Fatalf("err = %v", err)
		}
	})

	// Q-STOR-191: a repository that is gone ends the job as done, which RunDeleteJob tells by the sentinel.
	t.Run("R-STOR-018 the guard's answers are classified: a refusal and a withdrawal end the job, a gone repository is passed on, the rest is retried", func(t *testing.T) {
		for _, tc := range []struct {
			name      string
			err       error
			permanent bool
			gone      bool
		}{
			{"refused", errs.E(errs.Forbidden, errs.CodeForbidden, "no"), true, false},
			{"withdrawn", errs.E(errs.Unauthenticated, errs.CodeInvalidToken, "revoked"), true, false},
			{"gone", ErrRepositoryGone, false, true},
			{"unavailable", errs.E(errs.Unavailable, errs.CodeDatabaseUnavailable, "down"), false, false},
			{"plain", errors.New("connection reset"), false, false},
		} {
			s := guarded(func(context.Context, DeleteArgs) (BatchGuard, error) { return BatchGuard{}, tc.err })
			_, err := s.batchGuard(ctx, job)
			if jobs.IsPermanent(err) != tc.permanent || errors.Is(err, ErrRepositoryGone) != tc.gone {
				t.Errorf("%s: %v (permanent %v)", tc.name, err, jobs.IsPermanent(err))
			}
		}
	})

	t.Run("R-STOR-018 a batch decides delete on every listed item and asks the format about its files only", func(t *testing.T) {
		var checked, removed []string
		g := &BatchGuard{
			Check:   func(_ context.Context, p []string) error { checked = append(checked, p...); return nil },
			Removal: func(_ context.Context, _ meta.Tx, f []string) error { removed = append(removed, f...); return nil },
		}
		rows := []subtreeRow{{Path: "tree", Kind: KindFolder}, {Path: "tree/a", Kind: KindFile}}
		if err := g.files(ctx, nil, rows); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(checked, []string{"tree", "tree/a"}) || !slices.Equal(removed, []string{"tree/a"}) {
			t.Fatalf("checked %v, asked the format about %v", checked, removed)
		}
	})

	// R-AUTHZ-018: an ownership refusal of a batch ends the job like a refused path.
	t.Run("R-STOR-018 a refusal of the check or of the format ends the job", func(t *testing.T) {
		no := errs.E(errs.Forbidden, "package_owned_by_other_owner", "owned")
		rows := []subtreeRow{{Path: "tree/a", Kind: KindFile}}
		byCheck := &BatchGuard{Check: func(context.Context, []string) error { return no }}
		byFormat := &BatchGuard{Check: allow, Removal: func(context.Context, meta.Tx, []string) error { return no }}
		for name, g := range map[string]*BatchGuard{"check": byCheck, "format": byFormat} {
			if err := g.files(ctx, nil, rows); !jobs.IsPermanent(err) {
				t.Errorf("%s: %v", name, err)
			}
		}
	})

	// Q-STOR-193: a database's text never reaches the job's error.
	t.Run("R-STOR-018 every error but a refusal and the job's own permanent ones is reported with a generic text", func(t *testing.T) {
		raw := errors.New(`ERROR: relation "items" does not exist (SQLSTATE 42P01)`)
		got := jobFailure(ctx, raw)
		e, ok := errs.As(got)
		if !ok || e.Kind != errs.Unavailable || strings.Contains(e.Detail, "SQLSTATE") || !errors.Is(got, raw) {
			t.Fatalf("raw: %v", got)
		}
		written := errs.E(errs.Unavailable, errs.CodeDatabaseUnavailable, "The principal cannot be resolved right now.", errs.WithCause(raw))
		for _, err := range []error{written, fmt.Errorf("batch: %w", written)} {
			got := jobFailure(ctx, err)
			if e, ok := errs.As(got); !ok || e.Code != errs.CodeUnavailable || !errors.Is(got, raw) {
				t.Errorf("%v became %v, want the generic error", err, got)
			}
		}
		refusal := jobs.Permanent(errs.E(errs.Forbidden, errs.CodeForbidden, "no"))
		for _, err := range []error{nil, refusal} {
			if got := jobFailure(ctx, err); got != err { //nolint:errorlint // the same value, unchanged
				t.Errorf("%v became %v", err, got)
			}
		}
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		if got := jobFailure(cancelled, raw); got != raw { //nolint:errorlint // the same value, unchanged
			t.Errorf("a cancelled job's error became %v", got)
		}
	})
}
