package npm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/npm/rows"
	"github.com/guided-traffic/trove/internal/formats/npm/semver"
)

// npm on an endpoint with exposure rules (R-NPM-048, R-EP-018). The adapter has two
// halves. The first is the version syntax the decision point compiles rules with
// (catalog.Versioning: ValidateRange, Prerelease, InRange) and the dimensions:
// formats.Register puts them into the catalog, and authz reads them there. The
// second is the filter: a document lists only what the endpoint exposes of the
// addressed repository, after the merge of a virtual repository and the rewrite of
// a remote document, before compression.
//
// The filter decides once, in exposedVersions and exposedTags, and is applied in two
// places: exposeRows on the rows of a local document before it is rendered (the
// handlers), and FilterDocument on the bytes of a merged or rewritten document (the
// remote and virtual handlers). Both keep the same versions and tags, so a document
// does not depend on the class that served it.

// maxRanges bounds the parsed ranges InRange keeps. A range is typed by an
// administrator and there are at most 1,000 rules per endpoint; the bound only keeps
// a replica's memory independent of how often rules were edited.
const maxRanges = 4096

// exposure is the npm ExposureAdapter. It keeps the ranges it parsed: the decision
// point asks InRange for every version and rule, and a range is parsed once instead
// of on every call. The memo is a field of the Format, never a package variable
// (R-ARCH-032).
type exposure struct {
	mu     sync.RWMutex
	ranges map[string]*semver.Range
}

var _ formats.ExposureAdapter = (*exposure)(nil)

func newExposure() *exposure { return &exposure{ranges: map[string]*semver.Range{}} }

// Exposure returns the format's exposure adapter (R-EP-018): npm repositories can be
// served on an endpoint with exposure rules.
func (f *Format) Exposure() formats.ExposureAdapter { return f.exposure }

// ValidateRange checks the range of a rule (R-NPM-048, Q-NPM-16): node-semver's
// strict syntax, accepted with and without includePrerelease, at most 4096 bytes.
func (*exposure) ValidateRange(expr string) error {
	if _, err := semver.ParseForExposure(expr); err != nil {
		return fmt.Errorf("%q is not a version range in node-semver's strict syntax of at most %d bytes", expr, semver.MaxRangeLength)
	}
	return nil
}

// Prerelease reports whether a version has a pre-release part. A version is read as
// node-semver reads the versions of documents (Q-NPM-15); ok is false for a text
// that is no version, which a rule without prereleases therefore never exposes.
func (*exposure) Prerelease(version string) (pre, ok bool) {
	v, err := semver.ParseNode(version, false)
	if err != nil {
		return false, false
	}
	return v.IsPrerelease(), true
}

// InRange reports whether a version lies in a validated range, evaluated with
// includePrerelease: the rule's prereleases flag, not the range, decides whether a
// pre-release is exposed (Q-NPM-16). A range that does not parse admits nothing.
func (x *exposure) InRange(version, expr string) bool {
	r := x.rangeOf(expr)
	return r != nil && r.SatisfiesString(version)
}

// Dimensions: npm rules narrow packages, versions and dist-tags.
func (*exposure) Dimensions() formats.ExposureDimensions {
	return formats.ExposureDimensions{Packages: true, Versions: true, Tags: true}
}

// rangeOf returns the parsed range of a rule's text, nil when it does not parse.
func (x *exposure) rangeOf(expr string) *semver.Range {
	x.mu.RLock()
	r, ok := x.ranges[expr]
	x.mu.RUnlock()
	if ok {
		return r
	}
	r, err := semver.ParseForExposure(expr)
	if err != nil {
		r = nil
	}
	x.mu.Lock()
	if len(x.ranges) >= maxRanges {
		clear(x.ranges)
	}
	x.ranges[expr] = r
	x.mu.Unlock()
	return r
}

// FilterDocument removes from a merged or rewritten document what the view does not
// expose (R-NPM-048). It filters the package document (the full and the abbreviated
// form, path "{name}") and the single-version document of a version (path
// "{name}/{version}"); any other document is refused, so that a caller cannot pass a
// document it was not written for and serve it unfiltered. A dist-tag map and the
// version a tag names are read from the filtered package document, because only
// that one knows where latest moved. Nothing exposed is the 404 of a missing package
// or version. A nil view filters nothing.
func (x *exposure) FilterDocument(_ context.Context, p formats.Path, doc []byte, view formats.ExposureView) ([]byte, error) {
	if view == nil {
		return doc, nil
	}
	name, ref, ok := documentOf(p)
	if !ok {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "The npm exposure filter does not filter the document at "+p.Path+".")
	}
	if !view.Package(name) {
		return nil, notFound()
	}
	if ref != "" {
		return filterVersionDocument(name, ref, doc, view)
	}
	return filterPackageDocument(name, doc, view)
}

