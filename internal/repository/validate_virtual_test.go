package repository

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
)

func virtualDoc(section string) string {
	return `{"key":"all","class":"virtual","format":"pkgfmt","virtual":` + section + `}`
}

func TestValidateVirtual(t *testing.T) {
	t.Run("R-REPO-017 defaults: no members, 7200 seconds, no pin rules, a warning", func(t *testing.T) {
		d, v, err := decodeValidate(t, `{"key":"all","class":"virtual","format":"pkgfmt"}`, true)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := json.Marshal(d.Virtual)
		if string(got) != `{"members":[],"deployTarget":null,"mergedMetadataTtlSeconds":7200,"pinning":{"rules":[]}}` {
			t.Fatalf("virtual = %s", got)
		}
		if len(v.warnings) != 1 || v.warnings[0].Code != "no_members" {
			t.Fatalf("warnings = %+v", v.warnings)
		}
	})
	t.Run("R-REPO-017 a document with members is valid and carries no warning", func(t *testing.T) {
		_, v, err := decodeValidate(t, virtualDoc(`{"members":["local","remote"],"deployTarget":"local","mergedMetadataTtlSeconds":0}`), true)
		if err != nil || len(v.warnings) != 0 {
			t.Fatalf("%v %+v", err, v.warnings)
		}
	})
	t.Run("R-REPO-008 R-REPO-017 a member listed twice, empty or the repository itself is invalid_member", func(t *testing.T) {
		_, _, err := decodeValidate(t, virtualDoc(`{"members":["a","a","","all"]}`), true)
		f := fieldsOf(err)
		for _, p := range []string{"/virtual/members/1", "/virtual/members/2", "/virtual/members/3"} {
			if f[p] != errs.CodeInvalidMember {
				t.Errorf("%s = %q (%v)", p, f[p], f)
			}
		}
		if _, ok := f["/virtual/members/0"]; ok {
			t.Error("the first occurrence of a member was reported")
		}
	})
	t.Run("R-REPO-021 R-REPO-008 a deploy target outside the members is invalid_deploy_target", func(t *testing.T) {
		_, _, err := decodeValidate(t, virtualDoc(`{"members":["a"],"deployTarget":"b"}`), true)
		if f := fieldsOf(err); f["/virtual/deployTarget"] != "invalid_deploy_target" {
			t.Fatalf("fields = %v", f)
		}
	})
	t.Run("R-REPO-017 mergedMetadataTtlSeconds is 0 to 86400", func(t *testing.T) {
		for _, ttl := range []string{"-1", "86401"} {
			_, _, err := decodeValidate(t, virtualDoc(`{"members":["a"],"mergedMetadataTtlSeconds":`+ttl+`}`), true)
			if f := fieldsOf(err); f["/virtual/mergedMetadataTtlSeconds"] != "out_of_range" {
				t.Errorf("%s: fields = %v", ttl, f)
			}
		}
		if _, _, err := decodeValidate(t, virtualDoc(`{"members":["a"],"mergedMetadataTtlSeconds":86400}`), true); err != nil {
			t.Error(err)
		}
	})
	t.Run("R-REPO-023 R-REPO-008 pin rules: patterns, members and duplicates", func(t *testing.T) {
		_, _, err := decodeValidate(t, virtualDoc(`{"members":["a","b"],"pinning":{"rules":[
			{"packages":["@p/*","a*b"],"members":["a","c","a"]},
			{"packages":["@p/*"],"members":["b"]},
			{"packages":[],"members":[]}]}}`), true)
		f := fieldsOf(err)
		want := map[string]string{
			"/virtual/pinning/rules/0/packages/1": "invalid_pattern",
			"/virtual/pinning/rules/0/members/1":  "invalid_pin_member",
			"/virtual/pinning/rules/0/members/2":  "invalid_pin_member",
			"/virtual/pinning/rules/1/packages/0": "invalid_pattern",
			"/virtual/pinning/rules/2/packages":   "out_of_range",
		}
		for p, code := range want {
			if f[p] != code {
				t.Errorf("%s = %q, want %q (%v)", p, f[p], code, f)
			}
		}
	})
	t.Run("R-REPO-023 pin rule members are kept in the order of the member list", func(t *testing.T) {
		d, _, err := decodeValidate(t, virtualDoc(`{"members":["a","b","c"],"pinning":{"rules":[{"packages":["x"],"members":["c","a"]}]}}`), true)
		if err != nil {
			t.Fatal(err)
		}
		if got := d.Virtual.Pinning.Rules[0].Members; !slices.Equal(got, []string{"a", "c"}) {
			t.Fatalf("members = %v", got)
		}
	})
	t.Run("R-REPO-023 a pin rule may name no members: its names answer 404", func(t *testing.T) {
		if _, _, err := decodeValidate(t, virtualDoc(`{"members":["a"],"pinning":{"rules":[{"packages":["x"],"members":[]}]}}`), true); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("R-REPO-023 pin rule limits: 200 rules, 50 patterns", func(t *testing.T) {
		rules := make([]PinRule, MaxPinRules+1)
		for i := range rules {
			rules[i] = PinRule{Packages: []string{"p"}, Members: []string{}}
		}
		doc := Document{Key: "all", Class: Virtual, Format: "pkgfmt", Settings: Settings{Virtual: &VirtualSettings{Members: []string{"a"}, Pinning: Pinning{Rules: rules}}}}
		doc.normalize()
		_, v := validate(testCatalog(), &doc, true)
		if fieldsOf(v.err())["/virtual/pinning/rules"] != "out_of_range" {
			t.Fatalf("fields = %v", fieldsOf(v.err()))
		}
	})
	t.Run("R-REPO-008 the virtual section applies to virtual repositories", func(t *testing.T) {
		_, _, err := decodeValidate(t, `{"key":"x","class":"local","format":"pkgfmt","virtual":{"members":[]}}`, true)
		if f := fieldsOf(err); f["/virtual"] != errs.CodeNotApplicable {
			t.Fatalf("fields = %v", f)
		}
	})
	t.Run("R-REPO-022 priorityMember on a virtual repository is not applicable", func(t *testing.T) {
		_, _, err := decodeValidate(t, `{"key":"all","class":"virtual","format":"pkgfmt","priorityMember":true}`, true)
		if f := fieldsOf(err); f["/priorityMember"] != errs.CodeNotApplicable {
			t.Fatalf("fields = %v", f)
		}
	})
	t.Run("R-REPO-008 a misspelt member of the virtual section is a malformed document", func(t *testing.T) {
		_, err := DecodeDocument([]byte(virtualDoc(`{"member":["a"]}`)))
		if e, ok := errs.As(err); !ok || e.Status() != 400 {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestStoredConfigKeepsNoReferences(t *testing.T) {
	d, err := DecodeDocument([]byte(virtualDoc(`{"members":["a","b"],"deployTarget":"a","mergedMetadataTtlSeconds":60,"pinning":{"rules":[{"packages":["x"],"members":["a"]}]}}`)))
	if err != nil {
		t.Fatal(err)
	}
	stored, err := d.marshalConfig()
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(stored, &m); err != nil {
		t.Fatal(err)
	}
	if string(m["virtual"]) != `{"mergedMetadataTtlSeconds":60}` {
		t.Fatalf("stored virtual section = %s", m["virtual"])
	}
	full, err := d.marshalFull()
	if err != nil {
		t.Fatal(err)
	}
	var fm map[string]json.RawMessage
	_ = json.Unmarshal(full, &fm)
	var vs VirtualSettings
	if err := json.Unmarshal(fm["virtual"], &vs); err != nil || !slices.Equal(vs.Members, []string{"a", "b"}) || *vs.DeployTarget != "a" {
		t.Fatalf("the full document lost the references: %s", fm["virtual"])
	}
	t.Run("R-REPO-017 a stored configuration reads back to the document with the references rendered from rows", func(t *testing.T) {
		var s Settings
		if err := json.Unmarshal(stored, &s); err != nil {
			t.Fatal(err)
		}
		doc := Document{Key: "all", Class: Virtual, Format: "pkgfmt", Settings: s}
		rel := &virtualRel{
			members: []relRef{{1, "a"}, {2, "b"}}, target: &relRef{1, "a"},
			pins: []relPin{{patterns: []string{"x"}, members: []relRef{{1, "a"}}}},
		}
		doc.renderVirtual(rel)
		if got := doc.Virtual; !slices.Equal(got.Members, []string{"a", "b"}) || *got.DeployTarget != "a" ||
			*got.MergedMetadataTTLSeconds != 60 || len(got.Pinning.Rules) != 1 || got.Pinning.Rules[0].Members[0] != "a" {
			t.Fatalf("rendered = %+v", got)
		}
		again, _ := doc.marshalFull()
		if string(again) != string(full) {
			t.Fatalf("the rendered document differs from the written one:\n%s\n%s", again, full)
		}
	})
}

func TestPatternUnscopedOnlyWarning(t *testing.T) {
	cases := []struct {
		name, body string
		warn       []string
	}{
		{"exclude *", `"excludePatterns":["*"]`, []string{"/excludePatterns"}},
		{"include *", `"includePatterns":["*"]`, []string{"/includePatterns"}},
		{"* with **", `"excludePatterns":["*","**"]`, nil},
		{"* with @*/*", `"excludePatterns":["*","@*/*"]`, nil},
		{"only **", `"excludePatterns":["**"]`, nil},
		{"only a scope", `"excludePatterns":["@acme/*"]`, nil},
		{"both lists", `"includePatterns":["*"],"excludePatterns":["*"]`, []string{"/includePatterns", "/excludePatterns"}},
	}
	for _, c := range cases {
		t.Run("R-REPO-005 R-REPO-008 pattern_unscoped_only: "+c.name, func(t *testing.T) {
			_, v, err := decodeValidate(t, `{"key":"rem","class":"remote","format":"pkgfmt","remote":{"url":"https://upstream.example.test/"},`+c.body+`}`, true)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, w := range v.warnings {
				if w.Code == "pattern_unscoped_only" {
					got = append(got, w.Pointer)
				}
			}
			if !slices.Equal(got, c.warn) {
				t.Fatalf("warnings at %v, want %v", got, c.warn)
			}
		})
	}
	t.Run("R-REPO-005 a format with path patterns gets no such warning", func(t *testing.T) {
		_, v, err := decodeValidate(t, `{"key":"gen","class":"local","format":"generic","excludePatterns":["*"]}`, true)
		if err != nil || len(v.warnings) != 0 {
			t.Fatalf("%v %+v", err, v.warnings)
		}
	})
}
