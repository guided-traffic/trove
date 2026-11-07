package ship

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/guided-traffic/trove/internal/journal"
)

type quarantineOutbox struct {
	*fakeOutbox
	quarantined []string
}

func (o *quarantineOutbox) Quarantine(_ context.Context, r journal.OutboxRow, reason string) error {
	o.quarantined = append(o.quarantined, r.ID+": "+reason)
	o.rows = slices.DeleteFunc(o.rows, func(x journal.OutboxRow) bool { return x.ID == r.ID })
	return nil
}

// TestR_HA_054_AnUnencodableRowIsQuarantinedAndTheRestShips: one row that cannot become an object must not hold back the
// revocations behind it (audit finding L4); it is set aside where it stays visible, and counted.
func TestR_HA_054_AnUnencodableRowIsQuarantinedAndTheRestShips(t *testing.T) {
	rs := rows(3)
	rs[1].Entry = json.RawMessage(`[1]`) // not a JSON object: Encode refuses it
	badID := rs[1].ID
	ob := &quarantineOutbox{fakeOutbox: &fakeOutbox{rows: rs}}
	st := &fakeStore{}
	var seen []string
	sh := New(Options{Outbox: ob, Store: st, BlobPrefix: "trove", OnQuarantine: func(r journal.OutboxRow, _ error) { seen = append(seen, r.ID) }})
	n, err := sh.Ship(t.Context())
	if err != nil || n != 2 {
		t.Fatalf("Ship = %d, %v; want the two good rows shipped", n, err)
	}
	if len(st.puts) != 2 || len(ob.quarantined) != 1 || len(seen) != 1 || seen[0] != badID {
		t.Errorf("puts %v, quarantined %v, callback %v", st.puts, ob.quarantined, seen)
	}
	if len(ob.rows) != 0 {
		t.Errorf("the outbox keeps %d rows", len(ob.rows))
	}
}

// TestR_HA_054_AnUnencodableRowWithoutQuarantineStillStopsLoudly: an outbox that cannot set a row aside keeps the failing
// behaviour rather than dropping the row.
func TestR_HA_054_AnUnencodableRowWithoutQuarantineStillStopsLoudly(t *testing.T) {
	rs := rows(2)
	rs[0].Entry = json.RawMessage(`[1]`)
	ob := &fakeOutbox{rows: rs}
	if _, err := New(Options{Outbox: ob, Store: &fakeStore{}}).Ship(t.Context()); err == nil || len(ob.rows) != 2 {
		t.Fatalf("Ship = %v with %d rows left; want an error and nothing dropped", err, len(ob.rows))
	}
}
