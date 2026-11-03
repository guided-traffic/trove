//go:build integration

package bastion

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/integration/testenv"
)

// atBastionHost reads from the main installation directly with the forwarded headers a Bastion sends
// (R-BAS-017): the answer is what the Bastion should serve its caller, byte for byte.
func (w *world) atBastionHost(path, token string, hdr ...string) testenv.Answer {
	w.t.Helper()
	h := append([]string{"X-Forwarded-Host", baseHost, "X-Forwarded-Proto", "https", "X-Forwarded-Port", "443", "X-Forwarded-For", "203.0.113.99"}, hdr...)
	return w.api("GET", path, token, nil, h...)
}

// agree requires that the Bastion answers a read as the main installation answers the caller's own request:
// the same status and, for a 200, the same bytes (a document is the one the main installation renders for the
// Bastion's host).
func agree(t *testing.T, w *world, r *replica, path string, c callOpts) reply {
	t.Helper()
	got := r.do(t, "GET", path, c)
	want := w.atBastionHost(path, c.token)
	if got.Status != want.Status {
		t.Fatalf("%s as %q: the Bastion answers %d, the main installation %d\nbastion: %.300s\nmain: %.300s", path, c.token[:min(12, len(c.token))], got.Status, want.Status, got.Body, want.Body)
	}
	if strings.HasSuffix(path, ".tgz") || strings.HasSuffix(path, ".nupkg") {
		if want.Status == http.StatusOK && !bytes.Equal(got.Body, want.Body) {
			t.Fatalf("%s: the artifact differs from the main installation's", path)
		}
		return got
	}
	// A refusal may be the Bastion's own problem detail (R-BAS-029); only what the main installation served is compared.
	if want.Status == http.StatusOK && !bytes.Equal(got.Body, want.Body) {
		t.Fatalf("%s: the body differs from the main installation's\nbastion: %.400s\nmain: %.400s", path, got.Body, want.Body)
	}
	return got
}

// TestR_BAS_021_R_BAS_023_R_BAS_024_EveryCallerIsAnsweredAsTheMainInstallationAnswers: $FULL, $PART, $NONE and
// the anonymous caller, with the cache empty and warm, on npm and NuGet: the Bastion's status and bytes are the
// main installation's, so a package $PART may not read is as missing as it is at the main installation.
func TestR_BAS_021_R_BAS_023_R_BAS_024_EveryCallerIsAnsweredAsTheMainInstallationAnswers(t *testing.T) {
	w := newWorld(t, worldOptions{})
	w.content()
	w.anonymousPolicy()

	paths := []string{
		"/api/npm/" + npmAll + "/" + escapeNPM("@acme/sdk"),
		tarballPath(npmAll, "@acme/sdk", "1.0.0"),
		"/api/npm/" + npmAll + "/plain-lib",
		tarballPath(npmAll, "plain-lib", "1.0.0"),
		"/api/npm/" + npmAll + "/no-such-package",
		"/api/nuget/" + ngAll + "/v3/flatcontainer/acme.sdk/index.json",
		nupkgPath(ngAll, "Acme.Sdk", "1.0.0"),
		"/api/nuget/" + ngAll + "/v3/flatcontainer/other.lib/index.json",
		nupkgPath(ngAll, "Other.Lib", "1.0.0"),
	}
	callers := func(anon bool) []callOpts {
		out := []callOpts{{token: w.full.token, addr: "203.0.113.11"}, {token: w.part.token, addr: "203.0.113.12"}, {token: w.none.token, addr: "203.0.113.13"}}
		if anon {
			out = append(out, callOpts{addr: "203.0.113.14"})
		}
		return out
	}
	for _, anon := range []bool{true, false} {
		w.setAnonymous(anon)
		name := "anonymous off"
		if anon {
			name = "anonymous on"
		}
		for _, v := range []variant{variants()[0], variants()[2]} {
			t.Run(name+"/"+v.name, func(t *testing.T) {
				b := w.newBastion(t, v.o).wait()
				r := b.reps[0]
				for range 2 { // cold, then warm
					for _, c := range callers(anon) {
						for _, p := range paths {
							agree(t, w, r, p, c)
						}
					}
				}
				if !anon {
					// The Bastion refuses a request without a credential itself, without a request to main.
					m := w.front.Mark()
					got := r.do(t, "GET", paths[0], callOpts{addr: "203.0.113.15"})
					if got.Status != http.StatusUnauthorized || len(w.front.Forwards(m, "/api/")) != 0 {
						t.Fatalf("anonymous with the switch off: %d, %d forwards", got.Status, len(w.front.Forwards(m, "/api/")))
					}
				}
			})
		}
	}
}

