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
	id       string
	body     []byte
	found    bool
	tooLarge bool
	// size is the length a too large response declared, 0 when it declared none.
	size int64
	// held is what the byte gate counts for the body until it is decoded.
	held int64
	err  error
}

// byteGate bounds the bytes of fetched bodies that wait to be decoded: a body that would take it above its limit waits, except
// when nothing is held, so that a body larger than the limit can still pass alone.
type byteGate struct {
	mu          stdsync.Mutex
	cond        *stdsync.Cond
	used, limit int64
	// peak is the most that was ever counted at once.
	peak int64
	// waiting is the number of acquire calls that wait right now.
	waiting int
}

func newByteGate(limit int64) *byteGate {
	g := &byteGate{limit: limit}
	g.cond = stdsync.NewCond(&g.mu)
	return g
}

// acquire waits until n bytes fit, or ctx ends.
func (g *byteGate) acquire(ctx context.Context, n int64) error {
	stop := context.AfterFunc(ctx, func() {
		g.mu.Lock()
		g.cond.Broadcast()
		g.mu.Unlock()
	})
	defer stop()
	g.mu.Lock()
	defer g.mu.Unlock()
	for g.used > 0 && g.used+n > g.limit {
		if err := ctx.Err(); err != nil {
			return err
		}
		g.waiting++
		g.cond.Wait()
		g.waiting--
	}
	g.used += n
	g.peak = max(g.peak, g.used)
	return nil
}

// add counts n bytes that are already held, without waiting.
func (g *byteGate) add(n int64) {
	g.mu.Lock()
	g.used += n
	g.peak = max(g.peak, g.used)
	g.mu.Unlock()
}

func (g *byteGate) release(n int64) {
	g.mu.Lock()
	g.used -= n
	g.cond.Broadcast()
	g.mu.Unlock()
}

// fetchApply fetches the records of one batch of the change file, 8 at a time, validates them, drops those whose stored
// modified time, digest and adapter version equal them and upserts the rest (R-VULN-004 incremental steps 2 and 3). A record the
// source no longer has (404) is left to the next full sync; one that fails validation is counted and skipped, so that one bad
// record never stalls the sync. A body whose identifier is not the one requested is rejected: the response of <id>.json is for
// that one record (S11 of the fix wave). A record whose modified time is older than the stored one does not replace it. The
// bodies are decoded as they arrive and released: the bytes of bodies that wait for the decoder are bounded by BatchBytes (a
// response without a declared length is counted when it has been read), and the decoded records are written in batches of at
// most BatchSize records and BatchBytes bytes, so that the largest records do not become one statement. Every rejection is
// logged through rl, which the caller shares over the batches of one run (Q-VULN-119).
func (s *Service) fetchApply(ctx context.Context, eco string, ad match.Adapter, ids []string, rl *rejectLog) (written, skipped, rejected int, err error) {
	ctx, cancel := context.WithCancel(ctx)
	stream := s.fetchStream(ctx, eco, ids, BatchBytes)
	defer func() {
		cancel()
		for range stream.ch { //nolint:revive // drains the producers that the cancel stops
		}
	}()
	var recs []*osv.Record
	var held int
	flush := func() error {
		w, k, err := s.writeFetched(ctx, eco, recs)
		written, skipped = written+w, skipped+k
		recs, held = nil, 0
		return err
	}
	limit := int64(s.o.Config.Sources.OSV.MaxRecordSize)
	for f := range stream.ch {
		switch {
		case f.err != nil:
			return 0, 0, 0, f.err
		case !f.found:
			skipped++
		case f.tooLarge:
			stream.gate.release(f.held)
			rejected++
			rl.add(f.id+".json", f.id, sizeRejection(f.size, limit))
		default:
			rec, derr := osv.Decode(f.body, eco, ad)
			size := len(f.body)
			stream.gate.release(f.held)
			if why := fetchedRejection(rec, derr, f.id); why != nil {
				rejected++
				rl.add(f.id+".json", f.id, why)
				continue
			}
			if len(recs) > 0 && (len(recs) >= BatchSize || held+size > BatchBytes) {
				if err := flush(); err != nil {
					return 0, 0, 0, err
				}
			}
			recs = append(recs, rec)
			held += size
		}
	}
	if err := ctx.Err(); err != nil {
		return 0, 0, 0, context.Cause(ctx)
	}
	if err := flush(); err != nil {
		return 0, 0, 0, err
	}
	return written, skipped, rejected, nil
}

// recordStream is the records of a batch as they arrive, and the gate that counts their bodies.
type recordStream struct {
	ch   <-chan fetched
	gate *byteGate
}

// fetchStream requests the records of ids, FetchParallel at a time, and sends each outcome as it arrives. The channel is closed
// when every request has ended or ctx has.
func (s *Service) fetchStream(ctx context.Context, eco string, ids []string, gateBytes int64) *recordStream {
	out := make(chan fetched)
	gate := newByteGate(gateBytes)
	next := make(chan string)
	var wg stdsync.WaitGroup
	for range min(FetchParallel, len(ids)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for id := range next {
				f := s.fetchRecord(ctx, eco, id, gate)
				select {
				case out <- f:
				case <-ctx.Done():
					gate.release(f.held)
					return
				}
			}
		}()
	}
	go func() {
		defer close(next)
		for _, id := range ids {
			select {
			case next <- id:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		wg.Wait()
		close(out)
	}()
	return &recordStream{ch: out, gate: gate}
}

// fetchedRejection says why a fetched body is not the record asked for, nil when it is: it fails a rule of R-VULN-006, or it
// carries another identifier.
func fetchedRejection(rec *osv.Record, err error, asked string) *rejection {
	switch {
	case err != nil && isRejected(err):
		return ruleRejection(err)
	case err != nil:
		return &rejection{reason: clean(err.Error(), 200), validation: true}
	case rec.Advisory.ID != asked:
		return &rejection{reason: "the body carries the identifier " + quoted(rec.Advisory.ID) + ", not the one requested", validation: true}
	}
	return nil
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
	version := match.AdapterVersion(eco)
	var batch []store.Written
	for _, r := range recs {
		if st, ok := stored[r.Advisory.ID]; ok && fetchedNotNewer(st, r, version) {
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
// time) written by the current adapter version, or one whose modified time is older than the stored one (S11 of the fix wave).
func fetchedNotNewer(st store.Stored, r *osv.Record, adapterVersion int) bool {
	if r.Advisory.Modified.Before(st.Modified) {
		return true
	}
	return st.AdapterVersion == adapterVersion && bytes.Equal(st.SHA256, r.SHA256[:]) && st.Modified.Equal(r.Advisory.Modified)
}
