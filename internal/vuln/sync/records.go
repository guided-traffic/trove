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
// modified time and digest equal them and upserts the rest (R-VULN-004 incremental steps 2 and 3). A record the source no longer
// has (404) is left to the next full sync; one that fails validation is counted and skipped, so that one bad record never stalls
// the sync. A body whose identifier is not the one requested is rejected: the response of <id>.json is for that one record
// (S11 of the fix wave). A record whose modified time is older than the stored one does not replace it. The records are held
// and written in batches of at most BatchSize records and BatchBytes bytes, so that the largest records do not become one statement.
func (s *Service) fetchApply(ctx context.Context, eco string, ad match.Adapter, ids []string) (written, skipped, rejected int, err error) {
	var recs []*osv.Record
	var held int
	flush := func() error {
		w, k, err := s.writeFetched(ctx, eco, recs)
		written, skipped = written+w, skipped+k
		recs, held = nil, 0
		return err
	}
	for len(ids) > 0 {
		window := ids[:min(fetchWindow, len(ids))]
		ids = ids[len(window):]
		out := s.fetchWindow(ctx, eco, window)
		for i, f := range out {
			switch {
			case f.err != nil:
				return 0, 0, 0, f.err
			case !f.found:
				skipped++
			case f.tooLarge:
				rejected++
			default:
				rec, derr := osv.Decode(f.body, eco, ad)
				if derr != nil || rec.Advisory.ID != window[i] {
					rejected++
					continue
				}
				if len(recs) > 0 && (len(recs) >= BatchSize || held+len(f.body) > BatchBytes) {
					if err := flush(); err != nil {
						return 0, 0, 0, err
					}
				}
				recs = append(recs, rec)
				held += len(f.body)
			}
		}
	}
	if err := flush(); err != nil {
		return 0, 0, 0, err
	}
	return written, skipped, rejected, nil
}

// fetchWindow is the number of records requested and held before they are decoded.
const fetchWindow = FetchParallel * 4

// fetchWindow requests the records of ids, FetchParallel at a time.
func (s *Service) fetchWindow(ctx context.Context, eco string, ids []string) []fetched {
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
	return out
}

// writeFetched writes the decoded records that differ from the stored ones: the digest and the modified time are compared, and
// a record older than the stored one is dropped.
func (s *Service) writeFetched(ctx context.Context, eco string, recs []*osv.Record) (written, skipped int, err error) {
	if len(recs) == 0 {
		return 0, 0, nil
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
		return 0, 0, err
	}
	var batch []store.Written
	for _, r := range recs {
		if st, ok := stored[r.Advisory.ID]; ok && fetchedNotNewer(st, r) {
			skipped++
			continue
		}
		batch = append(batch, store.Written{Advisory: r.Advisory, SHA256: r.SHA256[:]})
	}
	if err := s.commitBatch(ctx, eco, batch); err != nil {
		return 0, 0, err
	}
	return len(batch), skipped, nil
}

// fetchedNotNewer reports a fetched record that does not replace the stored one: the same record as stored (digest and modified
// time), or one whose modified time is older than the stored one (S11 of the fix wave).
func fetchedNotNewer(st store.Stored, r *osv.Record) bool {
	if r.Advisory.Modified.Before(st.Modified) {
		return true
	}
	return bytes.Equal(st.SHA256, r.SHA256[:]) && st.Modified.Equal(r.Advisory.Modified)
}
