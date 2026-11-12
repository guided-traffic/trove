//go:build integration

package bastion

import (
	"net/http"
	"sync"
	"testing"
	"time"
)

// bound is the revocation bound of R-BAS-026: a revocation at the main installation is refused at every replica
// within five seconds.
const bound = 5 * time.Second

// refused waits until every replica of the Bastion no longer serves the path to the caller and returns the
// slowest replica's time from now; it fails when a replica still serves it after the bound.
func refused(t *testing.T, b *bastion, path string, c callOpts, want ...int) time.Duration {
	t.Helper()
	return refusedSince(t, time.Now(), b, path, c, want...)
}

// refusedSince is refused with the bound counted from since (the moment the revocation was made).
func refusedSince(t *testing.T, since time.Time, b *bastion, path string, c callOpts, want ...int) time.Duration {
	t.Helper()
	start := since
	var worst time.Duration
	for _, r := range b.reps {
		for {
			got := r.do(t, "GET", path, c)
			if got.Status != http.StatusOK && got.Status != http.StatusNotModified {
				ok := len(want) == 0
				for _, s := range want {
					ok = ok || got.Status == s
				}
				if !ok {
					t.Fatalf("%s: refused with %d (%s), want one of %v", r.name, got.Status, got.Body, want)
				}
				break
			}
			if time.Since(start) > bound {
				t.Fatalf("%s still serves %s %v after the revocation (bound %v)", r.name, path, time.Since(start).Round(time.Millisecond), bound)
			}
			time.Sleep(50 * time.Millisecond)
		}
		if d := time.Since(start); d > worst {
			worst = d
		}
	}
	return worst
}

// warm reads the tarball and the document through every replica so that each holds the answers.
func warm(t *testing.T, b *bastion, c callOpts, paths ...string) {
	t.Helper()
	for _, r := range b.reps {
		for _, p := range paths {
			if got := r.do(t, "GET", p, c); got.Status != http.StatusOK {
				t.Fatalf("%s warm %s: %d %s", r.name, p, got.Status, got.Body)
			}
			if got := r.do(t, "GET", p, c); got.Status != http.StatusOK {
				t.Fatalf("%s warm (hit) %s: %d", r.name, p, got.Status)
			}
		}
	}
}

// TestR_BAS_015_R_BAS_026_RevocationByEveryFeedKind: a revocation of every kind of the feed (token, principal,
// grants, settings, content) made at the main installation through its management API is refused at both
// replicas of a Bastion within the five-second bound, although the caller's answers and the bytes are cached.
func TestR_BAS_015_R_BAS_026_RevocationByEveryFeedKind(t *testing.T) {
	w := newWorld(t, worldOptions{})
	w.content()
	w.anonymousPolicy()
	w.setAnonymous(true)
	w.publishNPM("@acme/gone", "1.0.0")
	b := w.newBastion(t, bastionOptions{Replicas: 2, Backend: "volume"}).wait()
	tgz := tarballPath(npmAll, "@acme/sdk", "1.0.0")
	doc := "/api/npm/" + npmAll + "/" + escapeNPM("@acme/sdk")

	t.Run("token", func(t *testing.T) {
		u := w.newReader(npmAll, npmLocal, npmRemote)
		c := callOpts{token: u.token, addr: "198.51.100.31"}
		warm(t, b, c, tgz, doc)
		w.mustAPI("DELETE", "/api/v1/tokens/"+u.id, 204, nil)
		t.Logf("refused after %v", refused(t, b, tgz, c, http.StatusUnauthorized))
	})
	t.Run("principal", func(t *testing.T) {
		u := w.newReader(npmAll, npmLocal, npmRemote)
		c := callOpts{token: u.token, addr: "198.51.100.32"}
		warm(t, b, c, tgz, doc)
		w.mustAPI("POST", "/api/v1/service-accounts/"+u.name+"/disable", 204, nil)
		t.Logf("refused after %v", refused(t, b, tgz, c, http.StatusUnauthorized))
	})
	t.Run("grants", func(t *testing.T) {
		// A reader whose grant is deleted still reads what is public, so the revocation of a grant is shown on private content.
		w.setAnonymous(false)
		u := w.newReader(npmAll, npmLocal, npmRemote)
		c := callOpts{token: u.token, addr: "198.51.100.33"}
		warm(t, b, c, tgz, doc)
		w.mustAPI("DELETE", "/api/v1/permission-policies/p-"+u.name, 204, nil)
		t.Logf("refused after %v", refused(t, b, tgz, c, http.StatusNotFound, http.StatusForbidden))
		refused(t, b, doc, c, http.StatusNotFound, http.StatusForbidden)
	})
	t.Run("settings", func(t *testing.T) {
		w.setAnonymous(true)
		c := callOpts{addr: "198.51.100.34"}
		warm(t, b, c, tgz, doc)
		made := time.Now()
		w.setAnonymous(false)
		t.Logf("refused after %v", refusedSince(t, made, b, tgz, c, http.StatusUnauthorized))
		w.setAnonymous(true)
	})
	t.Run("content", func(t *testing.T) {
		u := w.newReader(npmAll, npmLocal, npmRemote)
		c := callOpts{token: u.token, addr: "198.51.100.35"}
		gone, goneDoc := tarballPath(npmAll, "@acme/gone", "1.0.0"), "/api/npm/"+npmAll+"/"+escapeNPM("@acme/gone")
		warm(t, b, c, gone, goneDoc)
		rev, _ := w.api("GET", "/api/npm/"+npmLocal+"/"+escapeNPM("@acme/gone"), w.admin, nil).JSON(t)["_rev"].(string)
		w.mustAPI("DELETE", "/api/npm/"+npmLocal+"/"+escapeNPM("@acme/gone")+"/-rev/"+rev, 200, nil)
		t.Logf("refused after %v", refused(t, b, gone, c, http.StatusNotFound))
		refused(t, b, goneDoc, c, http.StatusNotFound)
	})
}

