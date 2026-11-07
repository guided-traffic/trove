// Package fixup is the registry of the restore's fix-ups (R-BAK-021 steps 6 and 7, R-BAK-022): what a restore rebuilds or
// invalidates after the load, registered by the package or the file that owns the state, the same pattern as the table
// registry of R-BAK-004 and the revision registry of R-ARCH-009. The restore runs every registered fix-up of a phase in a
// fixed order and never knows a domain by name, so the npm, NuGet and storage parts are added in files of their own
// without editing a shared switch.
//
// Two kinds exist:
//
//   - A [Fixup] runs inside the one transaction of step 6, after the schema is brought forward, together with the marker's
//     phase change to "fixed". Because a resumed restore never applies step 6 twice, a fix-up need not be idempotent on its
//     own, but it must be deterministic given the loaded rows.
//   - A [CachePurger] runs in step 7 when the backup excluded remote caches (R-BAK-008). It empties the cache of the remote
//     repositories of one format through that format's storage delete path, so format hooks update their indexes and
//     rev_seq grows. It runs in several transactions and must be idempotent, because step 7 is repeated by a resumed
//     restore.
//
// Fix-ups of the core (jobs, system_state keys, revision columns, the storage recount, mirror_state) live next to this
// file, one file per owner; a format adds its purger in its own file.
package fixup

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/guided-traffic/trove/internal/backup/format"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/qname"
)

// Env is what a fix-up may depend on.
type Env struct {
	// Manifest is the backup being restored.
	Manifest *format.Manifest
	// Now is the restore's clock; fix-ups use it instead of the database's now() where they must agree with each other.
	Now time.Time
	// NewKeyspaceID and NewIncarnation are the fresh identifiers the restore generated once, so a resumed restore's step 6
	// (which runs once) and its summary agree.
	NewKeyspaceID, NewIncarnation string
	// ExcludeRemoteCaches is options.excludeRemoteCaches of the manifest.
	ExcludeRemoteCaches bool
	// Items is the storage delete path (R-STOR-018) a [CachePurger] empties a remote repository's cache through. The
	// restore sets it to the item store composed with the format hooks, so the hooks run in the removing
	// transactions. A purger finds it nil only in a restore that runs no step 7.
	Items CacheEraser
}

// CacheEraser removes every cached item of one remote repository through the storage delete path with the trash can
// bypassed (R-STOR-018, R-BAK-008): the storage hook of the repository's format learns of the removed files in the
// removing transaction, so its index rows and the packages' rev_seq follow. It is idempotent and resumable: it removes
// what is left. The item store implements it.
type CacheEraser interface {
	EraseCache(ctx context.Context, repoID int64, name qname.Repo, format string) error
}

// Fixup is one step-6 fix-up.
type Fixup struct {
	// Name identifies the fix-up in the restore's log and in tests.
	Name string
	// Order sorts the fix-ups; equal orders run by name. Core fix-ups use the constants below.
	Order int
	// Apply runs in the shared step-6 transaction.
	Apply func(ctx context.Context, tx meta.Tx, env Env) error
}

// The orders of the core fix-ups. Gaps leave room for others.
const (
	OrderJobs         = 100
	OrderSystemState  = 200
	OrderRevisions    = 300
	OrderStorageUsage = 400
	OrderMirrorState  = 500
	OrderFormats      = 600
)

// CachePurger empties the remote-repository caches of one format (R-BAK-008).
type CachePurger struct {
	// Name is the format, for the log.
	Name string
	// Purge empties the cache of every remote repository of the format.
	Purge func(ctx context.Context, db meta.Runner, env Env) error
}

type registry struct {
	mu      sync.Mutex
	fixups  []Fixup
	purgers []CachePurger
	frozen  bool
}

var std registry

// Register adds a step-6 fix-up. It panics on a duplicate name, a nil function or a registration after Freeze: all are
// programming errors found at start-up.
func Register(f Fixup) {
	std.mu.Lock()
	defer std.mu.Unlock()
	if f.Name == "" || f.Apply == nil {
		panic("fixup: Register needs a name and an Apply function")
	}
	if std.frozen {
		panic(fmt.Sprintf("fixup: Register(%q) after the registry was frozen", f.Name))
	}
	for _, o := range std.fixups {
		if o.Name == f.Name {
			panic(fmt.Sprintf("fixup: %q registered twice", f.Name))
		}
	}
	std.fixups = append(std.fixups, f)
}

// RegisterPurger adds a remote-cache purger, under the same rules as [Register].
func RegisterPurger(p CachePurger) {
	std.mu.Lock()
	defer std.mu.Unlock()
	if p.Name == "" || p.Purge == nil {
		panic("fixup: RegisterPurger needs a name and a Purge function")
	}
	if std.frozen {
		panic(fmt.Sprintf("fixup: RegisterPurger(%q) after the registry was frozen", p.Name))
	}
	for _, o := range std.purgers {
		if o.Name == p.Name {
			panic(fmt.Sprintf("fixup: purger %q registered twice", p.Name))
		}
	}
	std.purgers = append(std.purgers, p)
}

// Freeze ends registration; the composition root calls it before it runs a command.
func Freeze() {
	std.mu.Lock()
	std.frozen = true
	std.mu.Unlock()
}

// Fixups returns the registered step-6 fix-ups in run order.
func Fixups() []Fixup {
	std.mu.Lock()
	out := append([]Fixup(nil), std.fixups...)
	std.mu.Unlock()
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Order != out[j].Order {
			return out[i].Order < out[j].Order
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Purgers returns the registered purgers ordered by name.
func Purgers() []CachePurger {
	std.mu.Lock()
	out := append([]CachePurger(nil), std.purgers...)
	std.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
