package repository

import (
	"bytes"
	"encoding/json"
	"net/url"
	"reflect"
	"slices"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/mirror/discover"
)

// The mirror mode of a remote repository (repositories-and-proxy.md R-REPO-050 to R-REPO-052): remote.mode and the
// members of remote.mirror, validated in the pass that validates the rest of the remote section and stored canonical.

// The bounds of remote.mirror (R-REPO-050).
const (
	minMirrorInterval     = 300
	maxMirrorInterval     = 604800
	minFullPassInterval   = 86400
	maxFullPassInterval   = 7776000
	maxMirrorConcurrency  = 64
	maxVersionsPerPackage = 10000
	minMirrorMaxBytes     = 1 << 20
	maxMirrorMaxBytes     = 1 << 62
)

// WarnMirrorUnbounded is the warning of a mirror of an upstream that is not the format's well-known one, without a
// bounding include list and without maxBytes (R-REPO-052, Q-REPO-132).
const WarnMirrorUnbounded = errs.CodeMirrorUnbounded

// mirror resolves remote.mode and remote.mirror.
func (r *remoteResolver) mirror(s *RemoteSettings, in remoteInput) {
	s.Mode = stringOr(in.Mode, ModePullThrough)
	r.oneOf("/remote/mode", s.Mode, ModePullThrough, ModeMirror)
	var m mirrorInput
	if in.Mirror != nil {
		m = *in.Mirror
	}
	d := DefaultMirrorSettings()
	s.Mirror = MirrorSettings{
		Paused:                  boolOr(m.Paused, d.Paused),
		IntervalSeconds:         intOr(m.IntervalSeconds, d.IntervalSeconds),
		FullPassIntervalSeconds: intOr(m.FullPassIntervalSeconds, d.FullPassIntervalSeconds),
		Discovery:               stringOr(m.Discovery, d.Discovery),
		Concurrency:             intOr(m.Concurrency, d.Concurrency),
		MaxVersionsPerPackage:   intOr(m.MaxVersionsPerPackage, d.MaxVersionsPerPackage),
		IncludePrereleases:      boolOr(m.IncludePrereleases, d.IncludePrereleases),
		MaxBytes:                m.MaxBytes,
	}
	r.mirrorRanges(s.Mirror)
	if s.Mode != ModeMirror {
		return
	}
	if !r.info.Mirror {
		r.notApplicable("/remote/mode", "the "+string(r.info.ID)+" format cannot discover the packages of its upstream, so a remote repository of it cannot be a mirror")
		return
	}
	r.checkMirrorURL(s)
	r.checkMirrorBound(s)
}

// checkMirrorURL refuses a query in the url of a mirror (Q-REPO-194): the formats' requests drop it, so a url that
// differs from the well-known upstream by a query would be that upstream without the bound of R-REPO-052.
func (r *remoteResolver) checkMirrorURL(s *RemoteSettings) {
	if u, err := url.Parse(s.URL); err == nil && (u.RawQuery != "" || u.ForceQuery) {
		r.v.add("/remote/url", codeInvalidValue, "a mirror's url carries no query: the format's requests do not send it")
	}
}

func (r *remoteResolver) mirrorRanges(m MirrorSettings) {
	r.rng("/remote/mirror/intervalSeconds", m.IntervalSeconds, minMirrorInterval, maxMirrorInterval)
	if f := m.FullPassIntervalSeconds; f != 0 {
		r.rng("/remote/mirror/fullPassIntervalSeconds", f, minFullPassInterval, maxFullPassInterval)
	}
	r.oneOf("/remote/mirror/discovery", m.Discovery, DiscoveryAuto, DiscoveryFeed, DiscoveryListing, DiscoveryNames)
	r.rng("/remote/mirror/concurrency", m.Concurrency, 1, maxMirrorConcurrency)
	r.rng("/remote/mirror/maxVersionsPerPackage", m.MaxVersionsPerPackage, 0, maxVersionsPerPackage)
	if b := m.MaxBytes; b != nil && (*b < minMirrorMaxBytes || *b > maxMirrorMaxBytes) {
		r.v.add("/remote/mirror/maxBytes", codeOutOfRange, "must be at least 1 MiB (1048576), at most 2^62, or null for no bound of its own")
	}
}

// checkMirrorBound applies R-REPO-052: a mirror of the format's well-known upstream needs include patterns that each
// bound it, or maxBytes; any other upstream without either is saved with a warning.
func (r *remoteResolver) checkMirrorBound(s *RemoteSettings) {
	if s.Mirror.MaxBytes != nil || boundingIncludes(r.include) {
		return
	}
	known, _ := normalizeUpstreamURL(r.info.DefaultUpstreamURL)
	if u, err := url.Parse(s.URL); known != "" && err == nil && discover.SameUpstream(u, known) {
		r.v.add("/remote/mode", errs.CodeMirrorUnbounded,
			"a mirror of "+known+" needs a bound: include patterns that each have a literal prefix (such as @types/* or Microsoft.Extensions.*), or remote.mirror.maxBytes")
		return
	}
	r.warn(WarnMirrorUnbounded, "/remote/mode",
		"the mirror has neither include patterns with a literal prefix nor remote.mirror.maxBytes: it stores everything the upstream offers")
}

