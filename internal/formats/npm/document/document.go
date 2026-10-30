// Package document renders the package documents of a local npm repository from the
// rows of the data port (R-NPM-010, R-NPM-011, R-NPM-012, R-NPM-014): the full
// document, the abbreviated install document and the single-version document, with
// the validators and the content negotiation that go with them (R-NPM-013).
//
// Package documents are not stored; every rendering is a pure function of the rows,
// the links and the settings in [Options], so two replicas that read the same rows
// render the same bytes. The package is a leaf: it imports the standard library,
// the version order of its sibling semver and the values of rows.
package document

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/guided-traffic/trove/internal/formats/npm/rows"
	"github.com/guided-traffic/trove/internal/formats/npm/semver"
)

// RenderVersion is npmRenderVersion (R-NPM-013): a constant of the format that a
// release increments whenever it changes the bytes rendered from the same inputs, so
// replicas of two releases never share a validator during a rolling upgrade.
const RenderVersion = 1

// Variant is the form of a rendering.
type Variant string

// The variants of R-NPM-003 and R-NPM-011.
const (
	Full        Variant = "full"
	Abbreviated Variant = "abbreviated"
)

// ContentType is the media type a variant is served as; pnpm decides from it which
// form it received.
func (v Variant) ContentType() string {
	if v == Abbreviated {
		return "application/vnd.npm.install-v1+json"
	}
	return "application/json"
}

// Options are the inputs of a rendering that do not come from the rows.
type Options struct {
	// Name is the normalized package name and Basename the name without its scope.
	Name, Basename string
	// TarballPrefix is the URL every tarball of the package lies under, ending in
	// "/-/": the registry URL of the serving repository, the name with an unescaped
	// scope separator, "/-/" (R-NPM-010). It is built from the response base URL, so
	// the rendered bytes hold it and the cache key holds the base URL (Q-NPM-26).
	TarballPrefix string
	// Rev is the _rev of the full document (R-NPM-009).
	Rev string
	// AbbreviatedTime adds the time map to abbreviated documents (npm.abbreviated_time).
	AbbreviatedTime bool
}

// The top-level fields of the full document that come from the version latest points
// to (R-NPM-010).
func topLevelFromLatest() []string {
	return []string{"description", "keywords", "license", "homepage", "repository", "bugs", "author"}
}

// The fields of a version in the abbreviated document (R-NPM-011); dist is added
// separately and complete.
func abbreviatedFields() []string {
	return []string{
		"name", "version", "deprecated", "dependencies", "optionalDependencies", "devDependencies", "bundleDependencies",
		"peerDependencies", "peerDependenciesMeta", "acceptDependencies", "bin", "directories", "engines", "cpu", "os", "libc",
		"funding", "_hasShrinkwrap", "hasInstallScript",
	}
}

// rendered is one version prepared for the three documents.
type rendered struct {
	version string
	parsed  semver.Version
	ok      bool // version is a SemVer version
	// readme and readmeFile are those of the stored manifest, taken out of its fields.
	readme, readmeFile string
	fields             map[string]json.RawMessage // the stored manifest, dist.tarball and deprecated applied
	at                 time.Time
}

// Timestamp is the ISO 8601 form of the time map: UTC, with milliseconds.
func Timestamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

// FullDocument renders the full document (R-NPM-010).
func FullDocument(p *rows.LocalPackage, o Options) ([]byte, error) {
	vs, err := prepare(p, o)
	if err != nil {
		return nil, err
	}
	var w object
	w.str("_id", o.Name)
	w.str("_rev", o.Rev)
	w.str("name", o.Name)
	if err := w.value("dist-tags", p.Tags); err != nil {
		return nil, err
	}
	if err := w.raw("versions", versionsObject(vs, nil)); err != nil {
		return nil, err
	}
	if err := w.value("time", timeMap(p, vs)); err != nil {
		return nil, err
	}
	if err := w.value("maintainers", maintainers(vs)); err != nil {
		return nil, err
	}
	latest := find(vs, p.Tags["latest"])
	if latest != nil {
		for _, k := range topLevelFromLatest() {
			if raw, ok := latest.fields[k]; ok {
				w.rawMessage(k, raw)
			}
		}
	}
	readme, file := p.Readme, p.ReadmeFilename
	if latest != nil {
		if latest.readme != "" {
			readme = latest.readme
		}
		if latest.readmeFile != "" {
			file = latest.readmeFile
		}
	}
	if readme != "" {
		w.str("readme", readme)
	}
	if file != "" {
		w.str("readmeFilename", file)
	}
	return w.bytes(), nil
}

