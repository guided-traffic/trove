package remote

import (
	"bytes"
	"encoding/json"
	"io"
	"sort"
	"time"

	"github.com/guided-traffic/trove/internal/formats/npm/semver"
)

// Parsed is a full package document decoded to its top level, with every version
// entry kept as the raw JSON it came as (D-8): only what a rendering changes is ever
// decoded further. Top holds every member but versions.
type Parsed struct {
	Top      map[string]json.RawMessage
	Versions map[string]json.RawMessage
}

// Parse decodes the top level of a package document. It refuses a document that is
// not one JSON object. A document without versions has none.
func Parse(doc []byte) (*Parsed, error) {
	dec := json.NewDecoder(bytes.NewReader(doc))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, invalid("it is not a JSON object")
	}
	p := &Parsed{Top: map[string]json.RawMessage{}, Versions: map[string]json.RawMessage{}}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, invalid("it is not valid JSON")
		}
		key, _ := keyTok.(string)
		if key != "versions" {
			var raw json.RawMessage
			if err := dec.Decode(&raw); err != nil {
				return nil, invalid("it is not valid JSON")
			}
			p.Top[key] = raw
			continue
		}
		tok, err := dec.Token()
		if err != nil {
			return nil, invalid("it is not valid JSON")
		}
		if tok == nil {
			continue
		}
		if tok != json.Delim('{') {
			return nil, invalid("versions is not an object")
		}
		for dec.More() {
			vTok, err := dec.Token()
			if err != nil {
				return nil, invalid("it is not valid JSON")
			}
			v, _ := vTok.(string)
			var raw json.RawMessage
			if err := dec.Decode(&raw); err != nil {
				return nil, invalid("it is not valid JSON")
			}
			p.Versions[v] = raw
		}
		if _, err := dec.Token(); err != nil {
			return nil, invalid("it is not valid JSON")
		}
	}
	if _, err := dec.Token(); err != nil {
		return nil, invalid("it is not valid JSON")
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, invalid("it holds more than one JSON value")
	}
	return p, nil
}

// Tags returns the dist-tags of the document; a member that is no map of strings is
// ignored.
func (p *Parsed) Tags() map[string]string {
	out := map[string]string{}
	var m map[string]json.RawMessage
	if json.Unmarshal(p.Top["dist-tags"], &m) != nil {
		return out
	}
	for k, v := range m {
		if s, ok := asString(v); ok {
			out[k] = s
		}
	}
	return out
}

// Times returns the strings of the time map.
func (p *Parsed) Times() map[string]string {
	out := map[string]string{}
	var m map[string]json.RawMessage
	if json.Unmarshal(p.Top["time"], &m) != nil {
		return out
	}
	for k, v := range m {
		if s, ok := asString(v); ok {
			out[k] = s
		}
	}
	return out
}

// Modified is time.modified, the zero time when the document has none.
func (p *Parsed) Modified() time.Time { return ParseTime(p.Times()["modified"]) }

// Ordered returns the version numbers by SemVer precedence, a text that is no
// version after the versions, by its characters: the order of the local documents.
func (p *Parsed) Ordered() []string { return OrderVersions(keys(p.Versions)) }

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// OrderVersions sorts version texts by SemVer precedence (build metadata as a last
// tie-break), a text that is no version after the versions.
func OrderVersions(vs []string) []string {
	type item struct {
		text string
		v    semver.Version
		ok   bool
	}
	items := make([]item, len(vs))
	for i, s := range vs {
		items[i].text = s
		if v, err := semver.Parse(s); err == nil {
			items[i].v, items[i].ok = v, true
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		switch {
		case !a.ok && !b.ok:
			return a.text < b.text
		case !a.ok:
			return false
		case !b.ok:
			return true
		}
		if c := semver.CompareBuild(a.v, b.v); c != 0 {
			return c < 0
		}
		return a.text < b.text
	})
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.text
	}
	return out
}

// builder writes a JSON object member by member, in the order they are added.
type builder struct {
	b bytes.Buffer
	n int
}

func (o *builder) key(k string) {
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

// raw adds a member whose value is JSON; the value is compacted without HTML escaping,
// so a pass-through value keeps its text.
func (o *builder) raw(k string, v json.RawMessage) {
	o.key(k)
	if err := json.Compact(&o.b, v); err != nil {
		o.b.WriteString("null")
	}
}

func (o *builder) str(k, v string) {
	o.key(k)
	vb, _ := json.Marshal(v)
	o.b.Write(vb)
}

func (o *builder) bytes() []byte {
	if o.n == 0 {
		return []byte("{}")
	}
	o.b.WriteByte('}')
	return o.b.Bytes()
}

// objectOf writes a map as an object with its keys sorted.
func objectOf(m map[string]json.RawMessage) []byte {
	ks := keys(m)
	sort.Strings(ks)
	var o builder
	for _, k := range ks {
		o.raw(k, m[k])
	}
	return o.bytes()
}
