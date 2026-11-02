package merge

import (
	"github.com/guided-traffic/trove/internal/formats/nuget/registration"
	"github.com/guided-traffic/trove/internal/formats/nuget/remote"
)

// FromRemote converts the versions of an upstream's registration, as the remote repository reads
// them from its stored index and pages (remote.Convert, R-NUGET-015), into the entries of the
// merge (Q-NUGET-82). The conversion is the remote's own, so a version shows the same entry
// through the remote repository and through a virtual repository that has it as a member: the
// declared ID, or the ID of the request where the upstream declares none, the SemVer 2.0.0 flag
// over the version and its dependency ranges, the metadata, and the members the remote passes
// through.
func FromRemote(vs []remote.Version) []Entry {
	out := make([]Entry, len(vs))
	for i, v := range vs {
		r := v.Row
		out[i] = Entry{
			Version: r.LowerVersion, Full: r.FullVersion, DeclaredID: r.DeclaredID, Listed: r.Listed,
			Prerelease: r.IsPrerelease, Semver2: r.IsSemver2, Published: r.PublishedAt,
			Types: r.PackageTypes, Metadata: r.Metadata,
			Deprecation: v.Extras.Deprecation, Vulnerabilities: v.Extras.Vulnerabilities, Other: otherOf(v.Extras.Other),
		}
	}
	return out
}

func otherOf(ms []registration.Member) []Member {
	if len(ms) == 0 {
		return nil
	}
	out := make([]Member, len(ms))
	for i, m := range ms {
		out[i] = Member{Key: m.Key, Value: m.Value}
	}
	return out
}

// Members returns the kept members of an entry in the renderer's form.
func (e Entry) Members() []registration.Member {
	if len(e.Other) == 0 {
		return nil
	}
	out := make([]registration.Member, len(e.Other))
	for i, m := range e.Other {
		out[i] = registration.Member{Key: m.Key, Value: m.Value}
	}
	return out
}
