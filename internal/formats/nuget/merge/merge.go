// Package merge combines the documents the members of a virtual NuGet repository hold of
// one ID (R-NUGET-017): the version lists of the flat container and the registration
// entries. It reads bytes and writes bytes; it knows no host, no request and no clock, so
// the same member documents always give the same merged bytes.
//
// The merged document is neutral. It holds the entries of the versions with their metadata
// and the member each one came from, and no address: the handler renders the registration
// documents and the version list from it for the repository the client addressed, after
// the merged-metadata cache, so the cached bytes hold no URL a request chose (R-REPO-043).
//
// Per version the entry comes from the member that tier resolution would pick for the
// version's .nupkg (R-REPO-019): the local members in order, then the remote members whose
// cache holds the package, then the remote members that would fetch it. Because the
// metadata winner and the content winner follow the same order, the registration entry a
// client reads describes the bytes it downloads.
package merge

import (
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/nuget/document"
	"github.com/guided-traffic/trove/internal/formats/nuget/version"
)

// Kind is the document a merge combines.
type Kind uint8

const (
	// Flat is the version list of the flat container ({"versions":[…]}).
	Flat Kind = iota + 1
	// Registration is the registration entries of an ID.
	Registration
)

// Tier is where a member sits in the resolution of R-REPO-019 step 6; a lower tier wins.
type Tier uint8

const (
	// Local is a local member.
	Local Tier = iota + 1
	// RemoteCached is a remote member whose cache holds the package of the version.
	RemoteCached
	// Remote is a remote member that would fetch the package from its upstream.
	Remote
)

// Entry is a version of an ID as the merge handles it. For a local member it is a stored
// version; for a remote member the catalog entry of an upstream registration leaf. The
// merged version list uses the Version and Source members alone.
type Entry struct {
	// Version is the URL form (lower-case, normalized, without build metadata), Full the
	// full normalized form the registration shows.
	Version string `json:"v"`
	Full    string `json:"full,omitempty"`
	// DeclaredID is the casing of the ID the version's .nuspec declares.
	DeclaredID string    `json:"id,omitempty"`
	Listed     bool      `json:"listed,omitempty"`
	Prerelease bool      `json:"pre,omitempty"`
	Semver2    bool      `json:"sv2,omitempty"`
	Published  time.Time `json:"published,omitzero"`
	// Types are the lower-case package type names.
	Types []string `json:"types,omitempty"`
	// Metadata is the JSON object of R-NUGET-005 that registration.ParseMetadata reads.
	Metadata json.RawMessage `json:"metadata,omitempty"`
	// Deprecation and Vulnerabilities are the upstream's members, passed through.
	Deprecation     json.RawMessage `json:"deprecation,omitempty"`
	Vulnerabilities json.RawMessage `json:"vulnerabilities,omitempty"`
	// Source is the key of the member the entry came from; the merge sets it.
	Source string `json:"source,omitempty"`
}

// Doc is the document of a local member and the merged document.
type Doc struct {
	Entries []Entry `json:"entries"`
}

// Part is one member's document as the merge receives it.
type Part struct {
	// Member is the member's key.
	Member string
	Tier   Tier
	// Position is the member's place in the resolution order.
	Position int
	Doc      []byte
	// Cached are the versions (URL form) of a remote member whose package is cached.
	Cached map[string]bool
	// Restricted says that the caller may not fill the member's cache: only the cached
	// versions of its document are listed, so that every listed version can be
	// downloaded (R-AUTHZ-008).
	Restricted bool
}

// rank orders the claims on one version: the tier of its package, then the position.
type rank struct{ tier, position int }

func (r rank) less(o rank) bool {
	if r.tier != o.tier {
		return r.tier < o.tier
	}
	return r.position < o.position
}

// Do merges the documents of the members of one ID. Local members hold the neutral Doc
// (flat: the version list of the flat container), remote members the upstream's document
// of the same path. The result is a Doc, ascending by precedence, in which every entry
// names the member it came from.
func Do(kind Kind, parts []Part) ([]byte, error) {
	won := map[string]Entry{}
	ranks := map[string]rank{}
	for _, p := range parts {
		entries, err := decode(kind, p)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if p.Restricted && !p.Cached[e.Version] {
				continue
			}
			r := rank{tier: tierOf(p, e.Version), position: p.Position}
			if old, ok := ranks[e.Version]; ok && !r.less(old) {
				continue
			}
			e.Source = p.Member
			won[e.Version], ranks[e.Version] = e, r
		}
	}
	out := make([]Entry, 0, len(won))
	parsed := make(map[string]version.Version, len(won))
	for v, e := range won {
		pv, err := version.Parse(v)
		if err != nil { // cannot happen: every entry's version was parsed when it was decoded
			return nil, fmt.Errorf("merge: version %q: %w", v, err)
		}
		parsed[v] = pv
		out = append(out, e)
	}
	slices.SortFunc(out, func(a, b Entry) int { return version.Compare(parsed[a.Version], parsed[b.Version]) })
	return document.Marshal(Doc{Entries: out})
}

// tierOf is the tier a part holds a version at.
func tierOf(p Part, v string) int {
	switch {
	case p.Tier == Local:
		return int(Local)
	case p.Tier == RemoteCached, p.Cached[v]:
		return int(RemoteCached)
	}
	return int(Remote)
}

func decode(kind Kind, p Part) ([]Entry, error) {
	switch {
	case kind == Flat:
		return decodeFlat(p.Doc)
	case p.Tier == Local:
		return Decode(p.Doc)
	}
	return DecodeUpstream(p.Doc)
}

// decodeFlat reads the version list of the flat container, which a local member and an
// upstream hold in one form; a version that is no version is dropped.
func decodeFlat(b []byte) ([]Entry, error) {
	var l struct {
		Versions []string `json:"versions"`
	}
	if err := json.Unmarshal(b, &l); err != nil {
		return nil, errs.E(errs.UpstreamFailed, errs.CodeUpstreamFailed, "A version list of a member is no version list.", errs.WithCause(err))
	}
	out := make([]Entry, 0, len(l.Versions))
	for _, s := range l.Versions {
		if v, err := version.Parse(s); err == nil {
			out = append(out, Entry{Version: v.URLForm()})
		}
	}
	return out, nil
}

// Decode reads the neutral document of a local member or a merged document.
func Decode(b []byte) ([]Entry, error) {
	var d Doc
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "A document of a local member cannot be read.", errs.WithCause(err))
	}
	return d.Entries, nil
}

// Encode writes the neutral document of a local member.
func Encode(entries []Entry) ([]byte, error) {
	if entries == nil {
		entries = []Entry{}
	}
	return document.Marshal(Doc{Entries: entries})
}

// EncodeFlat writes the version list of a local member in the form of the flat container.
func EncodeFlat(versions []string) ([]byte, error) { return document.VersionList(versions) }
