//go:build integration

package deblocal

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/integration/testenv"
)

func (e *env) getOn(r *testenv.Replica, path, token string) testenv.Answer {
	e.t.Helper()
	return e.st.Do(r, "GET", e.base()+path, token, nil)
}

func (e *env) generation(suite string) int64 {
	e.t.Helper()
	return e.st.Number(`SELECT s.generation FROM deb_suites s JOIN repositories r ON r.id = s.repo_id WHERE r.key = $1 AND s.suite = $2`, repo, suite)
}

// byHashOf are the by-hash paths of the index files the Release of a suite lists (only the compressed files exist).
func (e *env) byHashOf(r *testenv.Replica, suite string) []string {
	return only(e.allByHashOf(r, suite), "binary-amd64")
}

// only keeps the paths that contain the text. The index of an architecture without files is the same empty file in every generation, so
// its hash stays named by the current one; the index of amd64 changes with each upload.
func only(paths []string, text string) []string {
	var out []string
	for _, p := range paths {
		if strings.Contains(p, text) {
			out = append(out, p)
		}
	}
	return out
}

func (e *env) allByHashOf(r *testenv.Replica, suite string) []string {
	e.t.Helper()
	a := e.getOn(r, "dists/"+suite+"/Release", e.tokA)
	if a.Status != 200 {
		e.t.Fatalf("Release = %d", a.Status)
	}
	ps := paragraphs(e.t, a.Body)
	var out []string
	for _, l := range strings.Split(ps[0].Value("SHA256"), "\n")[1:] {
		f := strings.Fields(l)
		if strings.HasSuffix(f[2], ".gz") || strings.HasSuffix(f[2], ".xz") {
			dir := f[2][:strings.LastIndex(f[2], "/")]
			out = append(out, "dists/"+suite+"/"+dir+"/by-hash/SHA256/"+f[0])
		}
	}
	if len(out) == 0 {
		e.t.Fatal("the Release lists no index file")
	}
	return out
}

// R-DEB-010: every replica of a process that claims jobs has an index builder of its own, so a fault injected into one does not reach the
// builds of another (the builder was one pointer per process before).
func TestR_DEB_010_EveryReplicaHasItsOwnIndexBuilder(t *testing.T) {
	e := newEnvOpts(t, envOpts{replicas: 2})
	a, b := e.rs[0].App.DebIndexer(), e.rs[1].App.DebIndexer()
	if a == nil || b == nil {
		t.Fatalf("builders = %v and %v, want one on each replica", a, b)
	}
	if a == b {
		t.Fatal("both replicas share one index builder")
	}
	a.BeforeCommit = func(string) error { return errors.New("injected fault") }
	if b.BeforeCommit != nil {
		t.Error("the fault injected into the first replica's builder is on the second's")
	}
}