// documentOf reads the package name and the version of a document's canonical path:
// "{name}" or "{name}/{ref}". ok is false for every other path.
func documentOf(p formats.Path) (name, ref string, ok bool) {
	segs := strings.Split(p.Path, "/")
	n := 1
	if strings.HasPrefix(segs[0], "@") {
		n = 2
	}
	if len(segs) < n || len(segs) > n+1 || segs[0] == "-" {
		return "", "", false
	}
	name = strings.Join(segs[:n], "/")
	if len(segs) == n+1 {
		ref = segs[n]
		if ref == "" || ref == "-" {
			return "", "", false
		}
	}
	return name, ref, name != ""
}

// filterVersionDocument answers a single-version document: the document itself when
// its version is exposed (and, for a tag other than latest, the tag is kept), else the
// 404 of a missing version. latest is re-pointed on the package document, which a
// single-version document cannot do; its caller resolves latest there.
func filterVersionDocument(name, ref string, doc []byte, view formats.ExposureView) ([]byte, error) {
	var v struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(doc, &v); err != nil {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "The version document is not a JSON object.", errs.WithCause(err))
	}
	exposed := view.Version(name, v.Version)
	if _, isVersion := canonicalVersion(ref); !isVersion && ref != "latest" {
		exposed = exposed && view.Tag(name, ref, v.Version)
	}
	if !exposed {
		return nil, notFound()
	}
	return doc, nil
}

// exposedVersions keeps the versions the view exposes, in their order.
func exposedVersions(view formats.ExposureView, name string, versions []string) (kept []string, keep map[string]bool) {
	keep = make(map[string]bool, len(versions))
	for _, v := range versions {
		if view.Version(name, v) {
			kept = append(kept, v)
			keep[v] = true
		}
	}
	return kept, keep
}

// exposedTags is the dist-tag rule of R-NPM-048: a tag other than latest stays when
// the rules admit its name and the version it points to is exposed, and is never
// moved; latest is not subject to distTags, keeps its version when that is exposed,
// and otherwise moves to the highest exposed version that is not a pre-release, or
// to the highest exposed version when all are pre-releases (the rule of R-NPM-016).
// A document whose latest is missing gets one by the same rule.
func exposedTags(view formats.ExposureView, name string, tags map[string]string, kept []string, keep map[string]bool) map[string]string {
	out := make(map[string]string, len(tags))
	for tag, v := range tags {
		if tag != "latest" && keep[v] && view.Tag(name, tag, v) {
			out[tag] = v
		}
	}
	if l, ok := tags["latest"]; ok && keep[l] {
		out["latest"] = l
	} else if l, ok := highestText(kept); ok {
		out["latest"] = l
	}
	return out
}

// highestText is highest over the texts of a document: the highest version that is
// not a pre-release, else the highest version, returned as written. Texts that are
// no version are passed over.
func highestText(vs []string) (string, bool) {
	var best, bestPre string
	var bv, bp semver.Version
	for _, s := range vs {
		v, err := semver.ParseNode(s, false)
		if err != nil {
			continue
		}
		switch {
		case !v.IsPrerelease() && (best == "" || semver.Compare(v, bv) > 0):
			best, bv = s, v
		case v.IsPrerelease() && (bestPre == "" || semver.Compare(v, bp) > 0):
			bestPre, bp = s, v
		}
	}
	switch {
	case best != "":
		return best, true
	case bestPre != "":
		return bestPre, true
	}
	return "", false
}

