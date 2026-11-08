package nuget

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/journal"
)

func TestR_NUGET_033_TheHandlerIsRegisteredForBothEvents(t *testing.T) {
	for _, ev := range []string{audit.EventPackagePublished, audit.EventPackageDeleted} {
		if h, ok := journal.HandlerFor(ev); !ok || h.Domain != "nuget" {
			t.Errorf("%s: handler = %+v, %v", ev, h, ok)
		}
	}
	// An unlist or a relist reserves and withdraws nothing (R-NUGET-033).
	if _, ok := journal.HandlerFor(audit.EventPackageMetadataChanged); ok {
		t.Error("package.metadata_changed has a replay handler")
	}
}

// An entry that is not complete names nothing; the replay skips it before it reads the database.
func TestR_NUGET_033_AnIncompleteEntryIsSkipped(t *testing.T) {
	sum := strings.Repeat("ab", 64)
	good := map[string]any{"repository": 7, "id": "acme.widgets", "version": "1.2.3", "sha512": sum}
	with := func(k string, v any) map[string]any {
		m := map[string]any{}
		for kk, vv := range good {
			m[kk] = vv
		}
		m[k] = v
		return m
	}
	for name, entry := range map[string]any{
		"no repository": with("repository", 0),
		"no id":         with("id", ""),
		"long id":       with("id", strings.Repeat("a", maxEntryText+1)),
		"no version":    with("version", ""),
		"no sum":        with("sha512", ""),
		"short sum":     with("sha512", sum[:126]),
		"upper sum":     with("sha512", strings.ToUpper(sum)),
		"not hex":       with("sha512", strings.Repeat("zz", 64)),
		"not an object": []int{1},
		"wrong type":    with("repository", "seven"),
	} {
		for _, ev := range []string{audit.EventPackagePublished, audit.EventPackageDeleted} {
			t.Run(name+" "+ev, func(t *testing.T) {
				raw, err := json.Marshal(entry)
				if err != nil {
					t.Fatal(err)
				}
				res, err := replayPackage(context.Background(), nil, journal.Env{Now: time.Now()},
					journal.Object{V: 1, ID: "0197c2a4-1111-7000-8000-000000000001", At: time.Now(), Event: ev, Entry: raw})
				if err != nil || res.Outcome != journal.Skipped {
					t.Fatalf("result %+v, error %v", res, err)
				}
			})
		}
	}
}

// The end state of a number that the restored database does not hold must not depend on the order of the entries about it,
// because that order comes from times the writer of an entry chooses (R-NUGET-033).
func TestR_NUGET_033_TheEndStateOfANumberDoesNotDependOnTheOrderOfItsEntries(t *testing.T) {
	a, b := []byte{0xaa}, []byte{0xbb}
	type entry struct {
		published bool
		sum       []byte
	}
	apply := func(s numberState, e entry) (numberState, bool) {
		st, warn := absentStep(e.published, s, e.sum)
		switch st {
		case stepReserve:
			return numberState{Kind: kindReserved, Sha512: e.sum}, warn
		case stepRemove:
			return numberState{Kind: kindRemoved, Sha512: e.sum}, warn
		case stepPromote:
			return numberState{Kind: kindRemoved, Sha512: s.Sha512}, warn
		}
		return s, warn
	}
	same := func(x, y numberState) bool {
		return x.Kind == y.Kind && (x.Kind != kindReserved || bytes.Equal(x.Sha512, y.Sha512))
	}
	pool := []entry{{true, a}, {true, b}, {false, a}, {false, b}}
	starts := map[string]numberState{
		"none": {}, "reserved(A)": {kindReserved, a}, "reserved(B)": {kindReserved, b}, "removed(A)": {kindRemoved, a},
	}
	sets := [][]entry{nil}
	for _, x := range pool {
		sets = append(sets, []entry{x})
		for _, y := range pool {
			sets = append(sets, []entry{x, y})
			for _, z := range pool {
				sets = append(sets, []entry{x, y, z})
			}
		}
	}
	for name, start := range starts {
		for _, es := range sets {
			want := numberState{Kind: kindRemoved}
			switch {
			case len(es) == 0:
				want = start
			case start.Kind == kindNone || start.Kind == kindReserved:
				d := es[0].sum
				if start.Kind == kindReserved {
					d = start.Sha512
				}
				all := true
				for _, e := range es {
					all = all && e.published && bytes.Equal(e.sum, d)
				}
				if all {
					want = numberState{Kind: kindReserved, Sha512: d}
				}
			}
			for _, p := range permutations(es) {
				s := start
				for _, e := range p {
					before := s
					var warn bool
					s, warn = apply(s, e)
					wantWarn := before.Kind == kindReserved && s.Kind == kindRemoved && !bytes.Equal(before.Sha512, e.sum)
					if warn != wantWarn {
						t.Errorf("%s %v: warning %v, want %v: it belongs to a reservation turned into a removal by an entry of other bytes", name, p, warn, wantWarn)
					}
				}
				if !same(s, want) {
					t.Fatalf("%s %v: end state %+v, want %+v", name, p, s, want)
				}
			}
		}
	}
}

func permutations[T any](xs []T) [][]T {
	if len(xs) <= 1 {
		return [][]T{append([]T(nil), xs...)}
	}
	var out [][]T
	for i := range xs {
		rest := append(append([]T(nil), xs[:i]...), xs[i+1:]...)
		for _, p := range permutations(rest) {
			out = append(out, append([]T{xs[i]}, p...))
		}
	}
	return out
}
