package proxytest

import (
	"context"
	"sync/atomic"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/proxy"
)

// Populate is a proxy.Populate that counts what the engine asks of it. Allow is
// the answer of the check; Name and Fills are the endpoint it reports. Records
// counts the populate events that were written and committed, which is the
// number of upstream fetches (R-REPO-026 "populate audit").
type Populate struct {
	Allow bool
	Name  string
	Fills bool

	asks, denies, records atomic.Int64
}

var _ proxy.Populate = (*Populate)(nil)

// Allowed returns a Populate that permits the fill on a default endpoint.
func Allowed() *Populate { return &Populate{Allow: true, Name: "default", Fills: true} }

// Refused returns a Populate that refuses, as a caller who may read but not fill.
func Refused() *Populate { return &Populate{Name: "default", Fills: true} }

// May answers the question.
func (p *Populate) May(context.Context) bool {
	p.asks.Add(1)
	return p.Allow
}

// Deny is the audited refusal.
func (p *Populate) Deny(context.Context) error {
	p.denies.Add(1)
	return errs.E(errs.Forbidden, errs.CodePopulateNotPermitted, "the caller may not populate the cache")
}

// Record counts the event once its transaction commits.
func (p *Populate) Record(_ context.Context, tx meta.Tx) error {
	tx.After(func() { p.records.Add(1) })
	return nil
}

// Endpoint is the endpoint the check reports.
func (p *Populate) Endpoint() (string, bool) { return p.Name, p.Fills }

// Asks is the number of times the engine asked the check.
func (p *Populate) Asks() int { return int(p.asks.Load()) }

// Denies is the number of refusals the engine requested.
func (p *Populate) Denies() int { return int(p.denies.Load()) }

// Records is the number of committed populate events.
func (p *Populate) Records() int { return int(p.records.Load()) }
