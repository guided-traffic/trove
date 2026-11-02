package nuget

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/formattest"
	"github.com/guided-traffic/trove/internal/formats/nuget/nugettest"
	"github.com/guided-traffic/trove/internal/formats/nuget/rows"
)

// streamStore is a store that offers the scan as one statement (Scanner): it hands n packages to
// the visitor in batches, and with retry it starts over once after the first batch, as a
// transaction retried after a failover does. A Search with PerVersion fails the test: the scan
// must not page by offset when the store can stream it.
type streamStore struct {
	*nugettest.Store
	t      *testing.T
	n      int
	retry  bool
	starts int
}

func (s *streamStore) Search(ctx context.Context, q rows.SearchQuery) ([]rows.SearchHit, error) {
	if q.PerVersion {
		s.t.Errorf("the scan paged by offset (Offset %d) although the store streams it", q.Offset)
		return nil, nil
	}
	var out []rows.SearchHit
	for _, ref := range q.Only {
		out = append(out, rows.SearchHit{LowerID: ref.LowerID, DisplayName: ref.LowerID, Listed: []rows.VersionEntry{ver(ref.LowerVersion, false)},
			Latest: rows.RegistrationVersion{VersionEntry: ver(ref.LowerVersion, false)}})
	}
	return out, nil
}

func (s *streamStore) SearchScan(_ context.Context, _ rows.SearchQuery, batch int, start func(), visit func([]rows.SearchHit) (bool, error)) error {
	attempt := func(stopAfter int) error {
		s.starts++
		start()
		var buf []rows.SearchHit
		sent := 0
		for i := range s.n {
			buf = append(buf, rows.SearchHit{LowerID: fmt.Sprintf("p%06d", i), Listed: []rows.VersionEntry{ver("1.0.0", false)},
				ListedTypes: [][]string{{"dependency"}}, ListedMatch: []bool{true}})
			if len(buf) == batch {
				if more, err := visit(buf); err != nil || !more {
					return err
				}
				buf, sent = nil, sent+1
				if stopAfter > 0 && sent == stopAfter {
					return nil
				}
			}
		}
		if len(buf) > 0 {
			_, err := visit(buf)
			return err
		}
		return nil
	}
	if s.retry {
		if err := attempt(1); err != nil {
			return err
		}
	}
	return attempt(0)
}

// TestR_NUGET_034_TheScanOfARowLevelReaderIsReadAsOneStatement (Q-NUGET-74): a store that offers
// the scan as one statement in one transaction is read through it, not in batches paged by
// offset, each of which ranked the whole repository again. A transaction that starts over counts
// its packages once and gives the budget back what the abandoned attempt spent, so a retry
// neither doubles totalHits nor turns a scan within the budget into a 400.
func TestR_NUGET_034_TheScanOfARowLevelReaderIsReadAsOneStatement(t *testing.T) {
	for _, c := range []struct {
		name  string
		n     int
		retry bool
		want  int
	}{
		{"one attempt", 1_234, false, 1_234},
		{"a retried attempt", 1_234, true, 1_234},
		{"a retried attempt near the budget", maxSearchScan - 10, true, maxSearchScan - 10},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := &streamStore{Store: nugettest.New(), t: t, n: c.n, retry: c.retry}
			h := newHost(t, formattest.HostOptions{Data: st, Resolver: resolverFake{scope: scopeFake{shape: formats.ReadRows}}})
			r := doSearch(t, h, "q=p&take=2")
			if r.Code != http.StatusOK {
				t.Fatalf("%d %s", r.Code, r.Body.String())
			}
			if d := decodeSearch(t, r); d.TotalHits != c.want || len(d.Data) != 2 {
				t.Errorf("totalHits %d with %d results, want %d and 2", d.TotalHits, len(d.Data), c.want)
			}
			if want := map[bool]int{false: 1, true: 2}[c.retry]; st.starts != want {
				t.Errorf("%d attempts, want %d", st.starts, want)
			}
		})
	}
}
