package manifest

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func mustParse(t testing.TB, doc string) Manifest {
	t.Helper()
	m, err := Parse([]byte(doc), DefaultLimits())
	if err != nil {
		t.Fatalf("Parse(%s): %v", doc, err)
	}
	return m
}

// jsonOf renders a manifest with sorted keys for comparison.
func jsonOf(t testing.TB, m Manifest) string {
	t.Helper()
	b, err := m.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// runStep applies step to the manifest of in and compares the result with want,
// then applies the step again: every step is idempotent.
func runStep(t *testing.T, in, want string, step func(Manifest)) {
	t.Helper()
	m := mustParse(t, in)
	step(m)
	if got, w := jsonOf(t, m), jsonOf(t, mustParse(t, want)); got != w {
		t.Fatalf("step(%s)\n got  %s\n want %s", in, got, w)
	}
	step(m)
	if got, w := jsonOf(t, m), jsonOf(t, mustParse(t, want)); got != w {
		t.Fatalf("step is not idempotent: second run gives %s, want %s", got, w)
	}
}

func TestNormalizeIdentity(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"name trimmed", `{"name":"  @acme/ui "}`, `{"name":"@acme/ui"}`},
		{"name that is not a string stays", `{"name":5}`, `{"name":5}`},
		{"version cleaned", `{"version":"v1.2.3"}`, `{"version":"1.2.3"}`},
		{"version with build metadata", `{"version":" =1.2.3+b "}`, `{"version":"1.2.3"}`},
		{"a string that is no version stays", `{"version":"latest"}`, `{"version":"latest"}`},
	}
	for _, c := range cases {
		t.Run("R-NPM-007 step 3 "+c.name, func(t *testing.T) {
			runStep(t, c.in, c.want, func(m Manifest) { NormalizeName(m); NormalizeVersion(m) })
		})
	}
}

func TestNormalizeDependencies(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"object stays", `{"dependencies":{"a":"^1"}}`, `{"dependencies":{"a":"^1"}}`},
		{"array of name@range", `{"dependencies":["a@^1.0.0","b >=2","c","d@@1"]}`, `{"dependencies":{"a":"^1.0.0","b":">=2","c":"","d":"@1"}}`},
		{"string split on spaces and commas", `{"dependencies":"a@1, b@2"}`, `{"dependencies":{"a":"1","b":"2"}}`},
		{"optional dependencies as array", `{"optionalDependencies":["o@^3"]}`, `{"optionalDependencies":{"o":"^3"}}`},
		{"optional dependencies are not copied", `{"optionalDependencies":{"o":"1"}}`, `{"optionalDependencies":{"o":"1"}}`},
		{"non-string range removed", `{"dependencies":{"a":5,"b":"1"}}`, `{"dependencies":{"b":"1"}}`},
		{"non-object removed", `{"dependencies":5}`, `{}`},
		{"null removed", `{"dependencies":null}`, `{}`},
		{
			"shorthand expanded", `{"dependencies":{"a":"user/repo","b":"github:u/r.git","c":"git@github.com:u/r.git#v1","d":"https://github.com/u/r"}}`,
			`{"dependencies":{"a":"github:user/repo","b":"github:u/r","c":"git+ssh://git@github.com/u/r.git#v1","d":"git+https://github.com/u/r.git"}}`,
		},
		{
			"semver and tags stay", `{"dependencies":{"a":"^1.2.3 || 2.x","b":"latest","c":"npm:x@1","d":"file:../d"}}`,
			`{"dependencies":{"a":"^1.2.3 || 2.x","b":"latest","c":"npm:x@1","d":"file:../d"}}`,
		},
	}
	for _, c := range cases {
		t.Run("R-NPM-007 step 3 "+c.name, func(t *testing.T) { runStep(t, c.in, c.want, NormalizeDependencies) })
	}
}

func TestNormalizeBundleDependencies(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"renamed", `{"dependencies":{"a":"1"},"bundledDependencies":["a"]}`, `{"dependencies":{"a":"1"},"bundleDependencies":["a"]}`},
		{"the correct spelling wins", `{"dependencies":{"a":"1","b":"1"},"bundleDependencies":["a"],"bundledDependencies":["b"]}`, `{"dependencies":{"a":"1","b":"1"},"bundleDependencies":["a"]}`},
		{"true expands to the dependencies", `{"dependencies":{"b":"2","a":"1"},"bundleDependencies":true}`, `{"dependencies":{"b":"2","a":"1"},"bundleDependencies":["a","b"]}`},
		{"true without dependencies", `{"bundleDependencies":true}`, `{}`},
		{"an object is reduced to its keys", `{"dependencies":{"a":"1"},"bundleDependencies":{"a":"x","c":"y"}}`, `{"dependencies":{"a":"1","c":"*"},"bundleDependencies":["a","c"]}`},
		{"non-string members dropped", `{"dependencies":{"a":"1"},"bundleDependencies":["a",5,"",null,true]}`, `{"dependencies":{"a":"1"},"bundleDependencies":["a"]}`},
		{"a bundled name missing from dependencies is added with *", `{"bundleDependencies":["ghost"]}`, `{"dependencies":{"ghost":"*"},"bundleDependencies":["ghost"]}`},
		{"an existing range is kept", `{"dependencies":{"a":"^2"},"bundleDependencies":["a"]}`, `{"dependencies":{"a":"^2"},"bundleDependencies":["a"]}`},
		{"the empty name is no member in any form", `{"dependencies":{"":"1","a":"1"},"bundleDependencies":true}`, `{"dependencies":{"":"1","a":"1"},"bundleDependencies":["a"]}`},
		{"a list of only empty names is absence", `{"bundleDependencies":{"":1}}`, `{}`},
		{"false is absence", `{"bundleDependencies":false}`, `{}`},
		{"an empty list is absence", `{"bundleDependencies":[]}`, `{}`},
		{"a string is absence", `{"bundleDependencies":"a"}`, `{}`},
		{"sorted and without duplicates", `{"dependencies":{"a":"1","b":"1"},"bundleDependencies":["b","a","b"]}`, `{"dependencies":{"a":"1","b":"1"},"bundleDependencies":["a","b"]}`},
	}
	for _, c := range cases {
		t.Run("R-NPM-007 step 3 "+c.name, func(t *testing.T) { runStep(t, c.in, c.want, NormalizeBundleDependencies) })
	}
}

