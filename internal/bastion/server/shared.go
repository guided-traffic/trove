package server

import (
	"context"

	"github.com/guided-traffic/trove/internal/bastion/cache"
	"github.com/guided-traffic/trove/internal/bastion/guard"
	"github.com/guided-traffic/trove/internal/bastion/shared"
)

// openShared builds the shared state and the guard (R-BAS-034 to R-BAS-037): the local state for valkey.mode
// none, the Valkey state otherwise. A Valkey that cannot be reached does not stop the start and does not make
// the replica unready; it is a state of the client, which trove_valkey_available shows.
func (s *Server) openShared(signer *cache.Signer) (*shared.State, guard.Guard, error) {
	b := s.cfg.Bastion
	st, c, err := shared.Open(context.Background(), shared.Options{
		Valkey: s.cfg.Valkey, Replicas: b.Replicas, Instance: s.instance, Signer: signer,
		Log: s.log.With("component", "shared"), Registry: s.metrics.Registry(), Metrics: s.bm,
	})
	if err != nil {
		return nil, nil, err
	}
	s.closers = append(s.closers, c)
	g, err := guard.New(guard.Options{
		Limits: b.Limits, FailedAuth: b.FailedAuth, Blocking: b.Blocking,
		State: st, Metrics: s.bm, Log: s.log.With("component", "guard"),
	})
	if err != nil {
		s.closeAll()
		return nil, nil, err
	}
	s.closers = append(s.closers, g)
	return st, g, nil
}

// closeAll releases what openShared opened, last first; it may be called again.
func (s *Server) closeAll() {
	for i := len(s.closers) - 1; i >= 0; i-- {
		_ = s.closers[i].Close()
	}
	s.closers = nil
}
