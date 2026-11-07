package view

import (
	"slices"
	"strings"

	"github.com/guided-traffic/trove/internal/qname"
	"github.com/guided-traffic/trove/internal/repository"
)

// maxNesting bounds the depth the members of virtual repositories are followed to.
const maxNesting = 16

// Topology is how the repositories hold each other: which repositories hold package
// rows, and which virtual repositories contain them. Package rows exist in local
// and remote repositories only; a virtual repository holds none and shows the rows
// of its members (R-REPO-018).
//
// Repositories are keyed by their database ID, the key the statements and the rows they
// return carry (R-VULN-028); a name is looked up once per request ([Topology.ID],
// [Topology.IDs]) and rendered at the edge ([Topology.Name]), and no row is ever parsed
// back into one.
type Topology struct {
	repos map[int64]repository.Repository
	ids   map[qname.Repo]int64
	// physical and virtual are the IDs of the repositories of that kind, sorted by qualified name.
	physical, virtual []int64
	// rank is the position of a repository in the order of qualified names.
	rank map[int64]int
	// members are the physical repositories behind a virtual one, recursively.
	members map[int64][]int64
	// containers are the virtual repositories a physical one is a member of, recursively, sorted by qualified name.
	containers map[int64][]int64
}

// NewTopology builds the topology from the repositories of a snapshot.
func NewTopology(repos []repository.Repository) Topology {
	t := Topology{
		repos:      make(map[int64]repository.Repository, len(repos)),
		ids:        make(map[qname.Repo]int64, len(repos)),
		rank:       make(map[int64]int, len(repos)),
		members:    map[int64][]int64{},
		containers: map[int64][]int64{},
	}
	byName := slices.Clone(repos)
	slices.SortFunc(byName, func(a, b repository.Repository) int { return CompareNames(a.Name, b.Name) })
	for i, r := range byName {
		id := r.NumericID()
		t.repos[id] = r
		t.ids[r.Name] = id
		t.rank[id] = i
		if r.Class == repository.Virtual {
			t.virtual = append(t.virtual, id)
		} else {
			t.physical = append(t.physical, id)
		}
	}
	for _, id := range t.virtual {
		phys := physicalMembers(t.repos[id], 0)
		t.members[id] = phys
		for _, m := range phys {
			t.containers[m] = append(t.containers[m], id)
		}
	}
	return t
}

// CompareNames orders repository names the way their qualified texts sort.
func CompareNames(a, b qname.Repo) int {
	return strings.Compare(a.String(), b.String())
}

func physicalMembers(v repository.Repository, depth int) []int64 {
	if depth > maxNesting {
		return nil
	}
	var out []int64
	for _, m := range v.Members() {
		if m.Class == repository.Virtual {
			out = append(out, physicalMembers(m, depth+1)...)
			continue
		}
		if id := m.NumericID(); !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// ID returns the database ID of the repository of a name, 0 for a name the topology does not know.
func (t Topology) ID(name qname.Repo) int64 { return t.ids[name] }

// IDs returns the database IDs of the repositories of the given names; a name the topology does not know is left out.
func (t Topology) IDs(names []qname.Repo) []int64 {
	out := make([]int64, 0, len(names))
	for _, n := range names {
		if id := t.ID(n); id != 0 {
			out = append(out, id)
		}
	}
	return out
}

// Name returns the name of the repository of an ID, the zero name for an ID the topology does not know.
func (t Topology) Name(id int64) qname.Repo { return t.repos[id].Name }

// Names returns the names of the repositories of the given IDs, in that order.
func (t Topology) Names(ids []int64) []qname.Repo {
	out := make([]qname.Repo, len(ids))
	for i, id := range ids {
		out[i] = t.Name(id)
	}
	return out
}

// Texts returns the qualified texts of the repositories of the given IDs, for the format adapters, whose statements take the
// text of a repository (formats.ViewAdapter).
func (t Topology) Texts(ids []int64) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = t.Name(id).String()
	}
	return out
}

// Repository returns the repository of an ID; ok is false for an ID the topology does not know.
func (t Topology) Repository(id int64) (repository.Repository, bool) {
	r, ok := t.repos[id]
	return r, ok
}

// Less orders two repositories by their qualified names.
func (t Topology) Less(a, b int64) bool { return t.rank[a] < t.rank[b] }

// Physical lists the IDs of the repositories that hold package rows, sorted by name.
func (t Topology) Physical() []int64 { return slices.Clone(t.physical) }

// Virtual lists the IDs of the virtual repositories, sorted by name.
func (t Topology) Virtual() []int64 { return slices.Clone(t.virtual) }

// Class returns the class of a repository; ok is false for an ID it does not know.
func (t Topology) Class(id int64) (repository.Class, bool) {
	r, ok := t.repos[id]
	return r.Class, ok
}

// Expand maps repository IDs to the physical repositories whose rows they stand
// for: a virtual repository to its members, any other known repository to itself.
// An ID the topology does not know stands for nothing. The result is sorted by ID.
func (t Topology) Expand(ids []int64) []int64 {
	var out []int64
	for _, id := range ids {
		switch c, ok := t.Class(id); {
		case !ok:
		case c == repository.Virtual:
			out = append(out, t.members[id]...)
		default:
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// InProject turns the project of a filter into a repository filter: the physical repositories of the project,
// intersected with the repositories the filter names (a virtual repository standing for its members). A filter
// without a project is returned as it is. A project whose repositories the filter excludes lists nothing.
func (t Topology) InProject(f Filter) Filter {
	if f.Project == "" {
		return f
	}
	physical := t.physical
	if f.HasRepositories {
		physical = t.Expand(t.IDs(f.Repositories))
	}
	var in []qname.Repo
	for _, id := range physical {
		if n := t.Name(id); n.Project == f.Project {
			in = append(in, n)
		}
	}
	f.Repositories, f.HasRepositories, f.Project = in, true, ""
	return f
}

// Containers returns the virtual repositories that contain a physical one, sorted by name.
func (t Topology) Containers(id int64) []int64 { return t.containers[id] }

// Members returns the physical repositories behind a virtual one.
func (t Topology) Members(id int64) []int64 { return t.members[id] }

// Restricts reports whether the repository has include or exclude patterns
// (R-REPO-005); a repository the topology does not know has none.
func (t Topology) Restricts(id int64) bool { return t.repos[id].Restricts() }

// Admits applies the include and exclude patterns of a repository to a normalized
// package name (R-REPO-005): the patterns are routing rules that bind every caller,
// and a version they do not admit is not served, so it is not listed either.
func (t Topology) Admits(id int64, name string) bool {
	r, ok := t.repos[id]
	return ok && r.Admits(name, false)
}
