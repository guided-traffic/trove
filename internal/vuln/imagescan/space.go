package imagescan

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"time"
)

// The working space of the scanning is admitted through the worker's spool, the budget every other user of the volume draws on
// (Q-VULN-145, docs/security/image-scanning.md): the unpacked database once, and each scan the compressed size of its image plus a margin
// for what the analyser leaves beside it. Nothing here writes more than it holds, so the volume cannot be overrun by the scanning, and a
// scan that does not fit waits for the room or is put off with a reason, never left to evict the pod.

// ErrNoSpace says that the spool cannot take the bytes asked for now: its budget is held or its volume is nearly full. An adapter around a
// spool wraps its own sentinel with it.
var ErrNoSpace = errors.New("the worker's spool has no room")

// Space is the spool as the scanning sees it.
type Space interface {
	// Hold takes n bytes of the budget and returns the function that gives them back; it is safe to call more than once. The error is
	// [ErrNoSpace] when the budget or the volume cannot cover n, and nothing is held then.
	Hold(n int64) (release func(), err error)
	// Adopt counts n bytes that are on the volume already; it checks nothing.
	Adopt(n int64) (release func())
	// Budget is the byte budget of the whole spool.
	Budget() int64
}

// The margins and waits of the admission of a scan.
const (
	// scanMargin is held on top of the compressed size of an image: the analysis (kept within maxSBOMStored once compressed), the report
	// the detection leaves and the files the analyser keeps in its working directory. The layout is read by the analyser and not unpacked.
	scanMargin = int64(64) << 20
	// defaultSpaceWait is how long a scan waits for room before it is put off, and defaultSpacePoll how often it asks again meanwhile.
	defaultSpaceWait = 2 * time.Minute
	defaultSpacePoll = 500 * time.Millisecond
	// busyBackoff is the wait before a scan that found the spool busy is due again. A scan that could never fit, even with the spool
	// empty, waits refusedBackoff: only a larger spool or a smaller image changes that.
	busyBackoff    = time.Minute
	refusedBackoff = time.Hour
)

// The codes of a scan that was put off for lack of room. Neither spends an attempt: the scan did not fail, it did not start.
const (
	CodeSpoolBusy     = "spool_busy"
	CodeSpoolTooSmall = "spool_too_small"
)

// lease is the room one scan holds; Release gives it back and is safe to call on every path.
type lease struct{ release func() }

// Release gives the room back.
func (l *lease) Release() {
	if l.release != nil {
		l.release()
		l.release = nil
	}
}

// admit takes n bytes for a scan, waiting for them up to the service's wait. dbHeld is what the database holds of the budget: a scan that
// cannot fit next to it even in an empty spool is refused at once, because waiting would not help.
func (s *Service) admit(ctx context.Context, l *lease, n, dbHeld int64) error {
	if budget := s.o.Space.Budget(); n+dbHeld > budget {
		return &ScanError{
			Code: CodeSpoolTooSmall, Defer: refusedBackoff,
			Detail: fmt.Sprintf("the scan needs %d bytes of the spool next to the %d bytes of the database, and the budget of the spool is %d; "+
				"raise the spool size of the worker that claims the queue vuln, or lower vulnerabilities.images.max_image_size", n, dbHeld, budget),
		}
	}
	timer := time.NewTimer(s.o.SpaceWait)
	defer timer.Stop()
	for {
		release, err := s.o.Space.Hold(n)
		if err == nil {
			l.release = release
			return nil
		}
		if !errors.Is(err, ErrNoSpace) {
			return err
		}
		poll := time.NewTimer(s.o.SpacePoll)
		select {
		case <-ctx.Done():
			poll.Stop()
			return context.Cause(ctx)
		case <-timer.C:
			poll.Stop()
			return &ScanError{
				Code: CodeSpoolBusy, Defer: busyBackoff,
				Detail: fmt.Sprintf("the spool of the worker had no room for the %d bytes of the scan within %s because other work holds it; the scan is tried again", n, s.o.SpaceWait),
			}
		case <-poll.C:
		}
	}
}

// dirBytes is the size of the regular files below dir: what a database found on the volume holds.
func dirBytes(dir string) int64 {
	var n int64
	_ = filepath.WalkDir(dir, func(_ string, e fs.DirEntry, err error) error {
		if err != nil || !e.Type().IsRegular() {
			return nil //nolint:nilerr // a file that vanished counts as nothing
		}
		if info, err := e.Info(); err == nil {
			n += info.Size()
		}
		return nil
	})
	return n
}