// exposeRows applies the filter to the rows of a local document before it is
// rendered: the versions, the tags and, when latest moved, the README of the former
// latest, which belongs to a version the endpoint may hide (Q-NPM-39). It reports
// false when nothing of the package is exposed. The time map and the maintainers are
// rendered from the versions that remain; created and modified stay those of the
// package (R-NPM-012).
func exposeRows(view formats.ExposureView, name string, p *rows.LocalPackage) bool {
	if !view.Package(name) {
		return false
	}
	versions := make([]string, len(p.Versions))
	for i := range p.Versions {
		versions[i] = p.Versions[i].Version
	}
	kept, keep := exposedVersions(view, name, versions)
	if len(kept) == 0 {
		return false
	}
	p.Versions = keepIf(p.Versions, func(v rows.LocalVersion) bool { return keep[v.Version] })
	tags := exposedTags(view, name, p.Tags, kept, keep)
	if tags["latest"] != p.Tags["latest"] {
		p.Readme, p.ReadmeFilename = "", ""
	}
	p.Tags = tags
	return true
}

func keepIf[T any](in []T, keep func(T) bool) []T {
	out := make([]T, 0, len(in))
	for _, x := range in {
		if keep(x) {
			out = append(out, x)
		}
	}
	return out
}

// viewOf returns the exposure view of a request, nil when the endpoint does not
// restrict. An endpoint with rules whose view the request lacks exposes nothing
// (fail closed).
func viewOf(rq *formats.Request) formats.ExposureView {
	if v := rq.Endpoint.Exposure; v != nil {
		return v
	}
	if rq.Endpoint.Rules {
		return hidesAll{}
	}
	return nil
}

// hidesAll is the view that exposes nothing.
type hidesAll struct{}

func (hidesAll) Package(string) bool             { return false }
func (hidesAll) Version(string, string) bool     { return false }
func (hidesAll) Tag(string, string, string) bool { return false }

// readOnly is the refusal of a write's first step on an endpoint with exposure
// rules (R-EP-013, R-NPM-048): the read of ?write=true. The decision point refuses it
// as the write it prepares, which records deny.endpoint_read_only; the error below
// is the same answer where no ceiling decided.
func readOnly(ctx context.Context, rq *formats.Request, name string) error {
	if err := rq.Host.Authorize(ctx, rq, formats.Target{Package: name, Actions: []formats.Action{formats.ActionRead, formats.ActionDeploy}}); err != nil {
		return err
	}
	info, _ := errs.Lookup(errs.CodeEndpointReadOnly)
	return errs.E(errs.Forbidden, errs.CodeEndpointReadOnly, info.Detail)
}

// The fields of the full document that come from the version latest points to
// (R-NPM-010), and the README, which the filter replaces when it moves latest.
func topLevelOfLatest() []string {
	return []string{"description", "keywords", "license", "homepage", "repository", "bugs", "author", "readme", "readmeFilename"}
}

// filterPackageDocument filters a full or abbreviated package document. Members are
// read in order with their raw values; a member that occurs twice counts with its
// last value at the place of its first, as JSON.parse reads it, so no duplicate can
// carry an unfiltered copy past the filter. Members the filter does not know are
// kept as they are.
func filterPackageDocument(name string, doc []byte, view formats.ExposureView) ([]byte, error) {
	top, err := readObject(doc)
	if err != nil {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "The package document is not a JSON object.", errs.WithCause(err))
	}
	var versions []member
	if raw, ok := top.get("versions"); ok {
		if versions, err = readMembers(raw); err != nil {
			return nil, errs.E(errs.Internal, errs.CodeInternal, "The versions of the package document are not an object.", errs.WithCause(err))
		}
	}
	names := make([]string, len(versions))
	for i, m := range versions {
		names[i] = m.key
	}
	kept, keep := exposedVersions(view, name, names)
	if len(kept) == 0 {
		return nil, notFound()
	}
	tags := map[string]string{}
	if raw, ok := top.get("dist-tags"); ok {
		// A tag whose value is no string points to nothing a client can install.
		var all map[string]any
		if err := json.Unmarshal(raw, &all); err != nil {
			return nil, errs.E(errs.Internal, errs.CodeInternal, "The dist-tags of the package document are not an object.", errs.WithCause(err))
		}
		for k, v := range all {
			if s, ok := v.(string); ok {
				tags[k] = s
			}
		}
	}
	filtered := exposedTags(view, name, tags, kept, keep)
	keptVersions := keepIf(versions, func(m member) bool { return keep[m.key] })

	if err := top.set("versions", writeMembers(keptVersions)); err != nil {
		return nil, err
	}
	if err := top.setValue("dist-tags", filtered); err != nil {
		return nil, err
	}
	if raw, ok := top.get("time"); ok {
		t, err := readMembers(raw)
		if err != nil {
			return nil, errs.E(errs.Internal, errs.CodeInternal, "The time of the package document is not an object.", errs.WithCause(err))
		}
		// created and modified keep the values of the whole document (R-NPM-012); every
		// other key is a version, kept only when the version is.
		t = keepIf(t, func(m member) bool { return m.key == "created" || m.key == "modified" || keep[m.key] })
		if err := top.set("time", writeMembers(t)); err != nil {
			return nil, err
		}
	}
	if _, ok := top.get("maintainers"); ok {
		if err := top.setValue("maintainers", publishersOf(keptVersions)); err != nil {
			return nil, err
		}
	}
	// The fields of latest, in a full document, when the filter moved it.
	if _, full := top.get("_id"); full && filtered["latest"] != tags["latest"] {
		var latest map[string]json.RawMessage
		for _, m := range keptVersions {
			if m.key == filtered["latest"] {
				_ = json.Unmarshal(m.raw, &latest) // an entry that is no object has no fields to give
			}
		}
		for _, k := range topLevelOfLatest() {
			if raw, ok := latest[k]; ok {
				if err := top.set(k, raw); err != nil {
					return nil, err
				}
			} else {
				top.remove(k)
			}
		}
	}
	return top.bytes(), nil
}

