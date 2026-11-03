//go:build integration

package bastion

import (
	"net/http"
	"testing"
	"time"
)

// variants are the Bastion configurations the cache scenarios run in: each cache backend, with and without
// Valkey, one or two replicas (R-BAS-048).
type variant struct {
	name string
	o    bastionOptions
}

func variants() []variant {
	return []variant{
		{"volume", bastionOptions{Backend: "volume"}},
		{"volume+valkey", bastionOptions{Backend: "volume", Valkey: true}},
		{"bucket+valkey+2replicas", bastionOptions{Backend: "bucket", Valkey: true, Replicas: 2}},
		{"bucket", bastionOptions{Backend: "bucket"}},
	}
}

// TestR_BAS_021_R_BAS_022_R_BAS_023_ColdAndWarmCache is the roadmap criterion for npm and NuGet and for every
// caller: a cold run fetches every package and document once from the main installation; a second run with
// an empty client cache within metadata_revalidate_after sends no request for a tarball, a package or a
// document; after that period only conditional requests that transfer no artifact reach the main
// installation. $FULL reads everything; $PART reads what its policy covers and is refused the rest with the
// main installation's own answer; $NONE and the anonymous caller are refused.
func TestR_BAS_021_R_BAS_022_R_BAS_023_ColdAndWarmCache(t *testing.T) {
	w := newWorld(t, worldOptions{})
	w.content()
	w.anonymousPolicy()
	w.setAnonymous(true)

	for _, v := range variants() {
		t.Run(v.name, func(t *testing.T) {
			o := v.o
			o.Cache = "    metadata_revalidate_after: 3s\n"
			b := w.newBastion(t, o).wait()
			r0 := b.reps[0]
			r1 := b.reps[len(b.reps)-1]
			full := callOpts{token: w.full.token, addr: "203.0.113.10"}

			// Cold: every package and document once, through replica 0.
			m := w.front.Mark()
			s := npmRun(t, r0, npmAll, full, "@acme/sdk", "plain-lib")
			if !s.ok() || s.artifacts != 2 {
				t.Fatalf("cold npm run: %+v", s)
			}
			g := nugetRun(t, r0, ngAll, full, "Acme.Sdk", "Other.Lib")
			if !g.ok() || g.artifacts != 2 {
				t.Fatalf("cold nuget run: %+v", g)
			}
			if n := len(w.front.Artifacts(m)); n != 4 {
				t.Fatalf("a cold run cost %d artifact transfers at the main installation, want 4", n)
			}

			// Warm, within the metadata period: nothing for a tarball, a package or a document.
			m = w.front.Mark()
			s = npmRun(t, r1, npmAll, full, "@acme/sdk", "plain-lib")
			g = nugetRun(t, r1, ngAll, full, "Acme.Sdk", "Other.Lib")
			if !s.ok() || !g.ok() {
				t.Fatalf("warm runs: %+v %+v", s, g)
			}
			if len(b.reps) == 1 {
				if got := w.front.Forwards(m, "/api/"); len(got) != 0 {
					t.Fatalf("a warm run sent %d requests for artifacts or documents to the main installation: %+v", len(got), got)
				}
			} else if n := len(w.front.Artifacts(m)); n != 0 {
				t.Fatalf("a warm run on the second replica transferred %d artifacts", n)
			}

			// After metadata_revalidate_after: only conditional requests, none of which transfers an artifact.
			time.Sleep(3200 * time.Millisecond)
			m = w.front.Mark()
			s = npmRun(t, r0, npmAll, full, "@acme/sdk", "plain-lib")
			g = nugetRun(t, r0, ngAll, full, "Acme.Sdk", "Other.Lib")
			if !s.ok() || !g.ok() {
				t.Fatalf("revalidating runs: %+v %+v", s, g)
			}
			fw := w.front.Forwards(m, "/api/")
			if len(fw) == 0 {
				t.Fatal("no document was revalidated after the metadata period")
			}
			for _, f := range fw {
				if f.Header.Get("If-None-Match") == "" || isArtifactPath(f.Path) && f.Status == http.StatusOK {
					t.Fatalf("a request after the period was not a conditional one that spares the artifact: %s %s -> %d", f.Method, f.Path, f.Status)
				}
			}
			if n := len(w.front.Artifacts(m)); n != 0 {
				t.Fatalf("revalidation transferred %d artifacts", n)
			}
		})
	}
}