// R-DEB-010, R-DEB-031: fifty uploads of distinct packages in parallel through three replicas are all listed in the published index of
// the suite, the generation grows monotonically as every replica reads it, and fewer builds ran than uploads. No session affinity: the
// published generation is the same on every replica.
func TestR_DEB_010_FiftyParallelUploadsThroughThreeReplicas(t *testing.T) {
	e := newEnvOpts(t, envOpts{replicas: 3})
	const n = 50
	var wg sync.WaitGroup
	seqs := make([]int64, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			name := fmt.Sprintf("par%02d", i)
			a := e.st.Do(e.rs[i%3], "PUT", uploadPath(name+"_1.0-1_amd64.deb", []string{"stable"}), e.tokA, build(t, name, "1.0-1", nil), "Content-Type", debMedia)
			if a.Status != 201 {
				t.Errorf("upload %s = %d %s", name, a.Status, a.Body)
				return
			}
			seqs[i] = seqOf(e, a.JSON(t), "stable")
		}()
	}

	// The generation is read on every replica while the uploads run; it only grows.
	stop := make(chan struct{})
	var sampler sync.WaitGroup
	for i := range e.rs {
		sampler.Add(1)
		go func() {
			defer sampler.Done()
			var last int64
			for {
				select {
				case <-stop:
					return
				case <-time.After(200 * time.Millisecond):
				}
				a := e.st.Do(e.rs[i], "GET", "/api/v1/projects/"+project+"/repositories/"+repo+"/deb/index", e.tokA, nil)
				if a.Status != 200 {
					continue
				}
				var body struct {
					Suites []suiteState `json:"suites"`
				}
				if err := json.Unmarshal(a.Body, &body); err != nil || len(body.Suites) == 0 {
					continue
				}
				if g := body.Suites[0].Generation; g < last {
					t.Errorf("replica %d read generation %d after %d", i+1, g, last)
				} else {
					last = g
				}
			}
		}()
	}
	wg.Wait()
	var max int64
	for _, s := range seqs {
		max = max64(max, s)
	}
	for i := range e.rs {
		e.waitIndexOn(e.rs[i], "stable", max)
	}
	close(stop)
	sampler.Wait()

	rel := paragraphs(t, e.releaseOn(e.rs[1], "stable"))[0]
	if ps := e.packagesOn(e.rs[2], "stable", rel, "main/binary-amd64/Packages.gz"); len(ps) != n {
		t.Errorf("the index lists %d of %d uploads", len(ps), n)
	}
	if builds := e.metric("trove_deb_index_builds_total", `outcome="published"`); builds >= n {
		t.Errorf("%v builds ran for %d uploads", builds, n)
	}
}

// R-DEB-012: a client that read InRelease of generation n can fetch the index files that InRelease names by hash on every replica
// while later generations are published, and after the keep time and a further build only the last three superseded generations' files
// are left.
func TestR_DEB_012_ByHashFilesOfEarlierGenerationsOnEveryReplica(t *testing.T) {
	e := newEnvOpts(t, envOpts{replicas: 3})
	upload := func(i int) {
		name := fmt.Sprintf("gen%02d", i)
		a := e.mustUpload(e.tokA, name+"_1.0-1_amd64.deb", build(t, name, "1.0-1", nil), 201, []string{"stable"})
		e.waitIndex("stable", seqOf(e, a, "stable"))
	}
	upload(0)
	n := e.byHashOf(e.rs[0], "stable") // the files InRelease names, read by the client (the index of amd64: it changes with each upload)
	for i := 1; i <= 3; i++ {
		upload(i)
	}
	for i, r := range e.rs {
		for _, p := range n {
			if a := e.st.Do(r, "GET", e.base()+p, e.tokA, nil); a.Status != 200 {
				t.Errorf("replica %d: generation n's %s = %d after three newer generations", i+1, p, a.Status)
			}
		}
	}

	// The keep time passes: the superseded generations are older than deb.by_hash_keep.
	e.st.Exec(`UPDATE deb_generations SET superseded_at = now() - interval '2 hours' WHERE superseded_at IS NOT NULL`)
	upload(4)
	last3 := e.byHashOf(e.rs[0], "stable")
	for i, r := range e.rs {
		for _, p := range n {
			if a := e.st.Do(r, "GET", e.base()+p, e.tokA, nil); a.Status != 404 {
				t.Errorf("replica %d: generation n's %s = %d after the keep time and a further build, want 404", i+1, p, a.Status)
			}
		}
		for _, p := range last3 {
			if a := e.st.Do(r, "GET", e.base()+p, e.tokA, nil); a.Status != 200 {
				t.Errorf("replica %d: the current %s = %d", i+1, p, a.Status)
			}
		}
	}
	// The three newest superseded generations stay whatever their age.
	kept := e.st.Number(`SELECT count(*) FROM deb_generations g JOIN repositories r ON r.id = g.repo_id WHERE r.key = $1 AND g.suite = 'stable' AND g.superseded_at IS NOT NULL`, repo)
	if kept != 3 {
		t.Errorf("%d superseded generations are kept, want 3", kept)
	}
}