// TestR_BAS_020_R_BAS_026_AnAnswerObtainedBeforeARevocationAndStoredAfterItIsNotUsed: the main installation
// decides for a caller, the caller's token is revoked while that answer is on its way to the Bastion, and the
// feed entry arrives before the answer: the answer is stored after the revocation and must not be used (the
// answer's own time is the time the request was sent, R-BAS-020).
func TestR_BAS_020_R_BAS_026_AnAnswerObtainedBeforeARevocationAndStoredAfterItIsNotUsed(t *testing.T) {
	w := newWorld(t, worldOptions{})
	w.content()
	b := w.newBastion(t, bastionOptions{}).wait()
	r := b.reps[0]
	u := w.newReader(npmAll, npmLocal, npmRemote)
	c := callOpts{token: u.token, addr: "198.51.100.41"}
	path := tarballPath(npmAll, "@acme/sdk", "1.0.0")

	arrived, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	w.front.SetHold(func(req *http.Request, _ *http.Response) {
		if req.URL.Path == path && req.Header.Get("Authorization") != "" {
			once.Do(func() { close(arrived) })
			<-release
		}
	})
	done := make(chan reply, 1)
	go func() { done <- r.do(t, "GET", path, c) }()
	select {
	case <-arrived:
	case <-time.After(20 * time.Second):
		t.Fatal("the request never reached the main installation")
	}
	// The main installation has decided "allowed". Now the token is revoked, and the Bastion learns it from the feed.
	w.mustAPI("DELETE", "/api/v1/tokens/"+u.id, 204, nil)
	before := sumMetric(r.metrics(t), "trove_bastion_feed_entries_applied_total")
	deadline := time.Now().Add(bound)
	for sumMetric(r.metrics(t), "trove_bastion_feed_entries_applied_total") <= before {
		if time.Now().After(deadline) {
			t.Fatal("the feed entry of the revocation did not arrive")
		}
		time.Sleep(50 * time.Millisecond)
	}
	w.front.SetHold(nil)
	close(release)
	<-done // the request in flight was decided before the revocation; what it gets is not the question

	// The answer and the artifact of that request are stored now, after the revocation entry: the next request
	// of the revoked token is refused all the same, and the main installation is not asked about the cache hit.
	got := r.do(t, "GET", path, c)
	if got.Status != http.StatusUnauthorized {
		t.Fatalf("after the revocation the token gets %d, want 401", got.Status)
	}
}