func TestNormalizeBin(t *testing.T) {
	facts := Facts{Files: []string{"tools/a", "tools/sub/b", "tools/.hidden", "tools/.d/x", "other.js", "cli.js"}}
	cases := []struct{ name, in, want string }{
		{"string on a plain name", `{"name":"tool","bin":"./cli.js"}`, `{"name":"tool","bin":{"tool":"cli.js"}}`},
		{"string on a scoped name", `{"name":"@acme/ui","bin":"cli.js"}`, `{"name":"@acme/ui","bin":{"ui":"cli.js"}}`},
		{"string without a name", `{"bin":"cli.js"}`, `{}`},
		{"array keyed by base name", `{"name":"x","bin":["./bin/a.js","bin\\b.js"]}`, `{"name":"x","bin":{"a.js":"bin/a.js","b.js":"bin/b.js"}}`},
		{"keys reduced to their base name", `{"name":"x","bin":{"dir/tool":"x.js"}}`, `{"name":"x","bin":{"tool":"x.js"}}`},
		{"targets made relative and cleaned", `{"name":"x","bin":{"a":"./../x//y/../z.js","b":"bin\\c.js","c":"/abs/d.js"}}`, `{"name":"x","bin":{"a":"x/z.js","b":"bin/c.js","c":"abs/d.js"}}`},
		{"non-string and empty entries dropped", `{"name":"x","bin":{"a":5,"b":"","c":".","d":"ok.js"}}`, `{"name":"x","bin":{"d":"ok.js"}}`},
		{"empty bin removed", `{"name":"x","bin":{}}`, `{"name":"x"}`},
		{"a bin of another type removed", `{"name":"x","bin":5}`, `{"name":"x"}`},
		{
			"directories.bin expands from the tarball", `{"name":"x","directories":{"bin":"./tools"}}`,
			`{"name":"x","directories":{"bin":"./tools"},"bin":{"a":"tools/a","sub":"tools/sub","b":"tools/sub/b"}}`,
		},
		{
			"directories.bin at the package root", `{"name":"x","directories":{"bin":"."}}`,
			`{"name":"x","directories":{"bin":"."},"bin":{"cli.js":"cli.js","other.js":"other.js","tools":"tools","a":"tools/a","sub":"tools/sub","b":"tools/sub/b"}}`,
		},
		{"directories.bin without files below it", `{"name":"x","directories":{"bin":"nothing"}}`, `{"name":"x","directories":{"bin":"nothing"}}`},
		{
			"directories.bin does not override bin", `{"name":"x","bin":"cli.js","directories":{"bin":"tools"}}`,
			`{"name":"x","bin":{"x":"cli.js"},"directories":{"bin":"tools"}}`,
		},
	}
	for _, c := range cases {
		t.Run("R-NPM-007 step 3 "+c.name, func(t *testing.T) {
			runStep(t, c.in, c.want, func(m Manifest) { NormalizeBin(m, facts) })
		})
	}
}

func TestNormalizeScripts(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"prefix removed", `{"scripts":{"postinstall":"node_modules/.bin/tsc"}}`, `{"scripts":{"postinstall":"tsc"}}`},
		{"prefix with ./", `{"scripts":{"install":"./node_modules/.bin/x y"}}`, `{"scripts":{"install":"x y"}}`},
		{"prefix with backslashes", `{"scripts":{"install":"node_modules\\.bin\\x"}}`, `{"scripts":{"install":"x"}}`},
		{"only a leading prefix", `{"scripts":{"test":"echo node_modules/.bin/x"}}`, `{"scripts":{"test":"echo node_modules/.bin/x"}}`},
		{"non-string entries dropped", `{"scripts":{"preinstall":5,"test":"t","x":null}}`, `{"scripts":{"test":"t"}}`},
		{"non-object removed", `{"scripts":"x"}`, `{}`},
	}
	for _, c := range cases {
		t.Run("R-NPM-007 step 3 "+c.name, func(t *testing.T) { runStep(t, c.in, c.want, NormalizeScripts) })
	}
}

