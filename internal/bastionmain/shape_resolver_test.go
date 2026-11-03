package bastionmain

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/bastion/iface"
)

type fakeReacher struct {
	out map[string]authz.ReachResult
	err error
}

func (f fakeReacher) ReachOn(context.Context, *authz.Index, authz.Subject, []string) (map[string]authz.ReachResult, error) {
	return f.out, f.err
}

// TestR_BAS_012_ShapeResolverMapsReachToTheWire: full and partial carry no status, none carries the status a
// refused read has (R-AUTHZ-016): 401 for an anonymous subject, 403 for a visible refusal, 404 for a hidden or
// missing repository.
func TestR_BAS_012_ShapeResolverMapsReachToTheWire(t *testing.T) {
	r := NewShapeResolver(fakeReacher{out: map[string]authz.ReachResult{
		"a": {Reach: authz.ReachFull},
		"b": {Reach: authz.ReachPartial},
		"c": {Reach: authz.ReachNone, Denial: authz.DenyUnauthenticated},
		"d": {Reach: authz.ReachNone, Denial: authz.DenyVisible},
		"e": {Reach: authz.ReachNone, Denial: authz.DenyHidden},
		"f": {Reach: authz.ReachNone},
	}})
	got, err := r.Shapes(t.Context(), nil, authz.Subject{}, []string{"a", "b", "c", "d", "e", "f"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]Shape{
		"a": {Read: iface.ReadFull},
		"b": {Read: iface.ReadPartial},
		"c": {Read: iface.ReadNone, Status: http.StatusUnauthorized},
		"d": {Read: iface.ReadNone, Status: http.StatusForbidden},
		"e": {Read: iface.ReadNone, Status: http.StatusNotFound},
		"f": {Read: iface.ReadNone, Status: http.StatusNotFound},
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s: %+v, want %+v", k, got[k], w)
		}
	}
	t.Run("a refusal to decide is the error of the resolver", func(t *testing.T) {
		boom := errors.New("stale")
		if _, err := NewShapeResolver(fakeReacher{err: boom}).Shapes(t.Context(), nil, authz.Subject{}, []string{"a"}); !errors.Is(err, boom) {
			t.Fatalf("%v", err)
		}
	})
}

// TestR_BAS_013_AbsenceIsAbsentOnlyWhenSomethingWasMissingAndNothingWasUnknown: the record of the lookups of one
// request, and the notes on a request that has none.
func TestR_BAS_013_AbsenceIsAbsentOnlyWhenSomethingWasMissingAndNothingWasUnknown(t *testing.T) {
	ctx, a := WithAbsence(t.Context())
	if a.Absent() {
		t.Fatal("nothing noted is not absent")
	}
	NoteUnknown(ctx)
	if a.Absent() {
		t.Fatal("unknown alone is not absent")
	}
	ctx, a = WithAbsence(t.Context())
	NoteMissing(ctx)
	if !a.Absent() {
		t.Fatal("a missing item is absent")
	}
	NoteUnknown(ctx)
	if a.Absent() {
		t.Fatal("a refusal after a missing lookup makes the 404 mean less")
	}
	var nilAbsence *Absence
	if nilAbsence.Absent() {
		t.Fatal("no record is not absent")
	}
	NoteMissing(t.Context()) // no record on the context: a no-op
	NoteUnknown(t.Context())
}
