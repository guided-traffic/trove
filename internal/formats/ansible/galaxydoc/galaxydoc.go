// Package galaxydoc renders and parses the documents of the Galaxy API v3 that `ansible-galaxy` reads (R-ANS-003, R-ANS-004,
// R-ANS-009): the discovery document, a collection, the list of its versions, one version and an import task. A document is
// rendered from a neutral form ([Version], [VersionEntry], [CollectionSummary], [Task]) that comes from a database row (the local
// class) or from a document an upstream sent ([ParseUpstreamVersion], [ParseUpstreamList]), so the three classes serve the same bytes
// for the same version, and every address in a document is built from the repository's own address ([Addr]) and never from the
// upstream's.
//
// The renderers write the members in a fixed order with the escapes of a plain JSON writer, so the output is deterministic and a
// golden file can hold it. A version document leaves out the plugin listing of the public Galaxy (metadata.contents, docs_blob,
// manifest and files, Q-ANS-10); nothing in the client matrix reads them.
//
// It is a leaf: it imports the standard library and the rows leaf of the format.
package galaxydoc

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/formats/ansible/rows"
)

// MaxDocumentBytes bounds an upstream document the parsers accept (R-ANS-031, the stored-document bound).
const MaxDocumentBytes = 16 << 20

// The paging of a version list (R-ANS-004): limit defaults to 100 and is clamped to 1..100.
const (
	DefaultLimit = 100
	MaxLimit     = 100
)

// ErrInvalid is wrapped by every error of a document that is not what the parsers accept.
var ErrInvalid = errors.New("invalid Galaxy document")

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// PageLimit clamps the limit of a list request: 0 and negative values are the default, larger ones the maximum.
func PageLimit(limit int) int {
	switch {
	case limit <= 0:
		return DefaultLimit
	case limit > MaxLimit:
		return MaxLimit
	}
	return limit
}

// Addr is the address a document is rendered for: the repository URL as the client reached it, "<base>/api/ansible/<project>/<repo>/"
// with its trailing slash (built from formats.LinkBuilder). Namespace and Name name the collection a collection-scoped document
// ([RenderVersionList]) is about; the other renderers take them from their input.
type Addr struct {
	Repo, Namespace, Name string
}

// Collection is the address of the collection document.
func (a Addr) Collection(namespace, name string) string {
	return a.Repo + "v3/collections/" + namespace + "/" + name + "/"
}

// Versions is the address of the version list.
func (a Addr) Versions(namespace, name string) string {
	return a.Collection(namespace, name) + "versions/"
}

// Version is the address of a version document.
func (a Addr) Version(namespace, name, version string) string {
	return a.Versions(namespace, name) + version + "/"
}

// Artifact is the address of the archive of a file name.
func (a Addr) Artifact(file string) string { return a.Repo + "v3/artifacts/collections/" + file }

// Task is the address of an import task.
func (a Addr) Task(id string) string { return a.Repo + "v3/imports/collections/" + id + "/" }

// VersionEntry is one entry of a version list.
type VersionEntry struct {
	Version, RequiresAnsible string
	CreatedAt, UpdatedAt     time.Time
}

// Version is the neutral form of one version document.
type Version struct {
	Namespace, Name, Version, RequiresAnsible string
	SHA256                                    [32]byte
	Size                                      int64
	Filename                                  string
	// Dependencies are metadata.dependencies: "<ns>.<name>" to a requirement.
	Dependencies map[string]string
	// Info is the metadata object without the plugin listing and without the dependencies, as JSON. The renderer writes the known keys
	// in a fixed order with their defaults, then every other member in name order.
	Info json.RawMessage
	// Signatures are the detached signatures of the version, opaque and passed through.
	Signatures           []json.RawMessage
	CreatedAt, UpdatedAt time.Time
	// CollectionID is the UUID of the collection (collection.id).
	CollectionID string
}

// FileName is the archive file name of the version: Filename, or "<ns>-<name>-<version>.tar.gz".
func (v Version) FileName() string {
	if v.Filename != "" {
		return v.Filename
	}
	return v.Namespace + "-" + v.Name + "-" + v.Version + ".tar.gz"
}

// The types of the data port that a document is rendered from (R-ANS-009, R-ANS-004); internal/formats/ansible/rows owns them, so that
// the port and the renderer speak the same values.
type (
	// CollectionSummary is what the collection document shows.
	CollectionSummary = rows.CollectionSummary
	// TaskMessage is one message of an import task.
	TaskMessage = rows.TaskMessage
	// Task is the neutral form of an import task (R-ANS-009).
	Task = rows.Task
)

// The states of a task.
const (
	TaskCompleted = rows.TaskCompleted
	TaskFailed    = rows.TaskFailed
)

