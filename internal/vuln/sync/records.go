package sync

import (
	"bytes"
	"context"
	stdsync "sync"

	"github.com/guided-traffic/trove/internal/vuln/match"
	"github.com/guided-traffic/trove/internal/vuln/osv"
	"github.com/guided-traffic/trove/internal/vuln/store"
)

func validRecordID(id string) bool { return osv.ValidID(id) }

// fetched is the outcome of the request for one record.
type fetched struct {
	body     []byte
	found    bool
	tooLarge bool
	err      error
}

// fetchApply fetches the records of one batch of the change file, 8 at a time, validates them, drops those whose stored
// modified time and digest equal them and upserts the rest in one transaction (R-VULN-004 incremental steps 2 and 3). A
// record the source no longer has (404) is left to the next full sync; one that fails validation is counted and skipped, so
// that one bad record never stalls the sync.
func (s *Service) fetchApply(ctx context.Context, eco string, ad match.Adapter, ids []string, touched *touchSet) (written, skipped, rejected int, err error) {
	out := make([]fetched, len(ids))
	sem := make(chan struct{}, FetchParallel)
	var wg stdsync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			b, found, big, err := s.fetchRecord(ctx, eco, id)
			out[i] = fetched{body: b, found: found, tooLarge: big, err: err}
		}()
	}
	wg.Wait()
	var recs []*osv.Record
	for _, f := range out {
		switch {
		case f.err != nil:
			return 0, 0, 0, f.err
		case !f.found:
			skipped++
		case f.tooLarge:
			rejected++
		default:
			rec, derr := osv.Decode(f.body, eco, ad)
			if derr != nil {
				rejected++
				continue
			}
			recs = append(recs, rec)
		}
	}
	if len(recs) == 0 {
		return 0, skipped, rejected, nil
	}
	recIDs := make([]string, len(recs))
	for i, r := range recs {
		recIDs[i] = r.Advisory.ID
	}
	var stored map[string]store.Stored
	err = s.o.DB.Read(ctx, func(db dbtx) (err error) {
		stored, err = store.Sync{}.Stored(ctx, db, eco, recIDs)
		return err
	})
	if err != nil {
		return 0, 0, 0, err
	}
	var batch []store.Written
	for _, r := range recs {
		if st, ok := stored[r.Advisory.ID]; ok && bytes.Equal(st.SHA256, r.SHA256[:]) && st.Modified.Equal(r.Advisory.Modified) {
			skipped++
			continue
		}
		batch = append(batch, store.Written{Advisory: r.Advisory, SHA256: r.SHA256[:]})
	}
	if err := s.commitBatch(ctx, eco, batch, touched); err != nil {
		return 0, 0, 0, err
	}
	return len(batch), skipped, rejected, nil
}
