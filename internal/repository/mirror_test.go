package repository

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// mirrorCatalog holds a format whose remote adapter can discover (the catalog fact Mirror), with the well-known upstream
// of remoteCatalog, and the npmlike format, which cannot.
func mirrorCatalog() *catalog.Registry {
	r := remoteCatalog()
	r.Register(catalog.Entry{Facts: catalog.Facts{
		ID: "mirrorable", Classes: []Class{Local, Remote, Virtual}, Layout: "mirrorable", ProtocolBasePath: "/api/mirrorable/{key}/",
		PatternSubject: SubjectName, PackageNames: true,
		DefaultUpstreamURL: "https://registry.example.org/", DefaultMetadataTTLSeconds: intp(7200), DefaultNotFoundTTLSeconds: intp(1800),
	}, Names: lower{}, Mirror: true})
	return r
}

// validateMirrorDoc validates a remote document of format with the include list include ("" for none).
func validateMirrorDoc(t *testing.T, format, include, remote string) (Document, *validation) {
	t.Helper()
	body := `{"key":"m-remote","class":"remote","format":"` + format + `"`
	if include != "" {
		body += `,"includePatterns":` + include
	}
	body += `,"remote":` + remote + `}`
	d, err := DecodeDocument([]byte(body))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	_, v := validate(mirrorCatalog(), &d, true)
	return d, v
}

func fieldCodes(v *validation) map[string]string {
	out := map[string]string{}
	for _, f := range v.fields {
		out[f.Pointer] = f.Code
	}
	return out
}

func warningCodes(v *validation) map[string]string {
	out := map[string]string{}
	for _, w := range v.warnings {
		out[w.Pointer] = w.Code
	}
	return out
}

func TestMirrorSettings(t *testing.T) {
	t.Run("R-REPO-050 a remote created with only url reads back pullThrough and every mirror member with its default", func(t *testing.T) {
		d, v := validateMirrorDoc(t, "mirrorable", "", `{"url":"https://partner.example.test/"}`)
		if err := v.err(); err != nil {
			t.Fatal(err)
		}
		s := settingsOf(t, d)
		if s.Mode != ModePullThrough || !reflect.DeepEqual(s.Mirror, DefaultMirrorSettings()) || s.Mirrored() {
			t.Fatalf("mode %q, mirror %+v", s.Mode, s.Mirror)
		}
		var raw struct {
			Mirror map[string]json.RawMessage `json:"mirror"`
		}
		if err := json.Unmarshal(d.Remote, &raw); err != nil {
			t.Fatal(err)
		}
		for _, m := range []string{
			"paused", "intervalSeconds", "fullPassIntervalSeconds", "discovery", "concurrency",
			"maxVersionsPerPackage", "includePrereleases", "maxBytes",
		} {
			if _, ok := raw.Mirror[m]; !ok {
				t.Errorf("member mirror.%s is missing from %s", m, d.Remote)
			}
		}
	})
	t.Run("R-REPO-050 a section stored before the members existed reads back with them at their defaults", func(t *testing.T) {
		d, v := validateMirrorDoc(t, "mirrorable", "", `{"url":"https://partner.example.test/"}`)
		if err := v.err(); err != nil {
			t.Fatal(err)
		}
		var old map[string]json.RawMessage
		if err := json.Unmarshal(d.Remote, &old); err != nil {
			t.Fatal(err)
		}
		delete(old, "mode")
		delete(old, "mirror")
		stored, _ := json.Marshal(old)
		cfg, _ := json.Marshal(map[string]json.RawMessage{"remote": stored})
		doc, err := toDocument(q.Repository{Key: "m-remote", Class: "remote", Format: "mirrorable", Config: cfg}, mirrorCatalog())
		if err != nil {
			t.Fatal(err)
		}
		s := settingsOf(t, doc)
		if s.Mode != ModePullThrough || !reflect.DeepEqual(s.Mirror, DefaultMirrorSettings()) {
			t.Fatalf("read back mode %q, mirror %+v", s.Mode, s.Mirror)
		}
		if string(doc.Remote) != string(d.Remote) {
			t.Fatalf("the read-back section differs from the canonical one:\n%s\n%s", doc.Remote, d.Remote)
		}
	})
	t.Run("R-REPO-050 the ranges and the enumerations of the mirror members", func(t *testing.T) {
		for remote, pointer := range map[string]string{
			`{"mirror":{"concurrency":0}}`:                `/remote/mirror/concurrency`,
			`{"mirror":{"concurrency":65}}`:               `/remote/mirror/concurrency`,
			`{"mirror":{"intervalSeconds":299}}`:          `/remote/mirror/intervalSeconds`,
			`{"mirror":{"fullPassIntervalSeconds":3600}}`: `/remote/mirror/fullPassIntervalSeconds`,
			`{"mirror":{"maxVersionsPerPackage":-1}}`:     `/remote/mirror/maxVersionsPerPackage`,
			`{"mirror":{"maxBytes":1024}}`:                `/remote/mirror/maxBytes`,
			`{"mirror":{"discovery":"crawl"}}`:            `/remote/mirror/discovery`,
			`{"mode":"replica"}`:                          `/remote/mode`,
		} {
			full := `{"url":"https://partner.example.test/",` + strings.TrimPrefix(remote, "{")
			_, v := validateMirrorDoc(t, "mirrorable", "", full)
			want := "out_of_range"
			if strings.Contains(remote, "crawl") || strings.Contains(remote, "replica") {
				want = "invalid_value"
			}
			if got := fieldCodes(v)[pointer]; got != want {
				t.Errorf("%s: %s = %q, want %q (%v)", remote, pointer, got, want, fieldCodes(v))
			}
		}
		_, v := validateMirrorDoc(t, "mirrorable", "", `{"url":"https://partner.example.test/","mode":"mirror","mirror":{"fullPassIntervalSeconds":0,"maxBytes":1048576,"concurrency":64}}`)
		if err := v.err(); err != nil {
			t.Fatalf("the bounds themselves are refused: %v", err)
		}
	})
	t.Run("R-REPO-050 a format that cannot discover refuses mode mirror with not_applicable", func(t *testing.T) {
		_, v := validateMirrorDoc(t, "npmlike", `["@acme/*"]`, `{"url":"https://partner.example.test/","mode":"mirror"}`)
		if got := fieldCodes(v)["/remote/mode"]; got != "not_applicable" {
			t.Fatalf("/remote/mode = %q (%v)", got, fieldCodes(v))
		}
	})
}