func TestAddGypInstall(t *testing.T) {
	binding := Facts{Files: []string{"binding.gyp", "src/a.cc"}}
	other := Facts{Files: []string{"addon.gyp"}}
	nested := Facts{Files: []string{"src/binding.gyp"}}
	const gyp = `"install":"node-gyp rebuild"`
	cases := []struct {
		name  string
		facts Facts
		in    string
		want  string
	}{
		{"binding.gyp without install", binding, `{}`, `{"scripts":{` + gyp + `}}`},
		{"binding.gyp with an install script", binding, `{"scripts":{"install":"x"}}`, `{"scripts":{"install":"x"}}`},
		{"binding.gyp with a preinstall script adds it for Yarn 1", binding, `{"scripts":{"preinstall":"echo"}}`, `{"scripts":{"preinstall":"echo",` + gyp + `}}`},
		{"binding.gyp with gypfile false adds it for Yarn 1", binding, `{"gypfile":false}`, `{"gypfile":false,"scripts":{` + gyp + `}}`},
		{"another top-level gyp file", other, `{}`, `{"scripts":{` + gyp + `}}`},
		{"another gyp file with a preinstall script", other, `{"scripts":{"preinstall":"echo"}}`, `{"scripts":{"preinstall":"echo"}}`},
		{"another gyp file with gypfile false", other, `{"gypfile":false}`, `{"gypfile":false}`},
		{"a nested gyp file", nested, `{}`, `{}`},
		{"no gyp file", Facts{Files: []string{"index.js"}}, `{}`, `{}`},
	}
	for _, c := range cases {
		t.Run("R-NPM-007 step 3 "+c.name, func(t *testing.T) {
			runStep(t, c.in, c.want, func(m Manifest) { AddGypInstall(m, c.facts) })
		})
	}
}

func TestNormalizeEngines(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"array of engine and range", `{"engines":["node >=10","npm ^6"]}`, `{"engines":{"node":">=10","npm":"^6"}}`},
		{"no range", `{"engines":["node"]}`, `{"engines":{"node":"*"}}`},
		{"non-strings dropped", `{"engines":["node >=1",5]}`, `{"engines":{"node":">=1"}}`},
		{"object stays", `{"engines":{"node":">=18"}}`, `{"engines":{"node":">=18"}}`},
	}
	for _, c := range cases {
		t.Run("R-NPM-007 step 3 "+c.name, func(t *testing.T) { runStep(t, c.in, c.want, NormalizeEngines) })
	}
}

func TestFacts(t *testing.T) {
	t.Run("R-NPM-007 a dotfile and a nested file are no top-level gyp file", func(t *testing.T) {
		for _, files := range [][]string{{".gyp"}, {"a/b.gyp"}, {"x.gypi"}} {
			if (Facts{Files: files}).hasTopLevelGyp() {
				t.Fatalf("%v matched", files)
			}
		}
		if !(Facts{Files: []string{"x.gyp"}}).hasTopLevelGyp() {
			t.Fatal("x.gyp did not match")
		}
	})
}

// TestNormalizeAgainstNpm replays a corpus through npm's own prepare() step
// (testdata/prepare/generate.js): the manifest a client publishes and the raw
// package.json of the tarball must compare equal after trove's normalization,
// and trove must not change what npm already normalized.
func TestNormalizeAgainstNpm(t *testing.T) {
	var corpus []struct {
		Name     string   `json:"name"`
		Files    []string `json:"files"`
		Manifest Manifest `json:"manifest"`
	}
	readJSON(t, "testdata/prepare/corpus.json", &corpus)
	prepared := map[string]Manifest{}
	readJSON(t, "testdata/prepare/prepared.json", &prepared)
	if len(corpus) == 0 || len(prepared) != len(corpus) {
		t.Fatalf("corpus holds %d cases, prepared output %d", len(corpus), len(prepared))
	}
	for _, c := range corpus {
		t.Run("R-NPM-007 step 3 corpus "+c.Name, func(t *testing.T) {
			facts := Facts{Files: c.Files}
			request, ok := prepared[c.Name]
			if !ok {
				t.Fatal("no prepared output")
			}
			if diff := Mismatches(request, c.Manifest, facts); len(diff) != 0 {
				t.Errorf("npm's manifest and the raw package.json differ in %v\n request %s\n packaged %s\n normalized request %s\n normalized packaged %s",
					diff, jsonOf(t, request), jsonOf(t, c.Manifest), jsonOf(t, Normalize(request, facts)), jsonOf(t, Normalize(c.Manifest, facts)))
			}
			once := Normalize(c.Manifest, facts)
			if twice := Normalize(once, facts); !reflect.DeepEqual(once, twice) {
				t.Errorf("Normalize is not idempotent:\n once  %s\n twice %s", jsonOf(t, once), jsonOf(t, twice))
			}
		})
	}
}

func readJSON(t *testing.T, file string, v any) {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(v); err != nil {
		t.Fatalf("%s: %v", file, err)
	}
}
