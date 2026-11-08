package registrytoken

import (
	"strings"

	"github.com/guided-traffic/trove/internal/qname"
)

// Actions is a set of the actions of the distribution token protocol (R-OCI-006): pull, push and delete.
type Actions uint8

// The actions. The zero value is the empty set.
const (
	Pull Actions = 1 << iota
	Push
	Delete
	// All is "*" of a scope entry.
	All = Pull | Push | Delete
)

// Has reports whether every action of b is in a.
func (a Actions) Has(b Actions) bool { return b != 0 && a&b == b }

// String renders the set in the order of the protocol, "pull,push,delete"; "" for the empty set.
func (a Actions) String() string {
	var parts []string
	for _, x := range []struct {
		bit  Actions
		name string
	}{{Pull, "pull"}, {Push, "push"}, {Delete, "delete"}} {
		if a&x.bit != 0 {
			parts = append(parts, x.name)
		}
	}
	return strings.Join(parts, ",")
}

// parseActions reads the action list of a scope entry: pull, push, delete and "*", comma-separated. An action the protocol
// does not name is ignored (it grants nothing); the result is the set of the known ones.
func parseActions(s string) Actions {
	var a Actions
	for _, w := range strings.Split(s, ",") {
		switch strings.TrimSpace(w) {
		case "pull":
			a |= Pull
		case "push":
			a |= Push
		case "delete":
			a |= Delete
		case "*":
			a |= All
		}
	}
	return a
}

// Entry is one entry of a grant: the actions on one image name of one repository, as the client addressed them (the cache
// view of a remote repository by its base repository). A push through a virtual repository names the virtual repository;
// the decision point decides it on the deploy target.
type Entry struct {
	Repo    qname.Repo
	Name    string
	Actions Actions
}

// Grant is what a registry token allows at most (R-OCI-005): at most [MaxEntries] entries and the catalog. A grant is a
// ceiling, never a permission of its own: every request is decided again by the decision point.
type Grant struct {
	Entries []Entry
	// Catalog is the grant registry:catalog:* (R-OCI-017).
	Catalog bool
}

// Allows reports whether the grant holds the action for the image name of the repository: the check of R-OCI-005 "Use"
// that the registry root's enforcement point makes before the decision point. A missing action is the 401 with
// error="insufficient_scope" of R-OCI-004.
func (g Grant) Allows(repo qname.Repo, name string, a Actions) bool {
	for _, e := range g.Entries {
		if e.Repo == repo && e.Name == name && e.Actions.Has(a) {
			return true
		}
	}
	return false
}

// Add merges an entry into the grant: two entries of one name are one entry with both action sets.
func (g *Grant) Add(e Entry) {
	for i := range g.Entries {
		if g.Entries[i].Repo == e.Repo && g.Entries[i].Name == e.Name {
			g.Entries[i].Actions |= e.Actions
			return
		}
	}
	g.Entries = append(g.Entries, e)
}

// FullName is the name of an image as a client sends it on an unbound host, "<projectKey>/<repoKey>/<name>" (R-OCI-001).
func FullName(repo qname.Repo, name string) string { return repo.Project + "/" + repo.Key + "/" + name }
