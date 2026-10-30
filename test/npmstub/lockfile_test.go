package npmstub

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/test/npmstub/corpus"
)

// lockfileDir is the reference project, committed by `make npm-reference`.
const lockfileDir = "../../perf/npm/reference/"

// lockBase is where the lockfile's tarball addresses point: npm-all of the dev profile.
const lockBase = "https://localhost:8080/api/npm/npm-all/"

func TestReferenceLockfileMatchesTheCorpus(t *testing.T) {
	// E2.2: the reference lockfile has at least 500 packages, all from the corpus, with the
	// corpus's integrity values and the dev profile's addresses. Together with
	// `make npm-reference-check` (which regenerates it with npm) this fails when the committed
	// lockfile and the corpus drift apart; this part needs neither Docker nor Node.
	wantProject := corpus.Default().Reference(corpus.ReferenceMinNames).PackageJSON()
	gotProject, err := os.ReadFile(lockfileDir + "package.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotProject, wantProject) {
		t.Fatal("perf/npm/reference/package.json is not the corpus's reference project; run `make npm-reference`")
	}

	raw, err := os.ReadFile(lockfileDir + "package-lock.json")
	if err != nil {
		t.Fatal(err)
	}
	var lock struct {
		LockfileVersion int `json:"lockfileVersion"`
		Packages        map[string]struct {
			Version          string `json:"version"`
			Resolved         string `json:"resolved"`
			Integrity        string `json:"integrity"`
			HasInstallScript bool   `json:"hasInstallScript"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(raw, &lock); err != nil {
		t.Fatal(err)
	}
	if lock.LockfileVersion != 3 {
		t.Errorf("lockfileVersion %d, want 3", lock.LockfileVersion)
	}
	names := map[string]bool{}
	for path, e := range lock.Packages {
		if path == "" {
			continue
		}
		name := path[strings.LastIndex(path, "node_modules/")+len("node_modules/"):]
		pkg, ok := corpus.Default().Get(name)
		if !ok {
			t.Fatalf("%s is not a corpus package", path)
		}
		i := pkg.Find(e.Version)
		if i < 0 {
			t.Fatalf("%s: version %s is not in the corpus", path, e.Version)
		}
		if e.Integrity != pkg.Versions[i].Dist.Integrity {
			t.Errorf("%s: integrity %s, corpus %s", path, e.Integrity, pkg.Versions[i].Dist.Integrity)
		}
		if want := corpus.TarballURL(lockBase, name, e.Version); e.Resolved != want {
			t.Errorf("%s: resolved %s, want %s", path, e.Resolved, want)
		}
		if e.HasInstallScript {
			t.Errorf("%s has an install script", path)
		}
		names[name] = true
	}
	if n := len(lock.Packages) - 1; n < 500 {
		t.Fatalf("%d locked packages, want at least 500", n)
	}
	t.Logf("%d locked packages (%d distinct names)", len(lock.Packages)-1, len(names))
}
