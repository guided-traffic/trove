package publish

import (
	"crypto/sha1" //nolint:gosec // dist.shasum is a SHA-1
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/npm/manifest"
)

type envOpt func(*Envelope)

func sums(data []byte) (integrity, shasum string) {
	s512 := sha512.Sum512(data)
	s1 := sha1.Sum(data) //nolint:gosec // see the import
	return "sha512-" + base64.StdEncoding.EncodeToString(s512[:]), hex.EncodeToString(s1[:])
}

// envelope builds a well-formed envelope for "@acme/ui@1.2.0" with a tarball of the
// given bytes, then lets the options spoil it.
func envelope(t *testing.T, opts ...envOpt) *Envelope {
	t.Helper()
	data := []byte("tarball")
	integrity, shasum := sums(data)
	m := map[string]any{
		"name": "@acme/ui", "version": "1.2.0",
		"dist": map[string]any{"integrity": integrity, "shasum": shasum, "tarball": "http://x/y.tgz"},
	}
	raw, _ := json.Marshal(m)
	e := &Envelope{
		ID: "@acme/ui", Name: "@acme/ui",
		Versions: map[string]json.RawMessage{"1.2.0": raw},
		DistTags: map[string]string{"latest": "1.2.0"},
		Attachments: []Attachment{
			{Name: "@acme/ui-1.2.0.tgz", Length: int64(len(data)), Decoded: int64(len(data))},
		},
	}
	for _, o := range opts {
		o(e)
	}
	return e
}

func withManifest(f func(m map[string]any)) envOpt {
	return func(e *Envelope) {
		for k, raw := range e.Versions {
			var m map[string]any
			_ = json.Unmarshal(raw, &m)
			f(m)
			e.Versions[k], _ = json.Marshal(m)
		}
	}
}

func TestEnvelopeRequest(t *testing.T) {
	ok, err := envelope(t).Request("@acme/ui", manifest.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if ok.Name.String() != "@acme/ui" || ok.Version.Canonical() != "1.2.0" || ok.Tag != "latest" || ok.Tarball == nil || ok.Sigstore != nil {
		t.Fatalf("%+v", ok)
	}
	cases := []struct {
		name string
		opt  envOpt
		code string
	}{
		{"R-NPM-007 the name differs from the path", func(e *Envelope) { e.Name = "other" }, "npm_bad_publish"},
		{"R-NPM-007 the _id differs from the name", func(e *Envelope) { e.ID = "other" }, "npm_bad_publish"},
		{"R-NPM-007 no version", func(e *Envelope) { e.Versions = nil }, "npm_bad_publish"},
		{"R-NPM-007 two versions", func(e *Envelope) { e.Versions["1.2.1"] = e.Versions["1.2.0"] }, "npm_bad_publish"},
		{"R-NPM-007 a version that is no SemVer", func(e *Envelope) { e.Versions = map[string]json.RawMessage{"1.2": e.Versions["1.2.0"]} }, "npm_invalid_version"},
		{"R-NPM-007 a version with build metadata", func(e *Envelope) { e.Versions = map[string]json.RawMessage{"1.2.0+b": e.Versions["1.2.0"]} }, "npm_invalid_version"},
		{"R-NPM-007 no dist-tag", func(e *Envelope) { e.DistTags = nil }, "npm_bad_publish"},
		{"R-NPM-007 two dist-tags", func(e *Envelope) { e.DistTags["next"] = "1.2.0" }, "npm_bad_publish"},
		{"R-NPM-007 a tag that is a range", func(e *Envelope) { e.DistTags = map[string]string{"1.x": "1.2.0"} }, "npm_invalid_tag"},
		{"R-NPM-007 a tag that points elsewhere", func(e *Envelope) { e.DistTags = map[string]string{"latest": "9.9.9"} }, "npm_bad_publish"},
		{"R-NPM-007 the manifest names another package", withManifest(func(m map[string]any) { m["name"] = "other" }), "npm_manifest_mismatch"},
		{"R-NPM-007 the manifest names another version", withManifest(func(m map[string]any) { m["version"] = "1.2.1" }), "npm_manifest_mismatch"},
		{"R-NPM-007 a NUL in the manifest", withManifest(func(m map[string]any) { m["description"] = "a\x00b" }), "npm_bad_publish"},
		{"R-NPM-007 a NUL in a key of the manifest", withManifest(func(m map[string]any) { m["a\x00b"] = 1 }), "npm_bad_publish"},
		{"R-NPM-007 a manifest that is no object", func(e *Envelope) { e.Versions["1.2.0"] = json.RawMessage(`[1]`) }, "npm_bad_publish"},
		{"R-NPM-007 no tarball", func(e *Envelope) { e.Attachments = nil }, "npm_bad_publish"},
		{"R-NPM-007 two tarballs", func(e *Envelope) {
			e.Attachments = append(e.Attachments, Attachment{Name: "b.tgz", Length: 7, Decoded: 7})
		}, "npm_bad_publish"},
		{"R-NPM-007 two bundles", func(e *Envelope) {
			e.Attachments = append(e.Attachments, Attachment{Name: "a.sigstore", Length: 1, Decoded: 1}, Attachment{Name: "b.sigstore", Length: 1, Decoded: 1})
		}, "npm_bad_publish"},
		{"R-NPM-007 an attachment of another kind", func(e *Envelope) {
			e.Attachments = append(e.Attachments, Attachment{Name: "x.zip", Length: 1, Decoded: 1})
		}, "npm_bad_publish"},
		{"R-NPM-007 no length", func(e *Envelope) { e.Attachments[0].Length = -1 }, "npm_bad_publish"},
		{"R-NPM-007 the length differs from the data", func(e *Envelope) { e.Attachments[0].Length++ }, "npm_bad_publish"},
		{"R-NPM-007 neither integrity nor shasum", withManifest(func(m map[string]any) { m["dist"] = map[string]any{"tarball": "x"} }), "npm_bad_publish"},
		{"R-NPM-007 an integrity without a sha512 entry and no shasum", withManifest(func(m map[string]any) { m["dist"] = map[string]any{"integrity": "sha256-AAAA"} }), "npm_bad_publish"},
		{"R-NPM-007 a sha512 entry that is no digest", withManifest(func(m map[string]any) { m["dist"] = map[string]any{"integrity": "sha512-AAAA"} }), "npm_bad_publish"},
		{"R-NPM-007 a shasum that is no digest", withManifest(func(m map[string]any) { m["dist"] = map[string]any{"shasum": "xyz"} }), "npm_bad_publish"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := envelope(t, c.opt).Request("@acme/ui", manifest.DefaultLimits())
			if e, _ := errs.As(err); e == nil || e.Code != c.code || e.Status() != 400 {
				t.Fatalf("err = %v, want 400 %s", err, c.code)
			}
		})
	}
	t.Run("R-NPM-002 a name that is not valid for a new package is npm_invalid_name", func(t *testing.T) {
		for _, bad := range []string{"Acme-UI", "node_modules", "fs", "_x", "a b"} {
			_, err := envelope(t, func(e *Envelope) { e.Name, e.ID = bad, bad }).Request(bad, manifest.DefaultLimits())
			if e, _ := errs.As(err); e == nil || e.Code != "npm_invalid_name" || e.Status() != 400 {
				t.Errorf("%q: %v", bad, err)
			}
		}
	})
	t.Run("R-NPM-007 a version npm cleans is accepted under its canonical form", func(t *testing.T) {
		e := envelope(t, withManifest(func(m map[string]any) { m["version"] = "v1.2.0" }),
			func(e *Envelope) {
				e.Versions = map[string]json.RawMessage{"v1.2.0": e.Versions["1.2.0"]}
				e.DistTags["latest"] = "v1.2.0"
			})
		r, err := e.Request("@acme/ui", manifest.DefaultLimits())
		if err != nil || r.Version.Canonical() != "1.2.0" {
			t.Fatalf("%v %+v", err, r)
		}
	})
	t.Run("R-NPM-007 a provenance bundle is picked up next to the tarball", func(t *testing.T) {
		e := envelope(t, func(e *Envelope) {
			e.Attachments = append(e.Attachments, Attachment{Name: "@acme/ui-1.2.0.sigstore", Length: 4, Decoded: 4})
		})
		r, err := e.Request("@acme/ui", manifest.DefaultLimits())
		if err != nil || r.Sigstore == nil || !strings.HasSuffix(r.Sigstore.Name, ".sigstore") {
			t.Fatalf("%v %+v", err, r)
		}
	})
}