// TestR_BAS_022_R_BAS_024_NegativeCacheOnlyForFullReaders: a name that does not exist for anyone is answered 404
// from the negative cache after the first request, for a full reader; the same 404 for a partial reader (who
// may not read a name that exists) is never stored, so it never stands for a hidden package.
func TestR_BAS_022_R_BAS_024_NegativeCacheOnlyForFullReaders(t *testing.T) {
	w := newWorld(t, worldOptions{})
	w.content()
	b := w.newBastion(t, bastionOptions{}).wait()
	r := b.reps[0]
	full, part := callOpts{token: w.full.token, addr: "203.0.113.21"}, callOpts{token: w.part.token, addr: "203.0.113.22"}

	// A name no member holds: absent.
	missing := "/api/npm/" + npmLocal + "/no-such-package"
	for i := 0; i < 2; i++ {
		m := w.front.Mark()
		if got := r.do(t, "GET", missing, full); got.Status != http.StatusNotFound {
			t.Fatalf("missing name: %d", got.Status)
		}
		n := len(w.front.Forwards(m, "/api/"))
		if i == 0 && n != 1 || i == 1 && n != 0 {
			t.Fatalf("request %d for a missing name sent %d requests to the main installation", i+1, n)
		}
	}
	// The same missing name for $PART is not remembered by the full reader's entry: $PART is not a full reader and
	// gets the main installation's answer each time (it may not read the repository's whole content).
	for i := 0; i < 2; i++ {
		m := w.front.Mark()
		got := r.do(t, "GET", missing, part)
		if got.Status != http.StatusNotFound {
			t.Fatalf("$PART, missing name: %d", got.Status)
		}
		if n := len(w.front.Forwards(m, "/api/")); n != 1 {
			t.Fatalf("$PART request %d for a missing name sent %d requests, want 1 (no negative entry for a partial reader)", i+1, n)
		}
	}
	// A package $PART may not read exists: the 404 it gets is the same body as for a missing name and is never
	// stored, so the full reader still reads the package.
	hidden := "/api/npm/" + npmLocal + "/plain-lib"
	hp := r.do(t, "GET", hidden, part)
	if hp.Status != http.StatusNotFound {
		t.Fatalf("hidden package for $PART: %d", hp.Status)
	}
	if got := r.do(t, "GET", hidden, full); got.Status != http.StatusOK {
		t.Fatalf("a refusal of $PART became a negative entry: $FULL got %d", got.Status)
	}
	if got := r.do(t, "GET", hidden, part); got.Status != http.StatusNotFound {
		t.Fatalf("hidden package for $PART after $FULL read it: %d", got.Status)
	}
	// An expired negative entry asks again.
	b2 := w.newBastion(t, bastionOptions{Cache: "    negative_period: 1s\n"}).wait()
	missing2 := "/api/npm/" + npmLocal + "/another-missing"
	b2.reps[0].do(t, "GET", missing2, full)
	time.Sleep(1300 * time.Millisecond)
	m := w.front.Mark()
	b2.reps[0].do(t, "GET", missing2, full)
	if n := len(w.front.Forwards(m, "/api/")); n != 1 {
		t.Fatalf("after negative_period the request sent %d requests, want 1", n)
	}
}
