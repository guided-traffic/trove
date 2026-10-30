package remote

import (
	"encoding/json"
	"net/url"
	"sort"
	"strings"

	"github.com/guided-traffic/trove/internal/formats/npm/name"
)

// TarballPrefix is the URL every tarball of a package lies under on the serving
// repository: its registry URL (ending in a slash), the name with an unescaped scope
// separator, "/-/" (R-NPM-025). It is built from the response base URL of the request,
// never from the upstream's address or a Host header (R-REPO-043).
func TarballPrefix(registry, pkg string) string {
	segs := strings.Split(pkg, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.TrimSuffix(registry, "/") + "/" + strings.Join(segs, "/") + "/-/"
}

// basename is the package name without its scope: the stem of a tarball file.
func basename(pkg string) string {
	if n, err := name.Parse(pkg); err == nil {
		return n.Package()
	}
	if _, rest, ok := strings.Cut(pkg, "/"); ok {
		return rest
	}
	return pkg
}

// abbreviatedFields are the members of a version entry the install document keeps
// (R-NPM-011); dist is kept whole. It is the list of the local rendering, and a test
// of the format pins the two to give the same document.
func abbreviatedFields() []string {
	return []string{
		"name", "version", "deprecated", "dependencies", "optionalDependencies", "devDependencies", "bundleDependencies",
		"peerDependencies", "peerDependenciesMeta", "acceptDependencies", "bin", "directories", "engines", "cpu", "os", "libc",
		"funding", "_hasShrinkwrap", "hasInstallScript",
	}
}

// Entry returns the entry of a version with its dist.tarball set to the conventional
// URL under prefix, and with the members of fields only (dist whole) when fields is
// not nil. Everything else passes through unchanged (R-NPM-027).
func Entry(raw json.RawMessage, prefix, pkg, version string, fields []string) (json.RawMessage, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil || m == nil {
		return nil, invalid("the entry of version %s is not an object", quote(version))
	}
	dist := map[string]json.RawMessage{}
	if d, ok := m["dist"]; ok {
		if err := json.Unmarshal(d, &dist); err != nil || dist == nil {
			return nil, invalid("the dist of version %s is not an object", quote(version))
		}
	}
	tarball, _ := json.Marshal(prefix + basename(pkg) + "-" + version + ".tgz")
	dist["tarball"] = tarball
	m["dist"] = objectOf(dist)
	if fields != nil {
		picked := make(map[string]json.RawMessage, len(fields)+1)
		for _, k := range fields {
			if v, ok := m[k]; ok {
				picked[k] = v
			}
		}
		picked["dist"] = m["dist"]
		m = picked
	}
	return objectOf(m), nil
}

// versionsObject writes the entries in SemVer order.
func (p *Parsed) versionsObject(prefix, pkg string, fields []string) (json.RawMessage, error) {
	var o builder
	for _, v := range p.Ordered() {
		e, err := Entry(p.Versions[v], prefix, pkg, v, fields)
		if err != nil {
			return nil, err
		}
		o.raw(v, e)
	}
	return o.bytes(), nil
}

// leading are the members a full document opens with; the others follow sorted.
func leading() []string {
	return []string{"_id", "_rev", "name", "dist-tags", "versions", "time"}
}

// Full renders the full document with every dist.tarball under prefix (R-NPM-025).
// Every other member passes through unchanged. rev, when not empty, replaces _rev.
// The result depends on the content of the document alone: two fills that stored one
// document render the same bytes, and rendering the output again changes nothing.
func (p *Parsed) Full(pkg, prefix, rev string) ([]byte, error) {
	versions, err := p.versionsObject(prefix, pkg, nil)
	if err != nil {
		return nil, err
	}
	var o builder
	written := map[string]bool{}
	for _, k := range leading() {
		switch {
		case k == "versions":
			o.raw(k, versions)
		case k == "_rev" && rev != "":
			o.str(k, rev)
		default:
			v, ok := p.Top[k]
			if !ok {
				continue
			}
			o.raw(k, v)
		}
		written[k] = true
	}
	rest := make([]string, 0, len(p.Top))
	for k := range p.Top {
		if !written[k] {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	for _, k := range rest {
		o.raw(k, p.Top[k])
	}
	return o.bytes(), nil
}

// Abbreviated renders the install document from the full one (R-NPM-011): name,
// modified, the dist-tags, the versions with the members an installer reads and,
// with withTime, the time map. A document that carries no time.modified has no
// modified.
func (p *Parsed) Abbreviated(pkg, prefix string, withTime bool) ([]byte, error) {
	versions, err := p.versionsObject(prefix, pkg, abbreviatedFields())
	if err != nil {
		return nil, err
	}
	var o builder
	o.str("name", pkg)
	if m := p.Times()["modified"]; m != "" {
		o.str("modified", m)
	}
	if tags, ok := p.Top["dist-tags"]; ok {
		o.raw("dist-tags", tags)
	} else {
		o.raw("dist-tags", json.RawMessage("{}"))
	}
	o.raw("versions", versions)
	if t, ok := p.Top["time"]; ok && withTime {
		o.raw("time", t)
	}
	return o.bytes(), nil
}

// Version renders the single-version document of a version number, or of the version a
// dist-tag points to (R-NPM-014): exactly the entry the full document lists. ok is
// false when the document has no such version.
func (p *Parsed) Version(pkg, prefix, ref string) (doc []byte, ok bool, err error) {
	v := ref
	if _, direct := p.Versions[ref]; !direct {
		v = p.Tags()[ref]
	}
	raw, found := p.Versions[v]
	if !found {
		return nil, false, nil
	}
	e, err := Entry(raw, prefix, pkg, v, nil)
	return e, err == nil, err
}

// Rewrite is Full for the bytes of a stored document: the tarball URLs of the
// document under prefix.
func Rewrite(doc []byte, pkg, prefix string) ([]byte, error) {
	p, err := Parse(doc)
	if err != nil {
		return nil, err
	}
	return p.Full(pkg, prefix, "")
}

// TagsOf reads the dist-tags of a stored document without decoding its versions.
func TagsOf(doc []byte) (map[string]string, error) {
	var d struct {
		DistTags map[string]string `json:"dist-tags"`
	}
	if err := json.Unmarshal(doc, &d); err != nil {
		return nil, invalid("it is not a package document")
	}
	if d.DistTags == nil {
		d.DistTags = map[string]string{}
	}
	return d.DistTags, nil
}
