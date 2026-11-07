// Package mirror is the engine of the mirror mode of remote repositories (repositories-and-proxy.md R-REPO-050 to
// R-REPO-059): the dispatch of passes, the pass itself (discovery, the pending names, the fills through the remote
// engine, the end), the fence of a pass, the throttling and the window, the bounds, the yield to clients, the metrics and
// the pass events. Its passes are jobs of the job framework on their own queue (QueueMirror), run by whatever process
// claims that queue (architecture.md R-ARCH-016, Q-ARCH-285); nothing here depends on which process that is.
//
// The seams (architecture.md Q-ARCH-288, docs/developer/mirror.md):
//
//   - A format discovers through the leaf contract internal/mirror/discover, which its remote adapter implements
//     (formats.RemoteCaps.Mirror); the composition root hands the engine the lister of a format by its ID.
//   - The engine fills through the remote engine (internal/proxy) with proxy.PriorityBackground, so the background
//     slots of R-REPO-055 and the rules of R-REPO-025 apply, and through the format's artifact gate in internal/formathost.
//   - The management API enqueues KindPass under PassKey with PassArgs.
//
// Contract wave: this file holds the names the work packages share; the engine itself, its tables and its events are work
// package C.
package mirror

import (
	"strconv"

	"github.com/guided-traffic/trove/internal/qname"
)

// The job kinds and the queue of the mirror (architecture.md R-ARCH-016, Q-REPO-131, Q-ARCH-285).
const (
	// KindPass is one attempt of a pass of one repository: queue QueueMirror, dedup key PassKey (exclusive), MaxAttempts
	// 1 (a failed attempt is continued by the dispatch after the back-off, R-REPO-053), no timeout.
	KindPass = "mirror-pass"
	// KindDispatch is the periodic task that enqueues the due passes: queue QueueDispatch, exclusive under its name, every
	// minute.
	KindDispatch = "mirror-dispatch"
	// QueueDispatch is the queue of the dispatch: the maintenance queue, so that hours-long passes holding the slots of the
	// queue mirror cannot starve the one-minute task that enqueues the next passes (Q-ARCH-321).
	QueueDispatch = "maintenance"
	// QueueMirror is the queue of the passes; its slots per claiming process are jobs.workers.mirror, and a pool of its own can
	// claim it alone (ha-and-helm.md R-HA-062).
	QueueMirror = "mirror"
)

// PassKey is the dedup key of the passes of one repository, "mirror-pass:<repository id>" (R-REPO-053).
func PassKey(repoID int64) string { return KindPass + ":" + strconv.FormatInt(repoID, 10) }

// PassArgs are the arguments of a KindPass job: the repository by key and numeric ID, and whether a request asked for a
// full pass (R-REPO-056). The arguments only name the repository; the pass reads its settings from the snapshot and its
// state from the mirror's tables when it starts, because an exclusive enqueue that finds a queued pass drops its own
// arguments.
type PassArgs struct {
	Repository   qname.Repo `json:"repository"`
	RepositoryID int64      `json:"repositoryId"`
	Full         bool       `json:"full,omitempty"`
}
