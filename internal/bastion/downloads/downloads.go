// Package downloads buffers the downloads a Bastion served from its cache and reports them to the main
// installation (R-BAS-016, R-BAS-039, Q-BAS-45): a bounded memory buffer, batches every flush interval or at
// the batch size, a spool to the cache backend at shutdown, and the count of events a full buffer lost.
//
// Work package 7 implements [Buffer]. The engine (work package 4) calls Add for every artifact it serves from
// the cache (a hit, or a check answered 304) while the handshake says recordDownloads: true, and Drain at
// shutdown. The buffer sends through internal/bastion/mainclient and spools through internal/bastion/cache;
// both are interfaces it is given.
package downloads

import (
	"context"
	"errors"

	"github.com/guided-traffic/trove/internal/bastion/iface"
)

// Buffer is the download event buffer of a replica.
type Buffer interface {
	// Add appends an event, or drops it when the buffer is full: the drop is counted in
	// trove_bastion_audit_events_dropped_total and reported in the next batch's dropped member (R-BAS-039).
	// It never blocks the request that serves the download.
	Add(e iface.DownloadEvent)
	// SetEnabled follows recordDownloads of the handshake (R-BAS-010): while false, Add discards without
	// counting and nothing is sent.
	SetEnabled(on bool)
	// Run sends batches until ctx ends; it retries while the main installation is unreachable and keeps the
	// events, up to the bound.
	Run(ctx context.Context) error
	// Drain sends what is buffered within ctx and spools the rest to the cache backend, at most
	// bastion.audit.spool_max_events (R-BAS-039). It is called at shutdown, after the listeners stopped.
	Drain(ctx context.Context) error
	// Adopt reads the spool files of any replica and sends them once the main installation is reachable,
	// deleting each after a 2xx (R-BAS-039).
	Adopt(ctx context.Context) error
}

// ErrNotBuilt is what [Stub] answers.
var ErrNotBuilt = errors.New("downloads: not built yet (work package 7)")

// Stub is the placeholder buffer of the contract work: it holds nothing and sends nothing.
type Stub struct{}

var _ Buffer = Stub{}

// Add implements [Buffer].
func (Stub) Add(iface.DownloadEvent) {}

// SetEnabled implements [Buffer].
func (Stub) SetEnabled(bool) {}

// Run implements [Buffer]; it returns when ctx ends.
func (Stub) Run(ctx context.Context) error { <-ctx.Done(); return nil }

// Drain implements [Buffer].
func (Stub) Drain(context.Context) error { return nil }

// Adopt implements [Buffer].
func (Stub) Adopt(context.Context) error { return ErrNotBuilt }
