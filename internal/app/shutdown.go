package app

import (
	"time"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// shutdownBudget is how the 5 s margin of the pod's grace period is spent
// (R-ARCH-024 step 5 and 6, R-HA-011). Production uses defaultBudget; the tests
// scale it down.
type shutdownBudget struct {
	// Margin is what the configuration keeps free of the grace period after the
	// server phase (config.ShutdownMargin). The exit deadline lies Slack before its
	// end, so the process is gone before the kubelet's SIGKILL, not exactly at it.
	Margin time.Duration
	// Slack is the part of Margin that no phase may use.
	Slack time.Duration
	// Quiesce is how long before the end of the server phase running work is
	// cancelled and released (step 4).
	Quiesce time.Duration
	// CommitGrace is the longest a committing request runs on past the server
	// phase; the exit deadline can shorten it.
	CommitGrace time.Duration
	// CloseReserve is kept free for the flush and the close of the components
	// after the commit grace.
	CloseReserve time.Duration
	// OpsReserve is kept free for the ops listener and the tracer, last of all.
	OpsReserve time.Duration
	// Abandon is how long past the close deadline a component that ignores its
	// context is waited for; inside OpsReserve.
	Abandon time.Duration
}

func defaultBudget() shutdownBudget {
	return shutdownBudget{
		Margin:       config.ShutdownMargin,
		Slack:        500 * time.Millisecond,
		Quiesce:      5 * time.Second,
		CommitGrace:  httpserver.DefaultCommitGrace,
		CloseReserve: time.Second,
		OpsReserve:   500 * time.Millisecond,
		Abandon:      250 * time.Millisecond,
	}
}

// shutdownPlan holds the absolute times of every phase. All of them derive from
// one start instant and from the one exit deadline, so the sum of the phases can
// never exceed the budget: a phase that runs long shortens the later ones.
type shutdownPlan struct {
	Start time.Time
	// ServerBy ends the server phase (steps 3 and 4): start + drain + shutdown_timeout.
	ServerBy time.Time
	// QuiesceAt is when running work is cancelled and released.
	QuiesceAt time.Time
	// CommitBy bounds the commit grace of requests that are committing.
	CommitBy time.Time
	// CloseBy ends the flush and the close of the components (step 6).
	CloseBy time.Time
	// ExitBy is the one deadline the process exits before: start + drain +
	// shutdown_timeout + margin - slack, which the configuration keeps inside the
	// grace period.
	ExitBy time.Time
}

// planShutdown computes the plan. drain is the drain delay this role really
// waits (zero for a worker), timeout is server.shutdown_timeout.
func planShutdown(start time.Time, drain, timeout time.Duration, b shutdownBudget) shutdownPlan {
	p := shutdownPlan{Start: start}
	p.ServerBy = start.Add(drain + timeout)
	p.ExitBy = p.ServerBy.Add(b.Margin - b.Slack)
	p.QuiesceAt = p.ServerBy.Add(-b.Quiesce)
	if floor := start.Add(drain); p.QuiesceAt.Before(floor) {
		p.QuiesceAt = floor
	}
	p.CloseBy = p.ExitBy.Add(-b.OpsReserve)
	p.CommitBy = p.ServerBy.Add(b.CommitGrace)
	if limit := p.CloseBy.Add(-b.CloseReserve); p.CommitBy.After(limit) {
		p.CommitBy = limit
	}
	if p.CommitBy.Before(p.ServerBy) {
		p.CommitBy = p.ServerBy
	}
	return p
}
