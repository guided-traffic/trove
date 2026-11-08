// Package app holds the two interfaces of the shutdown and three holders of a type that quiesces first.
package app

import "context"

type Quiescer interface{ Quiesce(ctx context.Context) }

type QuiescesFirst interface {
	Quiescer
	QuiescesFirst()
}

// Worker quiesces first.
type Worker struct{}

func (*Worker) Quiesce(context.Context) {}
func (*Worker) QuiescesFirst()          {}

// Named holds the worker in a named field and does not carry the marker: the violation.
type Named struct{ worker *Worker }

func (g *Named) Quiesce(ctx context.Context) { g.worker.Quiesce(ctx) }

// Marked holds it in a named field and carries the marker itself: clean.
type Marked struct{ worker *Worker }

func (g *Marked) Quiesce(ctx context.Context) { g.worker.Quiesce(ctx) }
func (g *Marked) QuiescesFirst()              {}

// Embedded embeds it, so the marker is promoted: clean.
type Embedded struct{ *Worker }
