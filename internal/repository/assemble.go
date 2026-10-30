package repository

// Assemble returns a copy of a virtual repository that holds the given members, deploy target and
// merged-metadata period the way the snapshot links them: Members, DeployTarget and Virtual answer
// from them. Production code never calls it, the snapshot is the only source of a repository with
// members; it exists so that the packages that resolve through a virtual repository can test
// against one without a database (internal/virtual, internal/formathost). The pin rules are not
// assembled: routing is replaced in such tests.
func Assemble(v Repository, members []Repository, target *Repository, mergedTTLSeconds int) Repository {
	keys := make([]string, len(members))
	ids := make(map[int64]bool, len(members))
	for i, m := range members {
		keys[i] = m.Key
		ids[m.rid] = true
	}
	st := &virtualState{
		settings:  VirtualSettings{Members: keys, MergedMetadataTTLSeconds: &mergedTTLSeconds, Pinning: Pinning{Rules: []PinRule{}}},
		members:   append([]Repository(nil), members...),
		memberIDs: ids,
	}
	if target != nil {
		t := *target
		st.target = &t
		k := target.Key
		st.settings.DeployTarget = &k
	}
	v.virt = st
	return v
}