// timeFormat is the time format of the Galaxy: UTC with microseconds.
const timeFormat = "2006-01-02T15:04:05.000000Z"

func ts(t time.Time) string { return t.UTC().Format(timeFormat) }

// obj writes a JSON object member by member, in the order of the calls.
type obj struct {
	b bytes.Buffer
	n int
}

func newObj() *obj {
	o := &obj{}
	o.b.WriteByte('{')
	return o
}

func (o *obj) key(k string) {
	if o.n > 0 {
		o.b.WriteByte(',')
	}
	o.n++
	o.b.Write(enc(k))
	o.b.WriteByte(':')
}

// put writes a member encoded from a Go value.
func (o *obj) put(k string, v any) *obj {
	o.key(k)
	o.b.Write(enc(v))
	return o
}

// raw writes a member whose value is already JSON.
func (o *obj) raw(k string, v []byte) *obj {
	o.key(k)
	o.b.Write(v)
	return o
}

func (o *obj) bytes() []byte {
	o.b.WriteByte('}')
	return o.b.Bytes()
}

// enc encodes a value as compact JSON without the HTML escapes of encoding/json, as a plain JSON writer does.
func enc(v any) []byte {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	if err := e.Encode(v); err != nil {
		panic(fmt.Sprintf("galaxydoc: encoding %T: %v", v, err))
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n"))
}

// RenderRoot renders the discovery document of the repository URL (R-ANS-003). v1 is listed by remote and virtual repositories only.
func RenderRoot(v1 bool) []byte {
	avail := newObj().put("v3", "v3/")
	if v1 {
		avail.put("v1", "v1/")
	}
	return newObj().raw("available_versions", avail.bytes()).put("current_version", "v3").put("description", "trove Ansible Galaxy API").bytes()
}

// RenderCollection renders the collection document (R-ANS-004).
func RenderCollection(c CollectionSummary, a Addr) []byte {
	o := newObj().put("href", a.Collection(c.Namespace, c.Name)).put("namespace", c.Namespace).put("name", c.Name).put("deprecated", false).
		put("versions_url", a.Versions(c.Namespace, c.Name))
	if c.HighestVersion == "" {
		o.raw("highest_version", []byte("null"))
	} else {
		o.raw("highest_version", newObj().put("href", a.Version(c.Namespace, c.Name, c.HighestVersion)).put("version", c.HighestVersion).bytes())
	}
	return o.put("created_at", ts(c.CreatedAt)).put("updated_at", ts(c.UpdatedAt)).bytes()
}

// RenderVersionList renders one page of the version list of the collection a.Namespace.a.Name (R-ANS-004): page holds the entries
// of [offset, offset+len(page)) of the newest-first list of count entries. The links carry the same limit; previous and next are null
// at the ends.
func RenderVersionList(page []VersionEntry, count, limit, offset int, a Addr) []byte {
	limit = PageLimit(limit)
	offset = max(offset, 0)
	base := a.Versions(a.Namespace, a.Name)
	link := func(off int) []byte {
		return enc(base + "?limit=" + strconv.Itoa(limit) + "&offset=" + strconv.Itoa(off))
	}
	null := []byte("null")
	prev, next := null, null
	if offset > 0 {
		prev = link(max(offset-limit, 0))
	}
	if offset+limit < count {
		next = link(offset + limit)
	}
	last := 0
	if count > 0 {
		last = (count - 1) / limit * limit
	}
	links := newObj().raw("first", link(0)).raw("previous", prev).raw("next", next).raw("last", link(last))
	var data bytes.Buffer
	data.WriteByte('[')
	for i, e := range page {
		if i > 0 {
			data.WriteByte(',')
		}
		data.Write(newObj().put("version", e.Version).put("href", a.Version(a.Namespace, a.Name, e.Version)).put("created_at", ts(e.CreatedAt)).
			put("updated_at", ts(e.UpdatedAt)).put("requires_ansible", nullable(e.RequiresAnsible)).put("marks", []string{}).bytes())
	}
	data.WriteByte(']')
	return newObj().raw("meta", newObj().put("count", count).bytes()).raw("links", links.bytes()).raw("data", data.Bytes()).bytes()
}

// nullable is the value of requires_ansible: the public Galaxy writes null for a version without one, a string otherwise.
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// The members of the metadata object in the order the public Galaxy writes them, with the value written when the version has none.
func metadataKeys() []struct{ key, zero string } {
	return []struct{ key, zero string }{
		{"authors", "[]"},
		{"dependencies", "{}"},
		{"description", `""`},
		{"documentation", `""`},
		{"homepage", `""`},
		{"issues", `""`},
		{"license", "[]"},
		{"license_file", "null"},
		{"readme", `""`},
		{"repository", `""`},
		{"tags", "[]"},
	}
}

// dropped reports whether a member of the upstream's metadata is one trove does not serve (Q-ANS-10).
func dropped(key string) bool {
	switch key {
	case "contents", "docs_blob", "manifest", "files":
		return true
	}
	return false
}

// RenderVersion renders the document of one version (R-ANS-004). download_url and every href are built from a.
func RenderVersion(v Version, a Addr) []byte {
	deps := v.Dependencies
	if deps == nil {
		deps = map[string]string{}
	}
	info := map[string]json.RawMessage{}
	if len(v.Info) > 0 {
		_ = json.Unmarshal(v.Info, &info)
	}
	meta := newObj()
	known := map[string]bool{}
	for _, k := range metadataKeys() {
		known[k.key] = true
		switch raw, ok := info[k.key]; {
		case k.key == "dependencies":
			meta.put("dependencies", deps)
		case ok && string(raw) != "":
			meta.raw(k.key, raw)
		default:
			meta.raw(k.key, []byte(k.zero))
		}
	}
	var extra []string
	for k := range info {
		if !known[k] && !dropped(k) {
			extra = append(extra, k)
		}
	}
	sort.Strings(extra)
	for _, k := range extra {
		meta.raw(k, info[k])
	}
	sigs := []byte("[]")
	if len(v.Signatures) > 0 {
		var b bytes.Buffer
		b.WriteByte('[')
		for i, s := range v.Signatures {
			if i > 0 {
				b.WriteByte(',')
			}
			b.Write(s)
		}
		b.WriteByte(']')
		sigs = b.Bytes()
	}
	file := v.FileName()
	return newObj().put("version", v.Version).put("href", a.Version(v.Namespace, v.Name, v.Version)).put("created_at", ts(v.CreatedAt)).
		put("updated_at", ts(v.UpdatedAt)).put("requires_ansible", nullable(v.RequiresAnsible)).put("marks", []string{}).
		raw("artifact", newObj().put("filename", file).put("sha256", hex.EncodeToString(v.SHA256[:])).put("size", v.Size).bytes()).
		raw("collection", newObj().put("id", v.CollectionID).put("name", v.Name).put("href", a.Collection(v.Namespace, v.Name)).bytes()).
		put("download_url", a.Artifact(file)).put("name", v.Name).
		raw("namespace", newObj().put("name", v.Namespace).bytes()).raw("signatures", sigs).raw("metadata", meta.bytes()).bytes()
}

// RenderTask renders an import task (R-ANS-009). The document names the collection and the version and nothing else of the archive.
func RenderTask(t Task) []byte {
	o := newObj().put("id", t.ID).put("state", t.State).put("namespace", t.Namespace).put("name", t.Name).put("version", t.Version).
		put("created_at", ts(t.CreatedAt)).put("started_at", ts(t.StartedAt)).put("updated_at", ts(t.UpdatedAt)).put("finished_at", ts(t.FinishedAt))
	if t.ErrorCode == "" {
		o.raw("error", []byte("null"))
	} else {
		o.raw("error", newObj().put("code", t.ErrorCode).put("description", t.ErrorDescription).bytes())
	}
	var m bytes.Buffer
	m.WriteByte('[')
	for i, msg := range t.Messages {
		if i > 0 {
			m.WriteByte(',')
		}
		m.Write(newObj().put("level", msg.Level).put("message", msg.Message).put("time", ts(msg.Time)).bytes())
	}
	m.WriteByte(']')
	return o.raw("messages", m.Bytes()).bytes()
}

// upstreamVersion is the part of a version document of the public Galaxy that trove reads.
type upstreamVersion struct {
	Version         string          `json:"version"`
	Name            string          `json:"name"`
	Namespace       json.RawMessage `json:"namespace"`
	RequiresAnsible *string         `json:"requires_ansible"`
	CreatedAt       string          `json:"created_at"`
	UpdatedAt       string          `json:"updated_at"`
	DownloadURL     string          `json:"download_url"`
	Artifact        struct {
		Filename string `json:"filename"`
		SHA256   string `json:"sha256"`
		Size     int64  `json:"size"`
	} `json:"artifact"`
	Collection struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"collection"`
	Signatures []json.RawMessage          `json:"signatures"`
	Metadata   map[string]json.RawMessage `json:"metadata"`
}

// ParseUpstreamVersion reads a version document an upstream sent into the neutral form and returns the upstream's download_url, which
// the caller maps back to the archive path (R-ANS-017). The document is bounded by [MaxDocumentBytes]; the plugin listing of the
// metadata is dropped here, so a stored or served form never carries it. The identity of the document is checked against itself: a
// namespace, a name, a version and an archive digest are required, and a name and a collection name that disagree are refused. The
// caller compares the identity with the one the request named.
func ParseUpstreamVersion(doc []byte) (Version, string, error) {
	if len(doc) > MaxDocumentBytes {
		return Version{}, "", invalid("the version document is larger than %d bytes", MaxDocumentBytes)
	}
	var u upstreamVersion
	if err := json.Unmarshal(doc, &u); err != nil {
		return Version{}, "", invalid("the version document is not JSON of the expected shape")
	}
	var ns struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(u.Namespace, &ns); err != nil || ns.Name == "" {
		return Version{}, "", invalid("the version document has no namespace name")
	}
	name := u.Name
	if name == "" {
		name = u.Collection.Name
	}
	if name == "" || (u.Collection.Name != "" && u.Collection.Name != name) {
		return Version{}, "", invalid("the version document has no collection name, or two that differ")
	}
	if u.Version == "" {
		return Version{}, "", invalid("the version document has no version")
	}
	if u.DownloadURL == "" {
		return Version{}, "", invalid("the version document has no download_url")
	}
	sum, err := hex.DecodeString(u.Artifact.SHA256)
	if err != nil || len(sum) != 32 || strings.ToLower(u.Artifact.SHA256) != u.Artifact.SHA256 {
		return Version{}, "", invalid("artifact.sha256 is not 64 lower-case hexadecimal digits")
	}
	if u.Artifact.Size < 0 {
		return Version{}, "", invalid("artifact.size is negative")
	}
	v := Version{
		Namespace: ns.Name, Name: name, Version: u.Version, Filename: u.Artifact.Filename, Size: u.Artifact.Size,
		Signatures: u.Signatures, CollectionID: u.Collection.ID, CreatedAt: parseTime(u.CreatedAt), UpdatedAt: parseTime(u.UpdatedAt),
		Dependencies: map[string]string{},
	}
	copy(v.SHA256[:], sum)
	if u.RequiresAnsible != nil {
		v.RequiresAnsible = *u.RequiresAnsible
	}
	if raw, ok := u.Metadata["dependencies"]; ok && string(raw) != "null" {
		if err := json.Unmarshal(raw, &v.Dependencies); err != nil {
			return Version{}, "", invalid("metadata.dependencies is not an object of strings")
		}
	}
	keep := map[string]json.RawMessage{}
	for k, raw := range u.Metadata {
		if !dropped(k) && k != "dependencies" {
			keep[k] = raw
		}
	}
	v.Info = enc(keep)
	return v, u.DownloadURL, nil
}

// upstreamList is the part of a version list page of the public Galaxy that trove reads.
type upstreamList struct {
	Meta struct {
		Count int `json:"count"`
	} `json:"meta"`
	Links struct {
		Next *string `json:"next"`
	} `json:"links"`
	Data []struct {
		Version         string  `json:"version"`
		RequiresAnsible *string `json:"requires_ansible"`
		CreatedAt       string  `json:"created_at"`
		UpdatedAt       string  `json:"updated_at"`
	} `json:"data"`
}

// ParseUpstreamList reads one page of an upstream's version list: its entries in the upstream's order, the count of the whole list and
// the link of the next page ("" for the last). The link is the upstream's own and the caller never serves it.
func ParseUpstreamList(doc []byte) (entries []VersionEntry, count int, next string, err error) {
	if len(doc) > MaxDocumentBytes {
		return nil, 0, "", invalid("the version list is larger than %d bytes", MaxDocumentBytes)
	}
	var l upstreamList
	if err := json.Unmarshal(doc, &l); err != nil {
		return nil, 0, "", invalid("the version list is not JSON of the expected shape")
	}
	if l.Meta.Count < 0 || l.Meta.Count < len(l.Data) {
		return nil, 0, "", invalid("meta.count is %d for a page of %d entries", l.Meta.Count, len(l.Data))
	}
	for _, d := range l.Data {
		if d.Version == "" {
			return nil, 0, "", invalid("a version list entry has no version")
		}
		e := VersionEntry{Version: d.Version, CreatedAt: parseTime(d.CreatedAt), UpdatedAt: parseTime(d.UpdatedAt)}
		if d.RequiresAnsible != nil {
			e.RequiresAnsible = *d.RequiresAnsible
		}
		entries = append(entries, e)
	}
	if l.Links.Next != nil {
		next = *l.Links.Next
	}
	return entries, l.Meta.Count, next, nil
}

// parseTime reads a time of an upstream document; one that does not parse is the zero time, which the renderer writes as year 1.
func parseTime(s string) time.Time {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}
