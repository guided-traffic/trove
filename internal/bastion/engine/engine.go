// Package engine is the Bastion's request flow (R-BAS-018 to R-BAS-029, R-BAS-042 to R-BAS-044): the
// classification of a request, the credentials, the answers, the fetch on a miss, the artifacts and documents,
// the negative cache, the concurrency budget and the collapsing of misses, the feed, the unreachable and
// mismatch states, the Bastion's own responses, the token reload and the drain. It is the one place that knows
// how the pieces fit; every piece it uses is behind an interface of a package of its own.
//
// This file is the contract of the work package 4 with the server skeleton (internal/bastion/server) and with
// the packages it builds on: [Deps] lists everything the engine is given, [Engine] is what it hands back. The
// placeholder [New] returns answers 404 bastion_not_served to every request, which is what the skeleton serves
// until work package 4 replaces it (R-BAS-001's acceptance: a client request is a refusal, never a hit).
package engine

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/guided-traffic/trove/internal/bastion/answers"
	"github.com/guided-traffic/trove/internal/bastion/cache"
	"github.com/guided-traffic/trove/internal/bastion/downloads"
	"github.com/guided-traffic/trove/internal/bastion/guard"
	"github.com/guided-traffic/trove/internal/bastion/mainclient"
	"github.com/guided-traffic/trove/internal/bastion/metrics"
	"github.com/guided-traffic/trove/internal/bastion/shared"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// Deps is everything the engine is given. The server builds it from the configuration; a field that a work
// package builds is a stub until then ([Deps.Stubbed] names them). The engine takes no global state.
type Deps struct {
	// Config is the validated configuration of the role.
	Config *config.BastionConfig
	Log    *slog.Logger
	// Metrics is the one set of the Bastion's metrics (internal/bastion/metrics); the engine and every package
	// below it count through it.
	Metrics *metrics.Metrics
	// Replica is the pod name; Release the version of the binary.
	Replica string
	Release string
	// Now is the clock; nil means time.Now. Tests inject one.
	Now func() time.Time

	// Main is the client of the main installation (work package 4, internal/bastion/mainclient) and Tokens the
	// token in use (mainclient.Holder), which the engine's reloader replaces (R-BAS-006).
	Main   mainclient.Client
	Tokens *mainclient.Holder
	// Cache is the backend (work package 5) and Signer signs the entries and answers (R-BAS-032); nil without
	// a signing key, which the configuration allows with the volume backend and without Valkey.
	Cache  cache.Backend
	Signer *cache.Signer
	// Shared is the state the replicas share, or the local one (work package 6).
	Shared *shared.State
	// Guard is the abuse protection (guard.New, package 6); the server builds it with the shared state.
	Guard guard.Guard
	// Downloads is the download event buffer (work package 7).
	Downloads downloads.Buffer
	// Feed is the log of the feed entries received, which the validity rules of R-BAS-020 read.
	Feed *answers.FeedLog
}

// Stubbed names the dependencies that are still the placeholders of the contract work. The server logs them
// at start; a Bastion with a stubbed cache must not serve a read, which engine.New of work package 4
// checks.
func (d Deps) Stubbed() []string {
	var out []string
	if _, ok := d.Main.(mainclient.Stub); ok {
		out = append(out, "mainclient")
	}
	if _, ok := d.Cache.(cache.Stub); ok {
		out = append(out, "cache")
	}
	if _, ok := d.Downloads.(downloads.Stub); ok {
		out = append(out, "downloads")
	}
	return out
}

// Engine is the Bastion's request flow.
type Engine interface {
	// Handler serves every request of the client listener, whatever its path and method: the engine
	// classifies the path itself (R-BAS-018), so the server registers it as the router's fallback.
	http.Handler
	// Start runs the engine's loops (handshake, feed, token reload, sweep, spool adoption, answer refresh)
	// until ctx ends or Drain was called. It does not block: the loops run in goroutines the engine owns.
	Start(ctx context.Context) error
	// Drain stops the loops, sends or spools the download events and releases the leases (R-BAS-043); the
	// server calls it after the listeners stopped accepting and the in-flight requests finished or were cut.
	Drain(ctx context.Context) error
	// Ready reports what the engine contributes to readiness: nil, or an error naming what is missing. It
	// does not depend on the main installation (R-BAS-043, Q-BAS-44); it fails before the cache backend's
	// first check passed.
	Ready(ctx context.Context) error
}

// New builds the engine from its dependencies. This placeholder serves 404 bastion_not_served to every
// request; work package 4 replaces its body, keeps its signature and validates the dependencies.
func New(d Deps) (Engine, error) {
	if d.Config == nil || d.Metrics == nil {
		return nil, errors.New("engine: Config and Metrics are required")
	}
	return &placeholder{d: d}, nil
}

type placeholder struct{ d Deps }

// ServeHTTP answers every request with the refusal of a path the Bastion does not serve (R-BAS-003, R-BAS-029):
// problem details with the code bastion_not_served and Cache-Control: no-store.
func (p *placeholder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.d.Metrics.Refused.WithLabelValues(metrics.RefusedNotServed).Inc()
	p.d.Metrics.Requests.WithLabelValues(metrics.ClassRefused, metrics.ResultRefused).Inc()
	w.Header().Set("Cache-Control", "no-store")
	httpserver.Problem(w, r, errs.E(errs.NotFound, errs.CodeBastionNotServed, "This path is not served by a Bastion."))
}

func (p *placeholder) Start(context.Context) error { return nil }

func (p *placeholder) Drain(ctx context.Context) error { return p.d.Downloads.Drain(ctx) }

func (p *placeholder) Ready(context.Context) error { return nil }
