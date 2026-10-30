package remote_test

import (
	"bytes"
	"context"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/npm/remote"
)

// sri is the integrity of a content text.
func sri(content string) string {
	sum := sha512.Sum512([]byte(content))
	return "sha512-" + base64.StdEncoding.EncodeToString(sum[:])
}

// doc builds an upstream document of a package with the given versions; tamper edits the
// decoded document before it is encoded.
func doc(t testing.TB, name string, versions []string, tamper func(m map[string]any)) []byte {
	t.Helper()
	vs := map[string]any{}
	times := map[string]string{"created": "2020-01-01T00:00:00.000Z", "modified": "2024-05-01T10:00:00.000Z"}
	for _, v := range versions {
		vs[v] = map[string]any{
			"name": name, "version": v, "description": "d " + v,
			"dist": map[string]any{
				"tarball": "https://registry.example.org/" + name + "/-/" + lastSegment(name) + "-" + v + ".tgz", "integrity": sri(name + v),
				"shasum": "0123456789abcdef0123456789abcdef01234567", "signatures": []any{map[string]string{"keyid": "k", "sig": "s"}},
			},
			"_npmUser": map[string]any{"name": "u", "trustedPublisher": map[string]string{"id": "x"}},
		}
		times[v] = "2023-02-03T04:05:06.000Z"
	}
	m := map[string]any{
		"_id": name, "_rev": "12-abc", "name": name, "dist-tags": map[string]string{"latest": versions[len(versions)-1]},
		"versions": vs, "time": times, "readme": "a <b> & c", "maintainers": []any{map[string]string{"name": "u"}},
	}
	if tamper != nil {
		tamper(m)
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func lastSegment(name string) string {
	if i := strings.IndexByte(name, '/'); i >= 0 {
		return name[i+1:]
	}
	return name
}

func TestR_NPM_024_CheckAcceptsADocumentAndReadsTheFacts(t *testing.T) {
	b := doc(t, "@acme/ui", []string{"1.0.0", "2.0.0-rc.1", "1.5.0"}, nil)
	d, err := remote.Check(bytes.NewReader(b), "@acme/ui")
	if err != nil {
		t.Fatal(err)
	}
	if d.Name != "@acme/ui" || !d.Modified.Equal(time.Date(2024, 5, 1, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("name %q, modified %v", d.Name, d.Modified)
	}
	var got []string
	for _, v := range d.Versions {
		got = append(got, v.Version)
		if v.Integrity != sri("@acme/ui"+v.Version) || v.Shasum != "0123456789abcdef0123456789abcdef01234567" || v.PublishedAt.IsZero() {
			t.Errorf("%s: facts %+v", v.Version, v)
		}
	}
	// The facts are in the order of the version texts, whatever order the upstream listed them in.
	if strings.Join(got, ",") != "1.0.0,1.5.0,2.0.0-rc.1" {
		t.Errorf("versions = %v", got)
	}
	if d.Tarballs["1.5.0"] != "https://registry.example.org/@acme/ui/-/ui-1.5.0.tgz" {
		t.Errorf("tarball = %q", d.Tarballs["1.5.0"])
	}
}

func TestR_NPM_024_CheckRefusesWhatThePageListsAsInvalid(t *testing.T) {
	good := []string{"1.0.0", "1.1.0"}
	edit := func(f func(v map[string]any, m map[string]any)) func(map[string]any) {
		return func(m map[string]any) {
			f(m["versions"].(map[string]any)["1.1.0"].(map[string]any), m)
		}
	}
	cases := []struct {
		name   string
		doc    []byte
		reason string
	}{
		{"a name other than the requested", doc(t, "left-pad", good, func(m map[string]any) { m["name"] = "impostor" }), "name"},
		{"a version entry that names another version", doc(t, "left-pad", good, edit(func(v, _ map[string]any) { v["version"] = "9.9.9" })), "another version"},
		{"a version entry that names another package", doc(t, "left-pad", good, edit(func(v, _ map[string]any) { v["name"] = "other" })), "another package"},
		{"a tarball that is not absolute", doc(t, "left-pad", good, edit(func(v, _ map[string]any) { v["dist"].(map[string]any)["tarball"] = "/left-pad/-/x.tgz" })), "tarball"},
		{"a tarball with another scheme", doc(t, "left-pad", good, edit(func(v, _ map[string]any) { v["dist"].(map[string]any)["tarball"] = "ftp://x/y.tgz" })), "tarball"},
		{"a version without a tarball", doc(t, "left-pad", good, edit(func(v, _ map[string]any) { delete(v["dist"].(map[string]any), "tarball") })), "tarball"},
		{"a version without a dist", doc(t, "left-pad", good, edit(func(v, _ map[string]any) { delete(v, "dist") })), "dist"},
		{"an integrity that is no SRI", doc(t, "left-pad", good, edit(func(v, _ map[string]any) { v["dist"].(map[string]any)["integrity"] = "md5-xyz" })), "integrity"},
		{"an integrity with the wrong digest length", doc(t, "left-pad", good, edit(func(v, _ map[string]any) { v["dist"].(map[string]any)["integrity"] = "sha512-AAAA" })), "integrity"},
		{"a document that is no object", []byte(`["x"]`), "object"},
		{"invalid JSON", []byte(`{"name":"left-pad",`), "JSON"},
		{"two documents", append(doc(t, "left-pad", good, nil), []byte(` {}`)...), "more than one"},
		{"no name", []byte(`{"versions":{}}`), "no name"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := remote.Check(bytes.NewReader(c.doc), "left-pad")
			e, ok := errs.As(err)
			if !ok || e.Code != errs.CodeNPMInvalidUpstreamDocument || e.Kind != errs.UpstreamFailed {
				t.Fatalf("err = %v, want 502 npm_invalid_upstream_document", err)
			}
			if !strings.Contains(e.Detail, c.reason) {
				t.Errorf("detail %q does not say %q", e.Detail, c.reason)
			}
		})
	}
}

func TestR_NPM_024_CheckToleratesWhatRegistriesDo(t *testing.T) {
	// Old versions have a shasum and no integrity; a document may have no time map; a
	// package may have no version yet.
	b := doc(t, "left-pad", []string{"1.0.0"}, func(m map[string]any) {
		delete(m["versions"].(map[string]any)["1.0.0"].(map[string]any)["dist"].(map[string]any), "integrity")
		delete(m, "time")
	})
	d, err := remote.Check(bytes.NewReader(b), "left-pad")
	if err != nil {
		t.Fatal(err)
	}
	if v := d.Versions[0]; v.Integrity != "" || v.Shasum == "" || !v.PublishedAt.IsZero() || !d.Modified.IsZero() {
		t.Errorf("facts = %+v, modified %v", v, d.Modified)
	}
	for _, empty := range []string{`{"name":"left-pad"}`, `{"name":"left-pad","versions":null}`, `{"name":"left-pad","versions":{}}`} {
		if d, err := remote.Check(strings.NewReader(empty), "left-pad"); err != nil || len(d.Versions) != 0 {
			t.Errorf("%s: %v %v", empty, d, err)
		}
	}
}

func TestR_NPM_025_RewriteSetsTheTarballOfEveryVersionAndKeepsTheRest(t *testing.T) {
	b := doc(t, "@acme/ui", []string{"1.0.0", "1.5.0"}, nil)
	prefix := remote.TarballPrefix("https://trove.example.com/api/npm/npm-remote/", "@acme/ui")
	if prefix != "https://trove.example.com/api/npm/npm-remote/@acme/ui/-/" {
		t.Fatalf("prefix = %q", prefix)
	}
	out, err := remote.Rewrite(b, "@acme/ui", prefix)
	if err != nil {
		t.Fatal(err)
	}
	var in, got struct {
		Versions map[string]struct {
			Dist     map[string]json.RawMessage `json:"dist"`
			NPMUser  json.RawMessage            `json:"_npmUser"`
			Desc     string                     `json:"description"`
			Name     string                     `json:"name"`
			Original json.RawMessage            `json:"-"`
		} `json:"versions"`
		Readme string `json:"readme"`
		Rev    string `json:"_rev"`
	}
	if err := json.Unmarshal(b, &in); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	for v, e := range got.Versions {
		var tarball string
		_ = json.Unmarshal(e.Dist["tarball"], &tarball)
		if tarball != prefix+"ui-"+v+".tgz" {
			t.Errorf("%s: tarball %q", v, tarball)
		}
		// R-NPM-027: signatures, the publisher fields and everything else pass through.
		if !bytes.Equal(e.Dist["signatures"], in.Versions[v].Dist["signatures"]) || !bytes.Equal(e.Dist["integrity"], in.Versions[v].Dist["integrity"]) ||
			!bytes.Equal(e.NPMUser, in.Versions[v].NPMUser) || e.Desc != in.Versions[v].Desc {
			t.Errorf("%s: a member changed: %s", v, out)
		}
	}
	if got.Readme != "a <b> & c" || got.Rev != "12-abc" {
		t.Errorf("readme %q, rev %q", got.Readme, got.Rev)
	}
	if strings.Contains(string(out), "registry.example.org") {
		t.Errorf("the output still names the upstream: %s", out)
	}
}

func TestR_NPM_025_RewriteIsDeterministicAndIdempotent(t *testing.T) {
	b := doc(t, "left-pad", []string{"1.3.0", "1.0.0", "1.2.0-beta.1", "1.2.0"}, nil)
	prefix := "https://t.example/api/npm/r/left-pad/-/"
	one, err := remote.Rewrite(b, "left-pad", prefix)
	if err != nil {
		t.Fatal(err)
	}
	two, _ := remote.Rewrite(b, "left-pad", prefix)
	three, err := remote.Rewrite(one, "left-pad", prefix)
	if err != nil || !bytes.Equal(one, two) || !bytes.Equal(one, three) {
		t.Fatalf("not stable: %v\n%s\n%s", err, one, three)
	}
	// The versions are in SemVer order, as the local documents list them.
	p, _ := remote.Parse(one)
	if got := strings.Join(p.Ordered(), ","); got != "1.0.0,1.2.0-beta.1,1.2.0,1.3.0" {
		t.Errorf("order = %s", got)
	}
}

func TestR_NPM_011_AbbreviatedAndVersionDocumentsAreDerivedFromTheFullOne(t *testing.T) {
	b := doc(t, "left-pad", []string{"1.0.0", "1.1.0"}, func(m map[string]any) {
		m["versions"].(map[string]any)["1.1.0"].(map[string]any)["scripts"] = map[string]string{"test": "x"}
		m["versions"].(map[string]any)["1.1.0"].(map[string]any)["dependencies"] = map[string]string{"a": "^1"}
		m["dist-tags"].(map[string]string)["next"] = "1.0.0"
	})
	p, err := remote.Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	prefix := "https://t.example/api/npm/r/left-pad/-/"
	ab, err := p.Abbreviated("left-pad", prefix, true)
	if err != nil {
		t.Fatal(err)
	}
	var a struct {
		Name     string                     `json:"name"`
		Modified string                     `json:"modified"`
		Tags     map[string]string          `json:"dist-tags"`
		Versions map[string]map[string]any  `json:"versions"`
		Time     map[string]json.RawMessage `json:"time"`
	}
	if err := json.Unmarshal(ab, &a); err != nil {
		t.Fatal(err)
	}
	e := a.Versions["1.1.0"]
	if a.Name != "left-pad" || a.Modified != "2024-05-01T10:00:00.000Z" || a.Tags["next"] != "1.0.0" || len(a.Time) == 0 {
		t.Errorf("abbreviated = %s", ab)
	}
	if _, ok := e["scripts"]; ok {
		t.Error("an install document carries scripts")
	}
	if _, ok := e["description"]; ok {
		t.Error("an install document carries the description")
	}
	if e["dependencies"] == nil || e["dist"].(map[string]any)["tarball"] != prefix+"left-pad-1.1.0.tgz" {
		t.Errorf("entry = %v", e)
	}
	if noTime, _ := p.Abbreviated("left-pad", prefix, false); strings.Contains(string(noTime), `"time"`) {
		t.Error("abbreviated_time off still adds the time map")
	}

	// The document of a version is the entry of the full document, by number or by tag.
	full, _ := p.Full("left-pad", prefix, "")
	var fd struct {
		Versions map[string]json.RawMessage `json:"versions"`
	}
	_ = json.Unmarshal(full, &fd)
	for _, ref := range []string{"1.1.0", "latest"} {
		got, ok, err := p.Version("left-pad", prefix, ref)
		if err != nil || !ok || !bytes.Equal(got, fd.Versions["1.1.0"]) {
			t.Errorf("version %s: %v %v\n%s", ref, ok, err, got)
		}
	}
	if _, ok, _ := p.Version("left-pad", prefix, "9.9.9"); ok {
		t.Error("an unknown version has a document")
	}
}

func TestR_NPM_024_UpstreamURLs(t *testing.T) {
	base, _ := url.Parse("https://registry.example.org/mirror/")
	cases := []struct {
		path     string
		document bool
		want     string
	}{
		{"lodash", true, "https://registry.example.org/mirror/lodash"},
		{"@acme/ui", true, "https://registry.example.org/mirror/@acme%2Fui"},
		{"@acme/ui/-/ui-1.0.0.tgz", false, "https://registry.example.org/mirror/@acme/ui/-/ui-1.0.0.tgz"},
		{"lodash/-/lodash-4.17.21.tgz", false, "https://registry.example.org/mirror/lodash/-/lodash-4.17.21.tgz"},
		{"lodash/-/lodash 1.tgz", false, "https://registry.example.org/mirror/lodash/-/lodash%201.tgz"},
	}
	for _, c := range cases {
		u, err := remote.URL(base, c.path, c.document)
		if err != nil || u.String() != c.want {
			t.Errorf("%s: %v %v, want %s", c.path, u, err, c.want)
		}
	}
	for _, bad := range []string{"a/../b", "a//b", "./a", "a\x00b"} {
		if _, err := remote.URL(base, bad, false); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	req, err := remote.Request(context.Background(), base, "lodash", true)
	if err != nil || req.Header.Get("Accept") != "application/json" || req.Method != "GET" {
		t.Errorf("request = %+v, %v", req, err)
	}
}

func TestR_NPM_025_DigestsOfTheAnnouncement(t *testing.T) {
	if got := remote.SHA512(sri("x") + " sha1-AAAAAAAAAAAAAAAAAAAAAAAAAAA="); len(got) != 64 {
		t.Errorf("sha512 of an SRI list = %d bytes", len(got))
	}
	if remote.SHA512("sha1-AAAAAAAAAAAAAAAAAAAAAAAAAAA=") != nil || remote.SHA512("") != nil {
		t.Error("a digest without sha512 has one")
	}
	if len(remote.SHA1("0123456789abcdef0123456789abcdef01234567")) != 20 || remote.SHA1("xyz") != nil {
		t.Error("shasum")
	}
}

func TestR_NPM_024_TheDocumentOfTensOfMegabytesIsCheckedAsAStream(t *testing.T) {
	// 1,000 versions with a 16 KiB description each: about 16 MB, as the large packages of the
	// public registry are. The check keeps the facts only.
	big := strings.Repeat("x", 16<<10)
	versions := make([]string, 0, 1000)
	for i := range 1000 {
		versions = append(versions, "1.0."+strconv.Itoa(i))
	}
	b := doc(t, "big", versions, func(m map[string]any) {
		for _, v := range m["versions"].(map[string]any) {
			v.(map[string]any)["description"] = big
		}
	})
	d, err := remote.Check(bytes.NewReader(b), "big")
	if err != nil || len(d.Versions) != 1000 {
		t.Fatalf("%v %d", err, len(d.Versions))
	}
}

// FuzzRewrite: Rewrite never panics, and what it writes is a JSON document that
// rewriting again does not change (the idempotence check 4 of the conformance kit asks for).
func FuzzRewrite(f *testing.F) {
	f.Add(doc(f, "left-pad", []string{"1.0.0", "1.1.0"}, nil))
	f.Add(doc(f, "@acme/ui", []string{"2.0.0-rc.1"}, nil))
	f.Add([]byte(`{"name":"x","versions":{"1.0.0":{"dist":{"tarball":"http://h/x.tgz"}}}}`))
	f.Add([]byte(`{"versions":{"a":null,"b":[],"c":{"dist":null}}}`))
	f.Add([]byte(`[]`))
	f.Fuzz(func(t *testing.T, in []byte) {
		prefix := "https://t.example/api/npm/r/x/-/"
		out, err := remote.Rewrite(in, "x", prefix)
		if err != nil {
			return
		}
		if !json.Valid(out) {
			t.Fatalf("the output is no JSON: %s", out)
		}
		again, err := remote.Rewrite(out, "x", prefix)
		if err != nil || !bytes.Equal(out, again) {
			t.Fatalf("not idempotent: %v\n%s\n%s", err, out, again)
		}
		p, err := remote.Parse(out)
		if err != nil {
			t.Fatal(err)
		}
		for v, raw := range p.Versions {
			var e struct {
				Dist struct {
					Tarball string `json:"tarball"`
				} `json:"dist"`
			}
			if json.Unmarshal(raw, &e) == nil && e.Dist.Tarball != prefix+"x-"+v+".tgz" {
				t.Fatalf("%q: tarball %q", v, e.Dist.Tarball)
			}
		}
		if ab, err := p.Abbreviated("x", prefix, true); err != nil || !json.Valid(ab) {
			t.Fatalf("abbreviated: %v %s", err, ab)
		}
	})
}
