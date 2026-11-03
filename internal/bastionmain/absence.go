package bastionmain

import (
	"context"
	"sync/atomic"
)

// Absence is the record of what the lookups of one recognized Bastion request found (R-BAS-013, Q-BAS-70). The
// answer class `absent` says that the item is missing for every subject, so that the Bastion may keep a
// negative entry that must never stand for an item that is merely hidden from the caller (R-BAS-024). No
// format code decides it: the core's own lookups note what they could tell, and the marking stage reads the
// record when the 404 goes out.
//
//   - [NoteMissing] is called by a lookup of the host that is subject-independent and authoritative: a package
//     or an item that a local repository does not hold, a document or an artifact that the upstream of a remote
//     repository answered 404 or 410 for, or that its negative entry stands for.
//   - [NoteUnknown] is called when the request met something that makes a 404 mean less: a refusal of the
//     decision point or of a repository's include and exclude patterns, a serve-time refusal, a remote member
//     that could not tell (offline, not cached, only-if-cached).
//
// A 404 is absent exactly when something was noted missing, nothing unknown, and the caller's read shape of
// the addressed repository is full, so that no subject-dependent hiding exists to begin with.
type Absence struct {
	missing, unknown atomic.Bool
}

// Absent reports whether the lookups of the request amount to "missing for every subject".
func (a *Absence) Absent() bool { return a != nil && a.missing.Load() && !a.unknown.Load() }

type absenceKey struct{}

// WithAbsence returns ctx carrying a new record. The marking stage calls it for a recognized request.
func WithAbsence(ctx context.Context) (context.Context, *Absence) {
	a := &Absence{}
	return context.WithValue(ctx, absenceKey{}, a), a
}

func absenceFrom(ctx context.Context) *Absence {
	a, _ := ctx.Value(absenceKey{}).(*Absence)
	return a
}

// NoteMissing records that an authoritative lookup found the item missing for every subject. It is a no-op
// on a request that is no recognized Bastion request.
func NoteMissing(ctx context.Context) {
	if a := absenceFrom(ctx); a != nil {
		a.missing.Store(true)
	}
}

// NoteUnknown records that the request met a refusal, a serve-time check or a lookup that could not tell, so
// that a 404 of it is never `absent`. It is a no-op on a request that is no recognized Bastion request.
func NoteUnknown(ctx context.Context) {
	if a := absenceFrom(ctx); a != nil {
		a.unknown.Store(true)
	}
}