// AbbreviatedDocument renders the install document (R-NPM-011): name, modified, the
// tags, the versions with the fields an installer reads and, with
// npm.abbreviated_time, the time map.
func AbbreviatedDocument(p *rows.LocalPackage, o Options) ([]byte, error) {
	vs, err := prepare(p, o)
	if err != nil {
		return nil, err
	}
	var w object
	w.str("name", o.Name)
	w.str("modified", Timestamp(p.ModifiedAt))
	if err := w.value("dist-tags", p.Tags); err != nil {
		return nil, err
	}
	if err := w.raw("versions", versionsObject(vs, abbreviatedFields())); err != nil {
		return nil, err
	}
	if o.AbbreviatedTime {
		if err := w.value("time", timeMap(p, vs)); err != nil {
			return nil, err
		}
	}
	return w.bytes(), nil
}

// VersionDocument renders the one version p holds, exactly as the full document of
// the same revision lists it (R-NPM-014).
func VersionDocument(p *rows.LocalPackage, o Options) ([]byte, error) {
	vs, err := prepare(p, o)
	if err != nil {
		return nil, err
	}
	if len(vs) != 1 {
		return nil, fmt.Errorf("document: a version document renders one version, not %d", len(vs))
	}
	return json.Marshal(vs[0].fields)
}

// prepare parses the stored manifests, applies what the rows add (the tarball URL,
// the deprecation message) and orders the versions by SemVer precedence, so the
// bytes do not depend on the order the database returned the rows in.
func prepare(p *rows.LocalPackage, o Options) ([]rendered, error) {
	out := make([]rendered, 0, len(p.Versions))
	for i := range p.Versions {
		v := &p.Versions[i]
		var m map[string]json.RawMessage
		if err := json.Unmarshal(v.Manifest, &m); err != nil {
			return nil, fmt.Errorf("document: the manifest of %s@%s: %w", o.Name, v.Version, err)
		}
		if m == nil {
			m = map[string]json.RawMessage{}
		}
		var dist map[string]json.RawMessage
		if raw, ok := m["dist"]; ok {
			if err := json.Unmarshal(raw, &dist); err != nil {
				return nil, fmt.Errorf("document: the dist of %s@%s: %w", o.Name, v.Version, err)
			}
		}
		if dist == nil {
			dist = map[string]json.RawMessage{}
		}
		url, _ := json.Marshal(o.TarballPrefix + o.Basename + "-" + v.Version + ".tgz")
		dist["tarball"] = url
		d, err := json.Marshal(dist)
		if err != nil {
			return nil, err
		}
		m["dist"] = d
		if v.Deprecated != "" {
			s, _ := json.Marshal(v.Deprecated)
			m["deprecated"] = s
		} else {
			delete(m, "deprecated")
		}
		// The README of a package is that of the version latest points to; a copy in
		// every version of a document of 1,000 versions would add 64 MiB.
		r := rendered{version: v.Version, fields: m, at: v.PublishedAt}
		r.readme, _ = stringField(m, "readme")
		r.readmeFile, _ = stringField(m, "readmeFilename")
		delete(m, "readme")
		if pv, err := semver.Parse(v.Version); err == nil {
			r.parsed, r.ok = pv, true
		}
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool { return less(&out[i], &out[j]) })
	return out, nil
}

// less orders versions by SemVer precedence; a text that is no version sorts after
// the versions, by its characters.
func less(a, b *rendered) bool {
	switch {
	case !a.ok && !b.ok:
		return a.version < b.version
	case !a.ok:
		return false
	case !b.ok:
		return true
	}
	if c := semver.CompareBuild(a.parsed, b.parsed); c != 0 {
		return c < 0
	}
	return a.version < b.version
}

