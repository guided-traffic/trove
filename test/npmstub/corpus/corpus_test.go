package corpus

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
)

const testBase = "https://registry.test"

// digestOf hashes everything the corpus serves: both documents of every package and every tarball.
func digestOf(c *Corpus) string {
	h := sha256.New()
	for _, p := range c.Packages {
		h.Write(p.Document(testBase, false))
		h.Write(p.Document(testBase, true))
		for _, v := range p.Versions {
			h.Write([]byte(v.Dist.Integrity)) // the tarball bytes themselves are checked against it in TestTarballsMatchTheirIntegrityAndHaveNoScripts
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

func TestCorpusIsByteStable(t *testing.T) {
	// R-NPM-045: the test upstream is deterministic. Two generations give identical bytes, and the
	// bytes equal the committed digest, so a change of toolchain or generator shows up here.
	first, second := New(DefaultConfig()), New(DefaultConfig())
	a, b := digestOf(first), digestOf(second)
	if a != b {
		t.Fatalf("two generations differ: %s and %s", a, b)
	}
	want, err := os.ReadFile("testdata/corpus.sha256")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(want)); got != a {
		t.Fatalf("the corpus changed: digest %s, committed %s\n"+
			"If the change is intended, write the digest to test/npmstub/corpus/testdata/corpus.sha256 and run `make npm-reference`.", a, got)
	}
}

func TestCorpusShape(t *testing.T) {
	// R-NPM-045, E2.2: at least 600 packages with scopes, prereleases, dist-tags, deep and wide
	// trees, and a few packages with a thousand versions.
	c := Default()
	if len(c.Packages) < 600 {
		t.Fatalf("%d packages, want at least 600", len(c.Packages))
	}
	var scoped, pre, next, legacy, big int
	index := map[string]int{}
	for i, p := range c.Packages {
		index[p.Name] = i
		if strings.HasPrefix(p.Name, "@") {
			scoped++
		}
		if len(p.Versions) >= 1000 {
			big++
		}
		for _, v := range p.Versions {
			if IsPrerelease(v.Version) {
				pre++
			}
		}
		if _, ok := p.DistTags["next"]; ok {
			next++
		}
		if _, ok := p.DistTags["legacy"]; ok {
			legacy++
		}
		for tag, v := range p.DistTags {
			if p.Find(v) < 0 {
				t.Errorf("%s: dist-tag %s points to missing version %s", p.Name, tag, v)
			}
		}
		if IsPrerelease(p.DistTags["latest"]) {
			t.Errorf("%s: latest %s is a prerelease", p.Name, p.DistTags["latest"])
		}
	}
	for name, n := range map[string]int{"scoped": scoped, "prerelease versions": pre, "next tags": next, "legacy tags": legacy} {
		if n == 0 {
			t.Errorf("the corpus has no %s", name)
		}
	}
	if big < 2 {
		t.Errorf("%d packages with 1,000 versions, want at least 2", big)
	}
	for i, p := range c.Packages {
		for _, v := range p.Versions {
			for dep := range v.Dependencies {
				if j, ok := index[dep]; !ok || j >= i {
					t.Fatalf("%s@%s depends on %s, which is not an earlier package", p.Name, v.Version, dep)
				}
			}
		}
	}
	t.Log(c)
}

func TestVersionsAreOrderedAndLocalized(t *testing.T) {
	for _, p := range Default().Packages {
		for i := 1; i < len(p.Versions); i++ {
			if CompareVersions(p.Versions[i-1].Version, p.Versions[i].Version) >= 0 {
				t.Fatalf("%s: %s is not below %s", p.Name, p.Versions[i-1].Version, p.Versions[i].Version)
			}
			if !p.Versions[i].Time.After(p.Versions[i-1].Time) {
				t.Fatalf("%s: %s is not published after %s", p.Name, p.Versions[i].Version, p.Versions[i-1].Version)
			}
		}
	}
}

func TestTarballsMatchTheirIntegrityAndHaveNoScripts(t *testing.T) {
	// The documents' dist values are those of the bytes served; no manifest has a script.
	for _, p := range Default().Packages {
		for _, v := range p.Versions {
			tb := Tarball(p.Name, v.Version, v.Dependencies)
			sum := sha512.Sum512(tb)
			if got := "sha512-" + base64.StdEncoding.EncodeToString(sum[:]); got != v.Dist.Integrity {
				t.Fatalf("%s@%s: integrity %s, tarball hashes to %s", p.Name, v.Version, v.Dist.Integrity, got)
			}
			if m := manifestOf(t, tb); m["scripts"] != nil || m["name"] != p.Name || m["version"] != v.Version {
				t.Fatalf("%s@%s: manifest %v", p.Name, v.Version, m)
			}
		}
	}
}

func manifestOf(t *testing.T, tarball []byte) map[string]any {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(tarball))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(zr)
	for {
		hdr, err := tr.Next()
		if err != nil {
			t.Fatal("no package/package.json in the tarball:", err)
		}
		if hdr.Name != "package/package.json" {
			continue
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(body, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
}

func TestDocumentsHaveTheRegistryShape(t *testing.T) {
	p, _ := Default().Get(Default().Names()[0])
	var full, abbr map[string]any
	if err := json.Unmarshal(p.Document(testBase, false), &full); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(p.Document(testBase, true), &abbr); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"name", "dist-tags", "versions", "time", "readme"} {
		if full[k] == nil {
			t.Errorf("full document without %q", k)
		}
	}
	for _, k := range []string{"name", "dist-tags", "versions", "modified"} {
		if abbr[k] == nil {
			t.Errorf("abbreviated document without %q", k)
		}
	}
	if abbr["time"] != nil || abbr["readme"] != nil {
		t.Error("the abbreviated document carries time or readme")
	}
	first := p.Versions[0]
	want := TarballURL(testBase, p.Name, first.Version)
	got := full["versions"].(map[string]any)[first.Version].(map[string]any)["dist"].(map[string]any)["tarball"]
	if got != want {
		t.Errorf("tarball %v, want %s", got, want)
	}
}

func TestScopedTarballFile(t *testing.T) {
	if got := TarballFile("@lattice/amber-anchor", "1.2.3-beta.1"); got != "amber-anchor-1.2.3-beta.1.tgz" {
		t.Fatalf("got %s", got)
	}
	if v, ok := VersionOfFile("@lattice/amber-anchor", "amber-anchor-1.2.3-beta.1.tgz"); !ok || v != "1.2.3-beta.1" {
		t.Fatalf("got %q %v", v, ok)
	}
	if _, ok := VersionOfFile("amber-anchor", "other-1.0.0.tgz"); ok {
		t.Fatal("a file of another package matched")
	}
	if got := TarballURL("https://r.test/", "@a/b", "1.0.0"); got != "https://r.test/@a/b/-/b-1.0.0.tgz" {
		t.Fatalf("got %s", got)
	}
}

func TestReferenceClosure(t *testing.T) {
	// E2.2: the reference project resolves at least 500 packages. The closure by name is a lower
	// bound of the lockfile's entries.
	ref := Default().Reference(ReferenceMinNames)
	if len(ref.Dependencies) == 0 {
		t.Fatal("no root dependency")
	}
	if again := Default().Reference(ReferenceMinNames); !equalMaps(ref.Dependencies, again.Dependencies) {
		t.Fatal("the reference project is not deterministic")
	}
	seen := map[string]bool{}
	var walk func(string)
	walk = func(name string) {
		if seen[name] {
			return
		}
		seen[name] = true
		p, _ := Default().Get(name)
		for dep := range p.Versions[p.Find(p.DistTags["latest"])].Dependencies {
			walk(dep)
		}
	}
	for dep := range ref.Dependencies {
		walk(dep)
	}
	if len(seen) < ReferenceMinNames {
		t.Fatalf("the roots reach %d packages, want at least %d", len(seen), ReferenceMinNames)
	}
	t.Logf("%d roots reach %d packages", len(ref.Dependencies), len(seen))
}

func equalMaps(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func TestCompareVersions(t *testing.T) {
	ordered := []string{"0.1.0", "1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0", "1.0.1", "1.2.0", "2.0.0", "10.0.0"}
	for i := range ordered {
		for j := range ordered {
			want := sign(i - j)
			if got := CompareVersions(ordered[i], ordered[j]); got != want {
				t.Errorf("CompareVersions(%s, %s) = %d, want %d", ordered[i], ordered[j], got, want)
			}
		}
	}
}

func TestInsertRemoveRetag(t *testing.T) {
	p := Default().Packages[0].Clone()
	n := len(p.Versions)
	high := NewVersion(p.Name, "99.0.0", nil, p.Modified.AddDate(0, 0, 1))
	if !p.Insert(high) || p.Insert(high) {
		t.Fatal("insert must add a version once")
	}
	if p.Versions[len(p.Versions)-1].Version != "99.0.0" || len(p.Versions) != n+1 {
		t.Fatal("the new version is not last")
	}
	if len(Default().Packages[0].Versions) != n {
		t.Fatal("the clone shares its versions with the original")
	}
	if !p.Remove("99.0.0") || p.Remove("99.0.0") || len(p.Versions) != n {
		t.Fatal("remove must delete the version once")
	}
}
