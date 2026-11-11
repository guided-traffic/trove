package docgen_test

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/formats/ansible/docgen"
	"github.com/guided-traffic/trove/internal/formats/ansible/rows"
)

// build feeds the members to a generator the way the archive reader's tap does (Wants first, the capped body after) and builds the set.
func build(t testing.TB, lim docgen.Limits, readme string, members map[string]string) rows.DocSet {
	t.Helper()
	g := docgen.NewGenerator(lim)
	paths := make([]string, 0, len(members))
	for p := range members {
		paths = append(paths, p)
	}
	sortStrings(paths)
	for _, p := range paths {
		max, ok := g.Wants(p)
		if !ok {
			continue
		}
		body := []byte(members[p])
		if int64(len(body)) > max {
			body = body[:max]
		}
		g.Take(p, body)
	}
	return g.Build(readme)
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func entry(t testing.TB, set rows.DocSet, typ, name string) rows.DocEntry {
	t.Helper()
	for _, e := range set.Entries {
		if e.Type == typ && e.Name == name {
			return e
		}
	}
	t.Fatalf("no entry %s %s in %+v", typ, name, set.Entries)
	return rows.DocEntry{}
}

func codes(set rows.DocSet) []string {
	var out []string
	for _, w := range set.Warnings {
		out = append(out, w.Code)
	}
	return out
}

const module = `#!/usr/bin/python
from ansible.module_utils.basic import AnsibleModule

DOCUMENTATION = r'''
module: check_host_reachable
short_description: Checks whether a host answers
description:
  - Connects to C(host) and reports I(reachable).
  - See U(https://example.com/x).
version_added: "1.2.0"
author: Hans
options:
  host:
    description: The host.
    type: str
    required: true
  retries:
    type: int
    default: 1000
    description: How often.
  proxy:
    type: dict
    description: Proxy.
    suboptions:
      url:
        type: str
        description: The URL.
'''

EXAMPLES = """
- name: x
  check_host_reachable:
    host: a
  # </script>
"""

RETURN = '''
reachable:
  description: Whether it answered.
  returned: always
  type: bool
  sample: true
details:
  type: dict
  description: More.
  returned: success
  contains:
    rtt:
      type: float
      description: Milliseconds.
'''

def main():
    DOCUMENTATION = "not module level"
`

func TestR_ANS_031_TheDefaultLimitsAreTheBoundsOfTheSpec(t *testing.T) {
	want := docgen.Limits{
		Literal: 512 << 10, Source: 2 << 20, SourceBudget: 256 << 20,
		Entry: 1 << 20, EntriesPerVersion: 5000, BytesPerVersion: 16 << 20, File: 512 << 10, Files: 64,
	}
	if got := docgen.DefaultLimits(); got != want {
		t.Errorf("DefaultLimits = %+v, want %+v", got, want)
	}
}

// TestR_ANS_034_AModuleIsDocumentedFromItsLiterals: a module with a raw triple-quoted DOCUMENTATION, an EXAMPLES and a RETURN gives the
// doc_strings of R-ANS-035: options and return values as named lists with their nested members, typed scalars, the keys of the contract.
func TestR_ANS_034_AModuleIsDocumentedFromItsLiterals(t *testing.T) {
	set := build(t, docgen.DefaultLimits(), "", map[string]string{
		"plugins/modules/check_host_reachable.py": module,
		"README.md":       "# hans_fischer.common\n",
		"docs/install.md": "# Install\n",
	})
	e := entry(t, set, "module", "check_host_reachable")
	if !e.HasDocs || e.ShortDescription != "Checks whether a host answers" {
		t.Fatalf("entry = %+v", e)
	}
	var ds struct {
		Doc      map[string]any   `json:"doc"`
		Return   []map[string]any `json:"return"`
		Examples string           `json:"examples"`
		Metadata any              `json:"metadata"`
	}
	if err := json.Unmarshal(e.Doc, &ds); err != nil {
		t.Fatal(err)
	}
	opts := ds.Doc["options"].([]any)
	host, retries, proxy := opts[0].(map[string]any), opts[1].(map[string]any), opts[2].(map[string]any)
	if host["name"] != "host" || host["required"] != true || retries["default"] != float64(1000) || retries["type"] != "int" {
		t.Errorf("options = %v", opts)
	}
	if sub := proxy["suboptions"].([]any); len(sub) != 1 || sub[0].(map[string]any)["name"] != "url" {
		t.Errorf("suboptions = %v", proxy["suboptions"])
	}
	if ds.Doc["module"] != "check_host_reachable" || ds.Doc["plugin_type"] != "module" || ds.Doc["version_added"] != "1.2.0" {
		t.Errorf("doc = %v", ds.Doc)
	}
	if len(ds.Return) != 2 || ds.Return[0]["name"] != "reachable" || ds.Return[1]["contains"].([]any)[0].(map[string]any)["name"] != "rtt" {
		t.Errorf("return = %v", ds.Return)
	}
	if !strings.Contains(ds.Examples, "</script>") || ds.Metadata != nil {
		t.Errorf("examples %q, metadata %v", ds.Examples, ds.Metadata)
	}
	for _, want := range []string{"check host reachable", "Checks whether a host answers", "retries", "rtt", "Connects to host and reports reachable"} {
		if !strings.Contains(e.SearchText, want) {
			t.Errorf("search text %q lacks %q", e.SearchText, want)
		}
	}
	if set.Readme == nil || set.Readme.Name != "README.md" || set.Readme.Format != rows.DocFormatMarkdown || len(set.Files) != 1 || set.Files[0].Name != "install.md" {
		t.Errorf("readme %+v files %+v", set.Readme, set.Files)
	}
	if set.Source != rows.DocSourceGenerated || set.Generator != docgen.Version || len(set.Warnings) != 0 {
		t.Errorf("set = %+v", set)
	}
}

// TestR_ANS_034_WhatTheScannerDoesNotReadIsListedWithoutDocumentation: a DOCUMENTATION assembled by a call, inside a function, in an f-string
// or in bytes, is not read and gives no warning; __init__ and private files are no items; module_utils and doc_fragments are listed without
// docs; a sidecar wins over the source.
func TestR_ANS_034_WhatTheScannerDoesNotReadIsListedWithoutDocumentation(t *testing.T) {
	set := build(t, docgen.DefaultLimits(), "", map[string]string{
		"plugins/modules/joined.py":           "DOCUMENTATION = ''.join(['a: b'])\n",
		"plugins/modules/infunc.py":           "def f():\n    DOCUMENTATION = '''a: b'''\n",
		"plugins/modules/fstr.py":             "DOCUMENTATION = f'''a: {x}'''\n",
		"plugins/modules/bytes.py":            "DOCUMENTATION = b'''a: b'''\n",
		"plugins/modules/__init__.py":         "",
		"plugins/modules/_private.py":         "DOCUMENTATION = '''module: p'''\n",
		"plugins/module_utils/net/helpers.py": "x = 1\n",
		"plugins/doc_fragments/shared.py":     "class ModuleDocFragment: DOCUMENTATION = '''a: b'''\n",
		"plugins/modules/sided.py":            "DOCUMENTATION = '''\nshort_description: from source\n'''\n",
		"plugins/modules/sided.yml":           "DOCUMENTATION:\n  short_description: from sidecar\n  options:\n    x:\n      type: str\n",
		"plugins/filter/is_cidr.py":           "DOCUMENTATION = (\n    'short_description: Checks a CIDR\\n'\n    'description: x\\n'\n)\n",
		"plugins/modules/storage/s3.py":       "DOCUMENTATION = '''short_description: s3'''\n",
	})
	if got := codes(set); len(got) != 0 {
		t.Errorf("warnings = %v, want none", got)
	}
	for _, n := range []string{"joined", "infunc", "fstr", "bytes"} {
		if e := entry(t, set, "module", n); e.HasDocs {
			t.Errorf("%s has docs", n)
		}
	}
	for _, e := range set.Entries {
		if strings.Contains(e.Name, "init") || strings.Contains(e.Name, "private") {
			t.Errorf("entry %+v should not exist", e)
		}
	}
	for _, n := range [][2]string{{"module_utils", "net.helpers"}, {"doc_fragments", "shared"}} {
		if e := entry(t, set, n[0], n[1]); e.HasDocs {
			t.Errorf("%v has docs", n)
		}
	}
	if e := entry(t, set, "module", "sided"); !e.HasDocs || e.ShortDescription != "from sidecar" {
		t.Errorf("sidecar entry = %+v", e)
	}
	if e := entry(t, set, "filter", "is_cidr"); !e.HasDocs || e.ShortDescription != "Checks a CIDR" {
		t.Errorf("parenthesized concatenation = %+v", e)
	}
	if e := entry(t, set, "module", "storage.s3"); !e.HasDocs {
		t.Errorf("nested name = %+v", e)
	}
	for i := 1; i < len(set.Entries); i++ {
		a, b := set.Entries[i-1], set.Entries[i]
		if a.Type > b.Type || a.Type == b.Type && a.Name >= b.Name {
			t.Errorf("entries are not in type and name order: %s %s before %s %s", a.Type, a.Name, b.Type, b.Name)
		}
	}
}

// TestR_ANS_034_ABoundOrARefusalDropsThePartAndWarns: an unterminated literal, a literal over its bound, YAML with an anchor, a merge key or
// too many nodes, a bad name and a README that is not Markdown each give a warning and never an error.
func TestR_ANS_034_ABoundOrARefusalDropsThePartAndWarns(t *testing.T) {
	many := "DOCUMENTATION = '''\n" + strings.Repeat("- a\n", 30000) + "'''\n"
	set := build(t, docgen.DefaultLimits(), "README.rst", map[string]string{
		"plugins/modules/open.py":      "DOCUMENTATION = '''\nshort_description: x\n",
		"plugins/modules/huge.py":      "DOCUMENTATION = '''" + strings.Repeat("a: b\n", 120000) + "'''\n",
		"plugins/modules/anchor.py":    "DOCUMENTATION = '''\na: &x\n  b: c\nd: *x\n'''\n",
		"plugins/modules/merge.py":     "DOCUMENTATION = '''\nbase: &b\n  x: 1\nm:\n  <<: *b\n'''\n",
		"plugins/modules/nodes.py":     many,
		"plugins/modules/Bad__Name.py": "x = 1\n",
		"plugins/modules/bad__two.py":  "x = 1\n",
		"README.rst":                   "x\n",
	})
	want := map[string]string{
		"open": docgen.CodeLiteralUnterminate, "huge": docgen.CodeLiteralTooLarge, "anchor": docgen.CodeYAMLRefused,
		"merge": docgen.CodeYAMLRefused, "nodes": docgen.CodeYAMLRefused,
	}
	for name, code := range want {
		if e := entry(t, set, "module", name); e.HasDocs {
			t.Errorf("%s has docs", name)
		}
		found := false
		for _, w := range set.Warnings {
			found = found || w.Code == code && strings.Contains(w.Path, name+".py")
		}
		if !found {
			t.Errorf("%s: no warning %s in %v", name, code, set.Warnings)
		}
	}
	got := strings.Join(codes(set), " ")
	if !strings.Contains(got, docgen.CodeInvalidName) || !strings.Contains(got, docgen.CodeReadmeNotMarkdown) || set.Readme != nil {
		t.Errorf("warnings = %s, readme %v", got, set.Readme)
	}
}

// TestR_ANS_034_TheBoundsOfAVersionKeepTheListAndDropTheRest: 6,000 plugin files keep 5,000 entries and set truncated, a source past the budget is
// listed without documentation, and more than 64 files of docs/ keep 64.
func TestR_ANS_034_TheBoundsOfAVersionKeepTheListAndDropTheRest(t *testing.T) {
	members := map[string]string{}
	for i := range 6000 {
		members[fmt.Sprintf("plugins/filter/f%05d.py", i)] = "x = 1\n"
	}
	set := build(t, docgen.DefaultLimits(), "", members)
	if len(set.Entries) != 5000 || !set.Truncated || codes(set)[0] != docgen.CodeEntriesTruncated {
		t.Errorf("%d entries, truncated %v, warnings %v", len(set.Entries), set.Truncated, codes(set))
	}

	lim := docgen.DefaultLimits()
	lim.SourceBudget = 10
	set = build(t, lim, "", map[string]string{
		"plugins/modules/a.py": "DOCUMENTATION = '''\nshort_description: a\n'''\n",
		"plugins/modules/b.py": "DOCUMENTATION = '''\nshort_description: b\n'''\n",
	})
	if entry(t, set, "module", "a").HasDocs != true || entry(t, set, "module", "b").HasDocs || !reflect.DeepEqual(codes(set), []string{docgen.CodeBudgetExhausted}) {
		t.Errorf("budget: %+v %v", set.Entries, codes(set))
	}

	md := map[string]string{}
	for i := range 70 {
		md[fmt.Sprintf("docs/f%02d.md", i)] = "# f\n"
	}
	set = build(t, docgen.DefaultLimits(), "", md)
	if len(set.Files) != 64 || !set.Truncated {
		t.Errorf("%d files, truncated %v", len(set.Files), set.Truncated)
	}
}

// TestR_ANS_034_RolesAreItemsWithTheirREADME: a directory below roles/ is a role item; its README is the readme_md; a bad name is a warning.
func TestR_ANS_034_RolesAreItemsWithTheirREADME(t *testing.T) {
	set := build(t, docgen.DefaultLimits(), "", map[string]string{
		"roles/webserver/README.md":      "# webserver\n",
		"roles/webserver/tasks/main.yml": "- debug: msg=x\n",
		"roles/Bad-Role/tasks/main.yml":  "- debug: msg=x\n",
		"roles/bare/tasks/main.yml":      "- debug: msg=x\n",
		"playbooks/site.yml":             "- hosts: all\n",
	})
	if e := entry(t, set, "role", "webserver"); e.ReadmeMD != "# webserver\n" || e.HasDocs {
		t.Errorf("role = %+v", e)
	}
	if e := entry(t, set, "role", "bare"); e.ReadmeMD != "" {
		t.Errorf("role without README = %+v", e)
	}
	if len(set.Entries) != 2 {
		t.Errorf("entries = %+v", set.Entries)
	}
}

// TestR_ANS_034_TheSameArchiveGivesTheSameRows: the generator is deterministic.
func TestR_ANS_034_TheSameArchiveGivesTheSameRows(t *testing.T) {
	members := map[string]string{
		"plugins/modules/check_host_reachable.py": module, "plugins/lookup/x.py": "DOCUMENTATION = '''short_description: x'''\n",
		"README.md": "# t\n", "roles/r/README.md": "# r\n",
	}
	a, b := build(t, docgen.DefaultLimits(), "", members), build(t, docgen.DefaultLimits(), "", members)
	if !reflect.DeepEqual(a, b) {
		t.Error("two builds of one archive differ")
	}
}

// TestR_ANS_034_TheGeneratorImportsNoProcessPackage: go list -deps of the package names no os/exec, so nothing can be executed.
func TestR_ANS_034_TheGeneratorImportsNoProcessPackage(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "github.com/guided-traffic/trove/internal/formats/ansible/docgen").Output()
	if err != nil {
		t.Skipf("go list: %v", err)
	}
	for _, p := range strings.Fields(string(out)) {
		if p == "os/exec" {
			t.Error("docgen depends on os/exec")
		}
	}
}

func FuzzR_ANS_034_Generator(f *testing.F) {
	f.Add(module)
	f.Add("DOCUMENTATION = (\n 'a: b'\n 'c: d'\n)\nEXAMPLES = \"\"\"x\"\"\"\n")
	f.Add("DOCUMENTATION = '''a: &x b\nc: *x'''")
	f.Add("X = r'\\")
	f.Fuzz(func(t *testing.T, src string) {
		lim := docgen.DefaultLimits()
		lim.Literal, lim.Source = 4096, 8192
		build(t, lim, "", map[string]string{"plugins/modules/m.py": src, "plugins/modules/m.yml": src, "docs/a.md": src, "roles/r/README.md": src})
	})
}
