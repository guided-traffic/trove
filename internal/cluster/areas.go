package cluster

import (
	"fmt"
	"regexp"
	"sort"
	"sync"
)

// AreaClass is how an area leaves the replica (R-ARCH-018).
type AreaClass string

const (
	// ChangeArea carries changes whose propagation an owning page bounds. They
	// go through the change log in PostgreSQL, which carries correctness, and
	// through Valkey, which carries speed.
	ChangeArea AreaClass = "change"
	// SignalArea carries wake-ups and early evictions that client traffic
	// produces. They write nothing to PostgreSQL; delivery is at most once and
	// every consumer stays correct without it.
	SignalArea AreaClass = "signal"
)

// Area is one source of invalidation or wake-up. An area is registered in
// code, by its owning package at initialisation, and a change area also needs
// its cluster_epoch row from the migration that ships it.
type Area struct {
	// Name matches ^[a-z][a-z0-9_]{1,31}$.
	Name  string
	Owner string
	Class AreaClass
	// PayloadVersion is the highest payload version this build understands. A
	// change row with a higher version flushes the area's caches instead of
	// being ignored (rolling upgrades, R-ARCH-018 step 5). Change areas only.
	PayloadVersion int
}

var areaPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{1,31}$`)

var areas = struct {
	mu sync.RWMutex
	by map[string]Area
}{by: map[string]Area{}}

// RegisterArea registers an area. It panics on a programming error: an invalid
// name, an unknown class, a change area without a payload version, or a
// duplicate.
func RegisterArea(a Area) {
	if !areaPattern.MatchString(a.Name) {
		panic(fmt.Sprintf("cluster: invalid area name %q", a.Name))
	}
	switch a.Class {
	case ChangeArea:
		if a.PayloadVersion < 1 {
			panic(fmt.Sprintf("cluster: change area %q needs a payload version of 1 or more", a.Name))
		}
	case SignalArea:
	default:
		panic(fmt.Sprintf("cluster: area %q has class %q", a.Name, a.Class))
	}
	areas.mu.Lock()
	defer areas.mu.Unlock()
	if _, dup := areas.by[a.Name]; dup {
		panic(fmt.Sprintf("cluster: area %q is registered twice", a.Name))
	}
	areas.by[a.Name] = a
}

// LookupArea returns a registered area.
func LookupArea(name string) (Area, bool) {
	areas.mu.RLock()
	defer areas.mu.RUnlock()
	a, ok := areas.by[name]
	return a, ok
}

// Areas lists the registered areas of a class, sorted by name; the empty class
// lists all of them.
func Areas(class AreaClass) []Area {
	areas.mu.RLock()
	defer areas.mu.RUnlock()
	out := make([]Area, 0, len(areas.by))
	for _, a := range areas.by {
		if class == "" || a.Class == class {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// The areas that exist from the first release (R-ARCH-018). A page that adds an
// area registers it in its own package and seeds the cluster_epoch row of a
// change area in the migration that ships it.
func init() {
	RegisterArea(Area{Name: AreaJobs, Owner: "R-ARCH-016", Class: SignalArea})
	RegisterArea(Area{Name: AreaCache, Owner: "R-ARCH-018", Class: ChangeArea, PayloadVersion: 1})

	// The areas of M1: auth (identity), authz and repo_config. A change area also
	// needs its cluster_epoch row from the migration that ships it.
	// >>> M1 identity
	RegisterArea(Area{Name: AreaAuth, Owner: "R-AUTH-023", Class: ChangeArea, PayloadVersion: 1})
	// <<< M1 identity
	//
	// >>> M1 authz
	RegisterArea(Area{Name: AreaAuthz, Owner: "R-AUTHZ-028", Class: ChangeArea, PayloadVersion: 1})
	// <<< M1 authz
	//
	// >>> M1 repository
	RegisterArea(Area{Name: AreaRepoConfig, Owner: "R-REPO-049", Class: ChangeArea, PayloadVersion: 1})
	// <<< M1 repository
}

// Names of the areas of this package.
const (
	// AreaJobs wakes the workers of a queue; the payload names the queue.
	AreaJobs = "jobs"
	// AreaCache carries system settings and flush requests.
	AreaCache = "cache"
)

// Names of the areas of the M1 packages, declared next to their registration (constants
// between the markers).

// >>> M1 identity

// AreaAuth carries the changes that end, narrow or alter a credential, a
// principal or a runtime authentication setting (R-AUTH-023). The payload
// kinds are in docs/developer/identity.md.
const AreaAuth = "auth"

// <<< M1 identity
//
// >>> M1 authz

// AreaAuthz carries policy, role-binding, exposure-rule and authorization-setting
// changes; its epoch is the authorization epoch (R-AUTHZ-028).
const AreaAuthz = "authz"

// <<< M1 authz
//
// >>> M1 repository

// AreaRepoConfig carries repository rows and virtual membership: create, update, delete
// and every change of an input of the grant index (R-REPO-008, R-AUTHZ-028). The payload
// names the repository id, never a grant. The cluster_epoch row is seeded by migration 00010.
const AreaRepoConfig = "repo_config"

// <<< M1 repository