func find(vs []rendered, version string) *rendered {
	for i := range vs {
		if vs[i].version == version {
			return &vs[i]
		}
	}
	return nil
}

// versionsObject writes the versions object; fields restricts the members of each
// version (the abbreviated document), nil keeps them all. dist is always complete.
func versionsObject(vs []rendered, fields []string) []byte {
	var b bytes.Buffer
	b.WriteByte('{')
	for i := range vs {
		if i > 0 {
			b.WriteByte(',')
		}
		key, _ := json.Marshal(vs[i].version)
		b.Write(key)
		b.WriteByte(':')
		m := vs[i].fields
		if fields != nil {
			picked := make(map[string]json.RawMessage, len(fields)+1)
			for _, k := range fields {
				if raw, ok := m[k]; ok {
					picked[k] = raw
				}
			}
			picked["dist"] = m["dist"]
			m = picked
		}
		raw, _ := json.Marshal(m) // a map of valid raw messages cannot fail
		b.Write(raw)
	}
	b.WriteByte('}')
	return b.Bytes()
}

// timeMap is the time of R-NPM-012: created, modified and the commit time of every
// version, all from PostgreSQL's clock.
func timeMap(p *rows.LocalPackage, vs []rendered) *orderedMap {
	m := &orderedMap{}
	m.add("created", Timestamp(p.CreatedAt))
	m.add("modified", Timestamp(p.ModifiedAt))
	for i := range vs {
		m.add(vs[i].version, Timestamp(vs[i].at))
	}
	return m
}

// orderedMap is a JSON object of strings that keeps its insertion order.
type orderedMap struct {
	keys, values []string
}

func (m *orderedMap) add(k, v string) { m.keys, m.values = append(m.keys, k), append(m.values, v) }

func (m *orderedMap) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i := range m.keys {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(m.keys[i])
		v, _ := json.Marshal(m.values[i])
		b.Write(k)
		b.WriteByte(':')
		b.Write(v)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// maintainers lists the distinct principals that published the versions present,
// without e-mail, by name (R-NPM-010); it is informational.
func maintainers(vs []rendered) []map[string]string {
	seen := map[string]bool{}
	var names []string
	for i := range vs {
		var u struct {
			Name string `json:"name"`
		}
		if raw, ok := vs[i].fields["_npmUser"]; ok && json.Unmarshal(raw, &u) == nil && u.Name != "" && !seen[u.Name] {
			seen[u.Name] = true
			names = append(names, u.Name)
		}
	}
	sort.Strings(names)
	out := make([]map[string]string, len(names))
	for i, n := range names {
		out[i] = map[string]string{"name": n}
	}
	return out
}

func stringField(m map[string]json.RawMessage, k string) (string, bool) {
	raw, ok := m[k]
	if !ok {
		return "", false
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return "", false
	}
	return s, true
}

// object builds a JSON object member by member, in the order they are added.
type object struct {
	b bytes.Buffer
	n int
}

func (o *object) key(k string) {
	if o.n == 0 {
		o.b.WriteByte('{')
	} else {
		o.b.WriteByte(',')
	}
	o.n++
	kb, _ := json.Marshal(k)
	o.b.Write(kb)
	o.b.WriteByte(':')
}

func (o *object) str(k, v string) {
	o.key(k)
	vb, _ := json.Marshal(v)
	o.b.Write(vb)
}

func (o *object) value(k string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	o.key(k)
	o.b.Write(raw)
	return nil
}

func (o *object) raw(k string, raw []byte) error {
	o.key(k)
	o.b.Write(raw)
	return nil
}

func (o *object) rawMessage(k string, raw json.RawMessage) {
	o.key(k)
	if err := json.Compact(&o.b, raw); err != nil { // cannot happen: the column is jsonb
		o.b.WriteString("null")
	}
}

func (o *object) bytes() []byte {
	if o.n == 0 {
		return []byte("{}")
	}
	o.b.WriteByte('}')
	return o.b.Bytes()
}