func TestMirrorBound(t *testing.T) {
	const known = `"url":"https://registry.example.org/","mode":"mirror"`
	for _, tc := range []struct {
		name, include, remote string
		refused, warned       bool
	}{
		{"R-REPO-052 the well-known upstream without a bound is refused", "", `{` + known + `}`, true, false},
		{"R-REPO-052 a literal prefix bounds it", `["@types/*"]`, `{` + known + `}`, false, false},
		{"R-REPO-052 exact names bound it", `["lodash","react"]`, `{` + known + `}`, false, false},
		{"R-REPO-052 ** does not bound it", `["**"]`, `{` + known + `}`, true, false},
		{"R-REPO-052 one unbounded pattern unbounds the list", `["@types/*","@*/*"]`, `{` + known + `}`, true, false},
		{"R-REPO-052 maxBytes bounds it", "", `{` + known + `,"mirror":{"maxBytes":10737418240}}`, false, false},
		{"R-REPO-052 another upstream without a bound is saved with a warning", "", `{"url":"https://partner.example.test/","mode":"mirror"}`, false, true},
		{"R-REPO-052 pullThrough needs no bound", "", `{"url":"https://registry.example.org/"}`, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, v := validateMirrorDoc(t, "mirrorable", tc.include, tc.remote)
			if got := fieldCodes(v)["/remote/mode"] == "mirror_unbounded"; got != tc.refused {
				t.Fatalf("refused = %v (%v)", got, fieldCodes(v))
			}
			if got := warningCodes(v)["/remote/mode"] == WarnMirrorUnbounded; got != tc.warned {
				t.Fatalf("warned = %v (%v)", got, warningCodes(v))
			}
		})
	}
}

func TestR_AUTHZ_026_DelegatedMirrorRules(t *testing.T) {
	const (
		pull   = `{"url":"https://partner.example.test/"}`
		mirror = `{"url":"https://partner.example.test/","mode":"mirror","mirror":{"concurrency":8,"maxBytes":10737418240}}`
	)
	fields := func(t *testing.T, stored, next string) map[string]string {
		t.Helper()
		var cur *Document
		if stored != "" {
			d, v := validateMirrorDoc(t, "mirrorable", "", stored)
			if err := v.err(); err != nil {
				t.Fatalf("stored: %v", err)
			}
			cur = &d
		}
		nd, v := validateMirrorDoc(t, "mirrorable", "", next)
		if err := v.err(); err != nil {
			t.Fatalf("next: %v", err)
		}
		delegatedRemote(v, Write{Delegate: "web"}, cur, &nd)
		return fieldCodes(v)
	}
	for _, tc := range []struct {
		name, stored, next string
		want               map[string]string
	}{
		{"Q-REPO-142 a create in mode pullThrough is allowed", "", pull, map[string]string{}},
		{"Q-REPO-142 a create in mode mirror is refused", "", mirror, map[string]string{"/remote/mode": "mirror_restricted", "/remote/mirror": "mirror_restricted"}},
		{"Q-REPO-142 switching a stored pullThrough remote to mirror is refused", pull, `{"url":"https://partner.example.test/","mode":"mirror"}`, map[string]string{"/remote/mode": "mirror_restricted"}},
		{"Q-REPO-142 the stored mirror sent back is kept", mirror, mirror, map[string]string{}},
		{"Q-REPO-142 pausing is allowed", mirror, `{"url":"https://partner.example.test/","mode":"mirror","mirror":{"paused":true,"concurrency":8,"maxBytes":10737418240}}`, map[string]string{}},
		{"Q-REPO-142 turning the mode back to pullThrough is allowed", mirror, `{"url":"https://partner.example.test/","mode":"pullThrough","mirror":{"concurrency":8,"maxBytes":10737418240}}`, map[string]string{}},
		{"Q-REPO-142 raising the byte bound is refused", mirror, `{"url":"https://partner.example.test/","mode":"mirror","mirror":{"concurrency":8,"maxBytes":21474836480}}`, map[string]string{"/remote/mirror": "mirror_restricted"}},
		{"Q-REPO-142 raising the concurrency is refused", mirror, `{"url":"https://partner.example.test/","mode":"mirror","mirror":{"concurrency":16,"maxBytes":10737418240}}`, map[string]string{"/remote/mirror": "mirror_restricted"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := fields(t, tc.stored, tc.next)
			for k := range got {
				if !strings.HasPrefix(k, "/remote/mode") && !strings.HasPrefix(k, "/remote/mirror") {
					delete(got, k)
				}
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("violations = %v, want %v", got, tc.want)
			}
		})
	}
}
