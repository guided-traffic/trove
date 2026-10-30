package view

import (
	"slices"
	"sort"

	"github.com/guided-traffic/trove/internal/repository"
)

// maxNesting bounds the depth the members of virtual repositories are followed to.
const maxNesting = 16

// Topology is how the repositories hold each other: which repositories hold package
// rows, and which virtual repositories contain them. Package rows exist in local
// and remote repositories only; a virtual repository holds none and shows the rows
// of its members (R-REPO-018).
type Topology struct {
	class map[string]repository.Class
	repos map[string]repository.Repository
	// members are the physical repositories behind a virtual one, recursively.
	members map[string][]string
	// containers are the virtual repositories a physical one is a member of,
	// recursively.
	containers map[string][]string
}

// NewTopology builds the topology from the repositories of a snapshot.
func NewTopology(repos []repository.Repository) Topology {
	t := Topology{
		class:      make(map[string]repository.Class, len(repos)),
		repos:      make(map[string]repository.Repository, len(repos)),
		members:    map[string][]string{},
		containers: map[string][]string{},
	}
	for _, r := range repos {
		t.class[r.Key] = r.Class
		t.repos[r.Key] = r
	}
	for _, r := range repos {
		if r.Class != repository.Virtual {
			continue
		}
		phys := physicalMembers(r, 0)
		t.members[r.Key] = phys
		for _, k := range phys {
			t.containers[k] = append(t.containers[k], r.Key)
		}
	}
	for k := range t.containers {
		sort.Strings(t.containers[k])
	}
	return t
}

func physicalMembers(v repository.Repository, depth int) []string {
	if depth > maxNesting {
		return nil
	}
	var out []string
	for _, m := range v.Members() {
		if m.Class == repository.Virtual {
			out = append(out, physicalMembers(m, depth+1)...)
			continue
		}
		if !slices.Contains(out, m.Key) {
			out = append(out, m.Key)
		}
	}
	return out
}

// Physical lists the keys of the repositories that hold package rows, sorted.
func (t Topology) Physical() []string {
	var out []string
	for k, c := range t.class {
		if c != repository.Virtual {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// Virtual lists the keys of the virtual repositories, sorted.
func (t Topology) Virtual() []string {
	var out []string
	for k, c := range t.class {
		if c == repository.Virtual {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// Class returns the class of a repository; ok is false for a key it does not know.
func (t Topology) Class(key string) (repository.Class, bool) {
	c, ok := t.class[key]
	return c, ok
}

// Expand maps repository keys to the physical repositories whose rows they stand
// for: a virtual repository to its members, any other known repository to itself.
// A key the topology does not know stands for nothing.
func (t Topology) Expand(keys []string) []string {
	var out []string
	for _, k := range keys {
		switch c, ok := t.class[k]; {
		case !ok:
		case c == repository.Virtual:
			out = append(out, t.members[k]...)
		default:
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// Containers returns the virtual repositories that contain a physical one.
func (t Topology) Containers(key string) []string { return t.containers[key] }

// Restricts reports whether the repository has include or exclude patterns
// (R-REPO-005); a repository the topology does not know has none.
func (t Topology) Restricts(key string) bool { return t.repos[key].Restricts() }

// Admits applies the include and exclude patterns of a repository to a normalized
// package name (R-REPO-005): the patterns are routing rules that bind every caller,
// and a version they do not admit is not served, so it is not listed either.
func (t Topology) Admits(key, name string) bool {
	r, ok := t.repos[key]
	return ok && r.Admits(name, false)
}
