//go:build integration

package npm

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/npmstub"
	"github.com/guided-traffic/trove/test/npmstub/corpus"
)

func TestR_NPM_024_OneRequestOfTheUpstreamThenNoneThenOneConditional(t *testing.T) {
	r := newRemoteEnv(t, remoteOptions{MetadataTTL: 2})
	rep := r.start(1)[0]
	p := upstreamPackage(t, 3)

	a := r.view(rep, rRemote, p.Name, r.tokA, "Accept", abbreviatedAccept)
	if a.Status != 200 || len(versionsOf(t, a)) != len(p.Versions) {
		t.Fatalf("%d %.300s", a.Status, a.Body)
	}
	if ct := a.Header.Get("Content-Type"); ct != "application/vnd.npm.install-v1+json" {
		t.Errorf("Content-Type = %q: the abbreviated form was asked for", ct)
	}
	if got := a.Header.Get("Cache-Status"); !strings.Contains(got, "stored") && !strings.Contains(got, "uri-miss") {
		t.Errorf("Cache-Status = %q", got)
	}
	// R-NPM-024: both request variants use the one stored full document.
	full := r.view(rep, rRemote, p.Name, r.tokA)
	if full.Status != 200 || full.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("%d %v", full.Status, full.Header)
	}
	if got := r.upstreamDocs(p.Name); got != 1 {
		t.Fatalf("%d upstream requests for two requests inside the period, want 1", got)
	}
	var fd struct {
		Time    map[string]string `json:"time"`
		Maint   json.RawMessage   `json:"maintainers"`
		Version map[string]struct {
			NPMUser json.RawMessage `json:"_npmUser"`
		} `json:"versions"`
	}
	if err := json.Unmarshal(full.Body, &fd); err != nil || fd.Time["modified"] == "" {
		t.Errorf("the full document lacks time: %v %.200s", err, full.Body)
	}
	if got := r.stub.Counts().Documents(p.Name); got != 1 {
		t.Errorf("documents = %d", got)
	}
	if acc := r.stub.Counts().Entries; len(acc) == 0 {
		t.Fatal("the upstream counted nothing")
	}
	for _, e := range r.stub.Counts().Entries {
		if e.Kind == npmstub.KindDocument && e.Name == p.Name && !strings.Contains(e.Accept, "application/json") {
			t.Errorf("the upstream was asked for %q: the full document is what is stored", e.Accept)
		}
	}

	time.Sleep(2200 * time.Millisecond)
	again := r.view(rep, rRemote, p.Name, r.tokA)
	if again.Status != 200 {
		t.Fatalf("%d", again.Status)
	}
	if docs, cond := r.upstreamDocs(p.Name), r.stub.Counts().ConditionalDocuments(p.Name); docs != 2 || cond != 1 {
		t.Errorf("after the period: %d requests, %d conditional; want one conditional request more", docs, cond)
	}
	if got := again.Header.Get("Cache-Status"); !strings.Contains(got, "304") {
		t.Errorf("Cache-Status = %q: the revalidation was answered 304", got)
	}
	r.view(rep, rRemote, p.Name, r.tokA)
	if got := r.upstreamDocs(p.Name); got != 2 {
		t.Errorf("a request right after a revalidation reached the upstream: %d", got)
	}
}

func TestR_NPM_025_TarballAddressesAreOnTheServingRepository(t *testing.T) {
	r := newRemoteEnv(t, remoteOptions{})
	rep := r.start(1)[0]
	p := scopedPackage(t)
	for _, c := range []struct{ repo, hdr string }{{rRemote, ""}, {rAll, ""}, {rRemote, abbreviatedAccept}} {
		var hdr []string
		if c.hdr != "" {
			hdr = []string{"Accept", c.hdr}
		}
		a := r.view(rep, c.repo, strings.Replace(p.Name, "/", "%2f", 1), r.tokA, hdr...)
		if a.Status != 200 {
			t.Fatalf("%s: %d %s", c.repo, a.Status, a.Body)
		}
		tars := tarballsOf(t, a)
		if len(tars) != len(p.Versions) {
			t.Fatalf("%s: %d tarballs", c.repo, len(tars))
		}
		for v, addr := range tars {
			want := "http://trove.test/api/npm/" + c.repo + "/" + p.Name + "/-/" + corpus.TarballFile(p.Name, v)
			if addr != want {
				t.Errorf("%s %s: tarball %q, want %q", c.repo, v, addr, want)
			}
			if strings.Contains(addr, r.run.Addr) {
				t.Errorf("the upstream's address is in %q", addr)
			}
		}
	}
	// The cache view names itself.
	cv := r.view(rep, rRemote+"~cache", strings.Replace(p.Name, "/", "%2f", 1), r.tokA)
	if cv.Status != 200 || !strings.Contains(string(cv.Body), "/api/npm/npm-remote~cache/") {
		t.Errorf("cache view: %d %.300s", cv.Status, cv.Body)
	}
}

func TestR_NPM_024_FiftyConcurrentRequestsOverThreeReplicasCauseOneUpstreamRequest(t *testing.T) {
	for _, valkey := range []bool{true, false} {
		name := "Valkey stopped"
		limit := 3
		if valkey {
			name, limit = "Valkey running", 1
		}
		t.Run(name, func(t *testing.T) {
			r := newRemoteEnv(t, remoteOptions{Valkey: valkey})
			reps := r.start(3)
			p := upstreamPackage(t, 3)
			// A slow upstream makes the requests overlap, as a cold cache does.
			if err := r.stub.SetMode(npmstub.Mode{DelayMS: map[string]int{"document": 400}}); err != nil {
				t.Fatal(err)
			}
			var wg sync.WaitGroup
			failures := make(chan string, 50)
			for i := range 50 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					hdr := []string{}
					if i%2 == 0 {
						hdr = []string{"Accept", abbreviatedAccept}
					}
					a := r.st.Do(reps[i%3], http.MethodGet, "/api/npm/"+rRemote+"/"+p.Name, r.tokA, nil, hdr...)
					if a.Status != 200 {
						failures <- string(a.Body)
					}
				}()
			}
			wg.Wait()
			close(failures)
			for f := range failures {
				t.Errorf("a request failed: %s", f)
			}
			if got := r.upstreamDocs(p.Name); got < 1 || got > limit {
				t.Errorf("%d upstream requests, want at most %d", got, limit)
			}
		})
	}
}
