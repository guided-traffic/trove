package oci

import (
	"cmp"
	"context"
	"encoding/json"
	"slices"
	"sort"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
)

// The virtual class (R-OCI-027, R-OCI-028). The virtual engine plans a request (the repository's patterns, the pin rules and claims
// of R-REPO-023, the members the caller may read and fill); the format walks the members itself, because what it serves is not a
// document to merge: a manifest and a blob are one object each, a tag is taken from the first member that has it, and the lists are
// unions. The adapter therefore declares the tags lists as the only merged document (Strategy), and the handlers of
// virtual_read.go and virtual_write.go call the same member handlers a direct request uses, on a request derived for the member
// ([formats.Request.Member]) so that the addressed repository of every decision stays the virtual one.

// Virtual returns the format's virtual adapter.
func (f *Format) Virtual() formats.VirtualAdapter { return virtualAdapter{} }

type virtualAdapter struct{}

var _ formats.VirtualAdapter = virtualAdapter{}

// Strategy: the tags lists of the members are the one document that members contribute to (Q-OCI-160); every other path is a manifest,
// a tag or a blob, which one member serves.
func (virtualAdapter) Strategy(p formats.Path) formats.Strategy {
	if _, kind, _, ok := splitLayout(p.Path); ok && kind == fileTaglist {
		return formats.Merge
	}
	return formats.FirstMatch
}

// ArtifactPrefix: the artifacts of an image name are below the name, but the virtual engine's listing of the cached versions serves
// merged documents of versions, and a tags list lists tags, which a member's cache does not hold as artifacts of their own.
func (virtualAdapter) ArtifactPrefix(string) string { return "" }

// LocalDocument is never asked: the merged-metadata cache of the virtual engine is not used for a tags list, which is decided for the
// caller in every member (Q-OCI-160).
func (virtualAdapter) LocalDocument(context.Context, *formats.Request, formats.RepoRef, formats.Path, string) ([]byte, formats.RevSeq, bool, error) {
	return nil, 0, false, errs.E(errs.Internal, errs.CodeInternal, "The OCI format keeps no merged document of its members in the shared cache.")
}

// mergedTaglist is the union of the tags lists of the members: the tags in byte order without repeats, and for each the member that
// supplies it by the per-version source rule (the lowest tier, then position). Members are named once in members, from[i] indexes
// the member of tags[i].
type mergedTaglist struct {
	Name    string   `json:"name"`
	Tags    []string `json:"tags"`
	Members []string `json:"members"`
	From    []int    `json:"from"`
}

// Merge unions the tags lists of the members of a virtual repository (R-OCI-027). The documents are `{"name":…,"tags":[…]}`; the result is
// deterministic, and a tag that several members hold is attributed to the member the tier resolution would pick.
func (virtualAdapter) Merge(_ context.Context, _ *formats.Request, _ formats.Path, parts []formats.MemberDocument) ([]byte, error) {
	order := slices.Clone(parts)
	slices.SortStableFunc(order, func(a, b formats.MemberDocument) int {
		return cmp.Or(cmp.Compare(a.Tier, b.Tier), cmp.Compare(a.Position, b.Position))
	})
	out := mergedTaglist{Tags: []string{}, Members: []string{}, From: []int{}}
	supplier := map[string]int{}
	for i, p := range order {
		var doc struct {
			Name string   `json:"name"`
			Tags []string `json:"tags"`
		}
		if err := json.Unmarshal(p.Doc, &doc); err != nil {
			return nil, errs.E(errs.Internal, errs.CodeInternal, "A member's tags list is not a document the registry reads.", errs.WithCause(err))
		}
		if out.Name == "" {
			out.Name = doc.Name
		}
		out.Members = append(out.Members, p.Member.Name.Key)
		for _, t := range doc.Tags {
			if _, ok := supplier[t]; !ok {
				supplier[t] = i
			}
		}
	}
	for t := range supplier {
		out.Tags = append(out.Tags, t)
	}
	sort.Strings(out.Tags)
	for _, t := range out.Tags {
		out.From = append(out.From, supplier[t])
	}
	body, err := json.Marshal(out)
	if err != nil {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "The registry could not encode its answer.", errs.WithCause(err))
	}
	return body, nil
}