func TestEnvelopeCheckDigests(t *testing.T) {
	data := []byte("tarball")
	s512, s1 := sha512.Sum512(data), sha1.Sum(data) //nolint:gosec // dist.shasum is a SHA-1
	req, err := envelope(t).Request("@acme/ui", manifest.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if err := req.CheckDigests(s512[:], s1[:]); err != nil {
		t.Fatal(err)
	}
	bad := sha512.Sum512([]byte("other"))
	for name, c := range map[string]struct{ a, b []byte }{
		"R-NPM-007 the sha512 differs": {bad[:], s1[:]},
		"R-NPM-007 the sha1 differs":   {s512[:], make([]byte, 20)},
	} {
		t.Run(name, func(t *testing.T) {
			e, _ := errs.As(req.CheckDigests(c.a, c.b))
			if e == nil || e.Status() != 409 {
				t.Fatalf("%v", e)
			}
		})
	}
	t.Run("R-NPM-007 every sha512 entry of the integrity must match", func(t *testing.T) {
		integrity, _ := sums(data)
		other := sha512.Sum512([]byte("other"))
		both := integrity + " sha512-" + base64.StdEncoding.EncodeToString(other[:]) + " sha256-xyz"
		r, err := envelope(t, withManifest(func(m map[string]any) { m["dist"] = map[string]any{"integrity": both} })).Request("@acme/ui", manifest.DefaultLimits())
		if err != nil {
			t.Fatal(err)
		}
		if e, _ := errs.As(r.CheckDigests(s512[:], nil)); e == nil || e.Status() != 409 {
			t.Fatalf("%v", e)
		}
	})
	t.Run("R-NPM-007 an integrity alone and a shasum alone are enough", func(t *testing.T) {
		integrity, shasum := sums(data)
		for _, dist := range []map[string]any{{"integrity": integrity}, {"shasum": shasum}, {"shasum": strings.ToUpper(shasum)}} {
			r, err := envelope(t, withManifest(func(m map[string]any) { m["dist"] = dist })).Request("@acme/ui", manifest.DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			if err := r.CheckDigests(s512[:], s1[:]); err != nil {
				t.Fatalf("%v: %v", dist, err)
			}
		}
	})
}
