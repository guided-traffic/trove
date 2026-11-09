package app

import (
	"time"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/jobs"
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
	// Release is the time the job release writes get, from the cancel of the
	// handlers to the close deadline: the settle deadline of the job worker
	// (jobs.SettleDeadline, Q-ARCH-329).
	Release time.Duration
	// CommitGrace is the longest a committing request runs on past the server
	// phase; the exit deadline can shorten it.
	CommitGrace time.Duration
	// CloseReserve is kept free for the flush and the close of the components
	// after the commit grace.
	CloseReserve time.Duration
	// OpsReserve is kept free for the ops listener and the tracer, last of all. It is also the longest the ops listener
	// waits for a connection of its own, whenever the close phase ends (opsBy).
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
		Release:      jobs.SettleDeadline,
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
	// ReleaseAt is when the components that quiesce first (the job worker) are
	// cancelled: early enough that the release writes of their jobs get the whole
	// settle deadline before CloseBy, and never before the drain ends or after
	// QuiesceAt (Q-ARCH-329).
	ReleaseAt time.Time
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
	p.ReleaseAt = p.CloseBy.Add(-b.Release)
	if floor := start.Add(drain); p.ReleaseAt.Before(floor) {
		p.ReleaseAt = floor
	}
	if p.ReleaseAt.After(p.QuiesceAt) {
		p.ReleaseAt = p.QuiesceAt
	}
	p.CommitBy = p.ServerBy.Add(b.CommitGrace)
	if limit := p.CloseBy.Add(-b.CloseReserve); p.CommitBy.After(limit) {
		p.CommitBy = limit
	}
	if p.CommitBy.Before(p.ServerBy) {
		p.CommitBy = p.ServerBy
	}
	return p
}

// opsBy is when the ops listener stops waiting for its connections and resets the ones that are still open. The ops
// listener serves probes and scrapes, whose answers are a few KiB: it gets OpsReserve from the end of the close phase, not
// the time up to the exit deadline, so one peer that does not take its response cannot keep a drained pod running to the
// end of its grace period (H-798, Q-HA-280). It is never later than the exit deadline.
func (p shutdownPlan) opsBy(now time.Time, b shutdownBudget) time.Time {
	by := now.Add(b.OpsReserve)
	if by.After(p.ExitBy) {
		return p.ExitBy
	}
	return by
}