// R-DEB-010: a by-hash item that a test deletes between two builds is present again after the next build that lists it, because the
// commit upserts the by-hash item of every file of its generation.
func TestR_DEB_010_ADeletedByHashItemIsWrittenAgainByTheNextBuild(t *testing.T) {
	e := newEnv(t)
	a := e.mustUpload(e.tokA, "hello_2.10-3_amd64.deb", build(t, "hello", "2.10-3", nil), 201, []string{"stable"})
	e.waitIndex("stable", seqOf(e, a, "stable"))
	paths := e.byHashOf(e.r, "stable")
	victim := paths[0]
	e.st.Exec(`DELETE FROM items WHERE generated AND path = $1`, strings.TrimPrefix(victim, ""))
	if g := e.get(strings.TrimPrefix(victim, ""), e.tokA); g.Status != 404 {
		t.Fatalf("the deleted by-hash item answers %d", g.Status)
	}
	// A reindex is a full build of rows that did not change: it lists the same files again.
	if r := e.st.Do(e.r, "POST", "/api/v1/projects/"+project+"/repositories/"+repo+"/reindex", e.tokAdmin, nil); r.Status != 202 {
		t.Fatalf("reindex = %d %s", r.Status, r.Body)
	}
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if e.get(victim, e.tokA).Status == 200 {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Errorf("%s is still missing after the next build", victim)
}

// R-DEB-011: two full builds from unchanged rows publish byte-identical Packages.gz and Packages.xz (and so equal by-hash names).
func TestR_DEB_011_TwoFullBuildsPublishIdenticalPackagesFiles(t *testing.T) {
	e := newEnvOpts(t, envOpts{cfg: `{"deb":{"compression":["xz"]}}`})
	a := e.mustUpload(e.tokA, "hello_2.10-3_amd64.deb", build(t, "hello", "2.10-3", nil), 201, []string{"stable"})
	e.waitIndex("stable", seqOf(e, a, "stable"))
	read := func() (gz, xz []byte, gen int64) {
		return e.get("dists/stable/main/binary-amd64/Packages.gz", e.tokA).Body, e.get("dists/stable/main/binary-amd64/Packages.xz", e.tokA).Body, e.generation("stable")
	}
	gz1, xz1, gen1 := read()
	if len(gz1) == 0 || len(xz1) == 0 {
		t.Fatalf("Packages.gz %d bytes, Packages.xz %d bytes", len(gz1), len(xz1))
	}
	for i := 0; i < 2; i++ {
		before := e.generation("stable")
		if r := e.st.Do(e.r, "POST", "/api/v1/projects/"+project+"/repositories/"+repo+"/reindex", e.tokAdmin, nil); r.Status != 202 {
			t.Fatalf("reindex = %d %s", r.Status, r.Body)
		}
		deadline := time.Now().Add(60 * time.Second)
		for e.generation("stable") == before && time.Now().Before(deadline) {
			time.Sleep(300 * time.Millisecond)
		}
		if e.generation("stable") == before {
			t.Fatal("the reindex published no generation")
		}
	}
	gz2, xz2, gen2 := read()
	if gen2 <= gen1 {
		t.Fatalf("generation %d does not follow %d", gen2, gen1)
	}
	if !bytes.Equal(gz1, gz2) || !bytes.Equal(xz1, xz2) {
		t.Error("full builds of unchanged rows gave other Packages.gz or Packages.xz bytes")
	}
}

func (e *env) releaseOn(r *testenv.Replica, suite string) []byte {
	e.t.Helper()
	a := e.getOn(r, "dists/"+suite+"/Release", e.tokA)
	if a.Status != 200 {
		e.t.Fatalf("Release = %d", a.Status)
	}
	return a.Body
}