// boundingIncludes reports whether a non-empty include list bounds a mirror: every pattern has a literal prefix
// (discover.Bounding).
func boundingIncludes(include []string) bool {
	if len(include) == 0 {
		return false
	}
	for _, p := range include {
		if !discover.Bounding(p) {
			return false
		}
	}
	return true
}

// checkDelegatedMirror applies Q-REPO-142 to a project Owner's write: the mode and the mirror members stay as
// stored (the defaults on a create), except that paused may change and the mode may go back to pullThrough.
func checkDelegatedMirror(v *validation, stored, next *RemoteSettings) {
	base := RemoteSettings{Mode: ModePullThrough, Mirror: DefaultMirrorSettings()}
	if stored != nil {
		base = *stored
	}
	if next.Mode != base.Mode && next.Mode != ModePullThrough {
		v.add("/remote/mode", errs.CodeMirrorRestricted,
			"only a holder of repositories:write-config makes a remote repository a mirror; a project Owner may turn it back to pullThrough")
	}
	if !sameMirrorButPause(base.Mirror, next.Mirror) {
		v.add("/remote/mirror", errs.CodeMirrorRestricted,
			"a project Owner keeps the mirror settings an administrator of trove chose; it may change paused only")
	}
}

// checkDelegatedMirrorScope applies Q-REPO-193 to a project Owner's update of a repository that is a mirror
// and stays one: what the mirror covers (includePatterns, excludePatterns) and where it reads from (remote.url,
// remote.extraHosts) are the administrator's. A write that turns the mode back to pullThrough may change them.
func checkDelegatedMirrorScope(v *validation, stored, next *RemoteSettings, cur, nd *Document) {
	if stored == nil || stored.Mode != ModeMirror || next.Mode != ModeMirror {
		return
	}
	const why = "a project Owner keeps what an administrator of trove chose for a mirror; turn the mode back to pullThrough to change it"
	if !sameStrings(cur.IncludePatterns, nd.IncludePatterns) {
		v.add("/includePatterns", errs.CodeMirrorRestricted, why)
	}
	if !sameStrings(cur.ExcludePatterns, nd.ExcludePatterns) {
		v.add("/excludePatterns", errs.CodeMirrorRestricted, why)
	}
	if stored.URL != next.URL {
		v.add("/remote/url", errs.CodeMirrorRestricted, why)
	}
	if !reflect.DeepEqual(stored.ExtraHosts, next.ExtraHosts) {
		v.add("/remote/extraHosts", errs.CodeMirrorRestricted, why)
	}
}

// checkDelegatedFormatMirror applies Q-OCI-22 to a project Owner's write: what a mirror of the format keeps is the format's member
// `<format>.mirror` (oci.mirror), and it stays as stored, like remote.mirror (the format's defaults on a create), in either mode: an
// Owner who could widen it would decide what a mirror stores, which the rule of Q-REPO-142 reserves to an administrator. cur is the
// stored document, nil on a create.
func checkDelegatedFormatMirror(v *validation, cur, nd *Document) {
	var curMirror json.RawMessage
	if cur != nil {
		curMirror = mirrorMemberOfFormat(cur)
	}
	if !bytes.Equal(curMirror, mirrorMemberOfFormat(nd)) {
		v.add("/"+nd.Format+"/mirror", errs.CodeMirrorRestricted,
			"a project Owner keeps the mirror settings an administrator of trove chose; the format's mirror member is not its to change")
	}
}

// mirrorMemberOfFormat is the canonical `mirror` member of the settings member of the document's format; nil when there is none.
func mirrorMemberOfFormat(d *Document) json.RawMessage {
	for _, m := range formatMembers() {
		if m.format != d.Format {
			continue
		}
		var doc map[string]json.RawMessage
		if raw := m.read(&d.Settings); len(raw) == 0 || json.Unmarshal(raw, &doc) != nil {
			return nil
		}
		return canonicalObject(doc["mirror"])
	}
	return nil
}

func sameStrings(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

func sameMirrorButPause(a, b MirrorSettings) bool {
	sameBytes := (a.MaxBytes == nil) == (b.MaxBytes == nil) && (a.MaxBytes == nil || *a.MaxBytes == *b.MaxBytes)
	a.Paused, b.Paused, a.MaxBytes, b.MaxBytes = false, false, nil, nil
	return sameBytes && a == b
}