// publishersOf lists the distinct publishers (_npmUser.name) of the versions, by
// name and without e-mail, as the local rendering does (R-NPM-010).
func publishersOf(versions []member) []map[string]string {
	seen := map[string]bool{}
	var names []string
	for _, m := range versions {
		var v struct {
			User struct {
				Name string `json:"name"`
			} `json:"_npmUser"`
		}
		if json.Unmarshal(m.raw, &v) == nil && v.User.Name != "" && !seen[v.User.Name] {
			seen[v.User.Name] = true
			names = append(names, v.User.Name)
		}
	}
	sort.Strings(names)
	out := make([]map[string]string, len(names))
	for i, n := range names {
		out[i] = map[string]string{"name": n}
	}
	return out
}

// member is one member of a JSON object with its raw value.
type member struct {
	key string
	raw json.RawMessage
}

// object is a JSON object whose members keep their order.
type object struct{ members []member }

func readObject(doc []byte) (*object, error) {
	ms, err := readMembers(doc)
	if err != nil {
		return nil, err
	}
	return &object{members: ms}, nil
}

// readMembers reads the members of a JSON object in order. A key that occurs again
// keeps the place of its first occurrence and takes the later value (JSON.parse).
func readMembers(raw []byte) ([]member, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, errors.New("not a JSON object")
	}
	var out []member
	at := map[string]int{}
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := t.(string)
		if !ok {
			return nil, errors.New("an object key is not a string")
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		if i, dup := at[key]; dup {
			out[i].raw = v
			continue
		}
		at[key] = len(out)
		out = append(out, member{key: key, raw: v})
	}
	if t, err := dec.Token(); err != nil || t != json.Delim('}') {
		return nil, errors.New("the object does not end")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("data after the object")
	}
	return out, nil
}

// writeMembers writes members as a JSON object.
func writeMembers(ms []member) []byte {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, m := range ms {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(m.key)
		b.Write(k)
		b.WriteByte(':')
		b.Write(m.raw)
	}
	b.WriteByte('}')
	return b.Bytes()
}

func (o *object) get(key string) (json.RawMessage, bool) {
	for _, m := range o.members {
		if m.key == key {
			return m.raw, true
		}
	}
	return nil, false
}

// set replaces the value of a member, or adds the member at the end.
func (o *object) set(key string, raw []byte) error {
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		return errs.E(errs.Internal, errs.CodeInternal, "A value of the package document is not JSON.", errs.WithCause(err))
	}
	for i := range o.members {
		if o.members[i].key == key {
			o.members[i].raw = compact.Bytes()
			return nil
		}
	}
	o.members = append(o.members, member{key: key, raw: compact.Bytes()})
	return nil
}

func (o *object) setValue(key string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return errs.E(errs.Internal, errs.CodeInternal, "", errs.WithCause(err))
	}
	return o.set(key, raw)
}

func (o *object) remove(key string) {
	o.members = keepIf(o.members, func(m member) bool { return m.key != key })
}

func (o *object) bytes() []byte { return writeMembers(o.members) }
