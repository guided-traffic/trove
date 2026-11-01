package token

import (
	"context"
	"encoding/json"
	"net/netip"
	"sync"
	"time"

	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

const (
	// LastUsedFlushInterval is how often a replica writes the last-used times of
	// its tokens (R-AUTH-031).
	LastUsedFlushInterval = time.Minute
	// DefaultLastUsedGranularity applies when auth.tokens.last_used_granularity
	// is not set (a service built outside the composition root).
	DefaultLastUsedGranularity = 5 * time.Minute
)

// use is the latest successful authentication of one token on this replica.
type use struct {
	at   time.Time
	addr string
}

// lastUsed is the write-only buffer of R-AUTH-031 (R-ARCH-032): requests write
// it, nothing on the request path reads it. One entry per token, so a flush
// writes at most one row per token however often the token was presented.
type lastUsed struct {
	mu      sync.Mutex
	pending map[string]use
}

func (b *lastUsed) note(id string, u use) {
	b.mu.Lock()
	if b.pending == nil {
		b.pending = map[string]use{}
	}
	b.pending[id] = u
	b.mu.Unlock()
}

func (b *lastUsed) take() map[string]use {
	b.mu.Lock()
	batch := b.pending
	b.pending = nil
	b.mu.Unlock()
	return batch
}

// noteUse records that the token authenticated a request now. It takes a lock
// for a map write and does nothing else.
func (s *Service) noteUse(id, clientAddr string) {
	if id == "" {
		return
	}
	s.used.note(id, use{at: s.o.Now(), addr: storableAddr(clientAddr)})
}

// storableAddr returns the address in the form of an inet column, or "" when the
// request has none (an IPv6 zone is dropped: PostgreSQL does not take it).
func storableAddr(s string) string {
	a, err := netip.ParseAddr(s)
	if err != nil {
		return ""
	}
	return a.WithZone("").String()
}

func (s *Service) granularity() time.Duration {
	if g := s.o.Tokens.LastUsedGranularity; g > 0 {
		return g
	}
	return DefaultLastUsedGranularity
}

// FlushLastUsed writes the buffered last-used times in one statement. It is
// best effort (R-AUTH-031): a failed flush drops its batch, counts
// trove_auth_lastused_flush_failures_total and returns the error to the caller,
// which logs it; a token used again is recorded again.
func (s *Service) FlushLastUsed(ctx context.Context) error {
	batch := s.used.take()
	if len(batch) == 0 {
		return nil
	}
	type entry struct {
		ID   string `json:"id"`
		At   string `json:"at"`
		Addr string `json:"addr"`
	}
	entries := make([]entry, 0, len(batch))
	for id, u := range batch {
		entries = append(entries, entry{ID: id, At: u.at.UTC().Format(time.RFC3339Nano), Addr: u.addr})
	}
	raw, err := json.Marshal(entries)
	if err == nil {
		err = s.o.DB.InTx(ctx, func(tx meta.Tx) error {
			_, err := principal.Queries(tx).IdentityTouchTokensLastUsed(ctx, q.IdentityTouchTokensLastUsedParams{
				Uses: string(raw), GranularitySecs: s.granularity().Seconds(),
			})
			return err
		})
	}
	if err != nil {
		s.o.Metrics.LastUsedFlushFailed()
		return principal.DBErr("token.last_used", err)
	}
	return nil
}

// RunLastUsed flushes every LastUsedFlushInterval until ctx ends. The caller
// flushes once more at shutdown (R-ARCH-024 step 6).
func (s *Service) RunLastUsed(ctx context.Context) {
	t := time.NewTicker(LastUsedFlushInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.FlushLastUsed(ctx); err != nil {
				s.log.Warn("token last-used times were not written; the next use records them again", "error", err)
			}
		}
	}
}
