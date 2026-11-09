package repository

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/openpgp"
)

// generated is a public key made for the test and its fingerprint.
func generated(t *testing.T) (armored, fingerprint string) {
	t.Helper()
	_, public, fp, err := openpgp.Generate("test", time.Now().Add(-2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return string(public), fp
}

func debDoc(class Class, member map[string]any, remote string) *Document {
	raw, _ := json.Marshal(member)
	d := &Document{Class: class, Format: "deb"}
	d.Deb = raw
	if remote != "" {
		d.Remote = json.RawMessage(remote)
	}
	return d
}

func debEntry() catalog.Entry { return catalog.Entry{ID: "deb"} }

func TestValidateDebChecksTheKeysAndDerivesTheFingerprints(t *testing.T) {
	a, fpA := generated(t)
	b, fpB := generated(t)
	d := debDoc(Remote, map[string]any{"upstreamKeys": a + b}, "")
	v := &validation{}
	validateDeb(v, debEntry(), d)
	if len(v.fields) != 0 || len(v.warnings) != 0 {
		t.Fatalf("a valid block: %+v %+v", v.fields, v.warnings)
	}
	var m struct {
		Fingerprints []string `json:"upstreamKeyFingerprints"`
	}
	if err := json.Unmarshal(d.Deb, &m); err != nil || len(m.Fingerprints) != 2 || m.Fingerprints[0] != fpA || m.Fingerprints[1] != fpB {
		t.Errorf("fingerprints = %v, %v", m.Fingerprints, err)
	}
	// What a client reads back and writes again is dropped before the schema sees it and derived again.
	stripDebReadOnly(debEntry(), d)
	if strings.Contains(string(d.Deb), "upstreamKeyFingerprints") {
		t.Error("the read-only member stays")
	}

	v = &validation{}
	validateDeb(v, debEntry(), debDoc(Remote, map[string]any{"upstreamKeys": "not a key"}, ""))
	if len(v.fields) != 1 || v.fields[0].Pointer != "/deb/upstreamKeys" || v.fields[0].Code != codeInvalidValue {
		t.Errorf("a block that is no key: %+v", v.fields)
	}
}

func TestValidateDebWarnsForARemoteWithoutKeysAndRefusesAMirrorWithout(t *testing.T) {
	v := &validation{}
	validateDeb(v, debEntry(), debDoc(Remote, map[string]any{}, `{"mode":"pullThrough"}`))
	if len(v.fields) != 0 || len(v.warnings) != 1 || v.warnings[0].Code != "deb_upstream_unverified" {
		t.Errorf("pull-through without keys: %+v %+v", v.fields, v.warnings)
	}
	v = &validation{}
	validateDeb(v, debEntry(), debDoc(Remote, map[string]any{}, `{"mode":"mirror"}`))
	if len(v.fields) != 1 || v.fields[0].Code != errs.CodeDebUpstreamKeysRequired || v.fields[0].Pointer != "/deb/upstreamKeys" {
		t.Errorf("a mirror without keys: %+v", v.fields)
	}
	// Another class or another format is not this rule's.
	v = &validation{}
	validateDeb(v, debEntry(), debDoc(Local, map[string]any{}, ""))
	validateDeb(v, catalog.Entry{ID: "npm"}, debDoc(Remote, map[string]any{}, ""))
	if len(v.fields) != 0 || len(v.warnings) != 0 {
		t.Errorf("other repositories: %+v %+v", v.fields, v.warnings)
	}
}

func TestDebMemberUnverifiedIsARemoteDebianRepositoryWithoutKeys(t *testing.T) {
	a, _ := generated(t)
	withKeys, _ := json.Marshal(map[string]any{"deb": map[string]any{"upstreamKeys": a}})
	for _, c := range []struct {
		format, class string
		config        string
		want          bool
	}{
		{"deb", "remote", `{"deb":{}}`, true},
		{"deb", "remote", `{}`, true},
		{"deb", "remote", `{"deb":{"upstreamKeys":null}}`, true},
		{"deb", "remote", string(withKeys), false},
		{"deb", "local", `{}`, false},
		{"npm", "remote", `{}`, false},
	} {
		if got := debMemberUnverified(c.format, c.class, []byte(c.config)); got != c.want {
			t.Errorf("%+v = %v", c, got)
		}
	}
}

func TestDebKeysRemovedListsTheFingerprintsAnUpdateTakesOut(t *testing.T) {
	a, fpA := generated(t)
	b, fpB := generated(t)
	cur := debDoc(Remote, map[string]any{"upstreamKeys": a + b}, "")
	cur.Format, cur.Class = "deb", Remote
	next := debDoc(Remote, map[string]any{"upstreamKeys": b}, "")
	if got := debKeysRemoved(cur, next); len(got) != 1 || got[0] != fpA {
		t.Errorf("removing a = %v", got)
	}
	none := debDoc(Remote, map[string]any{}, "")
	if got := debKeysRemoved(cur, none); len(got) != 2 {
		t.Errorf("removing all = %v", got)
	}
	if got := debKeysRemoved(next, cur); len(got) != 0 {
		t.Errorf("adding a = %v", got)
	}
	local := &Document{Class: Local, Format: "deb"}
	if got := debKeysRemoved(local, none); len(got) != 0 {
		t.Errorf("a local repository = %v", got)
	}
	_ = fpB
}

func TestTheNarrowedEntryCarriesTheRemovedFingerprintsAsAPart(t *testing.T) {
	e := narrowedEntry{RepositoryID: "1", Key: "p/r", DebUpstreamKeysRemoved: []string{strings.Repeat("A", 40)}}
	if parts := e.parts(); len(parts) != 1 || parts[0] != partDebUpstreamKeysRemoved {
		t.Errorf("parts = %v", parts)
	}
	for fp, ok := range map[string]bool{strings.Repeat("A", 40): true, strings.Repeat("a", 40): false, "ABCD": false, strings.Repeat("G", 40): false} {
		if validFingerprint(fp) != ok {
			t.Errorf("validFingerprint(%q) = %v", fp, !ok)
		}
	}
}
