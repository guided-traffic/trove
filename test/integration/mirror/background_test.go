//go:build integration

package mirror

import (
	"context"
	"errors"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formathost"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/mirror"
	"github.com/guided-traffic/trove/internal/mirror/discover"
	"github.com/guided-traffic/trove/test/integration/testenv"
	"github.com/guided-traffic/trove/test/npmstub"
)

// The background fetches of a remote repository outside a pass (Q-REPO-272, R-REPO-054, R-REPO-055): a pull-through remote that no
// mirror pass runs for, a format host of a client-serving replica, and the counting stub as the upstream. The upstream is what a job
// of a format (the Debian parse job, the virtual build) is lent by Host.BackgroundUpstream.

const backgroundSecret = "background-secret"

func (r *rig) backgroundUpstream(t *testing.T, rep *testenv.Replica, jobID string) discover.Upstream {
	t.Helper()
	host := rep.App.FormatHost()
	if host == nil {
		t.Fatal("the replica has no format host")
	}
	u, err := host.BackgroundUpstream(formathost.WithJobID(context.Background(), jobID), formats.RepoRef{ID: r.repoN})
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestR_REPO_055_ABackgroundUpstreamFetchesAndStoresWithoutAPass(t *testing.T) {
	r := newRig(t, rigOptions{
		mode: "pull-through", token: backgroundSecret, mirror: settings{"concurrency": 2},
		remote: map[string]any{"limits": map[string]any{"maxArtifactBytes": nil, "maxMetadataBytes": nil, "maxConcurrentFetches": 8}},
	})
	const pkg = "bg-pad"
	r.publish(pkg, "1.0.0", "1.1.0")
	rep := r.web("web")
	u := r.backgroundUpstream(t, rep, "job-background-1")
	ctx := context.Background()

	t.Run("R-REPO-055 a document is fetched through the engine with the credential and stored: the second read costs no request", func(t *testing.T) {
		mark := r.traf.mark()
		doc, found, err := u.Document(ctx, pkg, "full")
		if err != nil || !found || len(doc) == 0 {
			t.Fatalf("found %v, %d bytes, %v", found, len(doc), err)
		}
		hits := r.traf.since(mark)
		if len(hits) != 1 || hits[0].auth != "bearer" {
			t.Fatalf("the document cost %+v, want one request with the bearer credential", hits)
		}
		mark = r.traf.mark()
		if _, found, err := u.Document(ctx, pkg, "full"); err != nil || !found {
			t.Fatalf("second read: found %v, %v", found, err)
		}
		if hits := r.traf.since(mark); len(hits) != 0 {
			t.Fatalf("the stored document cost %+v", hits)
		}
		if _, found, err := u.Document(ctx, "no-such-package", "full"); err != nil || found {
			t.Fatalf("a package the upstream does not have: found %v, %v", found, err)
		}
	})

	t.Run("R-REPO-055 an artifact is filled through the format's gate, stored, and read back; a stored one costs no request", func(t *testing.T) {
		path := tarballPath(pkg, "1.0.0")
		mark := r.traf.mark()
		body, found, err := u.Artifact(ctx, path, 1<<20)
		if err != nil || !found || len(body) == 0 {
			t.Fatalf("found %v, %d bytes, %v", found, len(body), err)
		}
		if !r.storedItem(path) {
			t.Fatal("the artifact is not stored")
		}
		if hits := tarballHits(r.traf.since(mark)); len(hits) != 1 || hits[0].auth != "bearer" {
			t.Fatalf("the artifact cost %+v, want one request with the bearer credential", hits)
		}
		mark = r.traf.mark()
		if again, found, err := u.Artifact(ctx, path, 1<<20); err != nil || !found || string(again) != string(body) {
			t.Fatalf("second read: found %v, %v", found, err)
		}
		if hits := r.traf.since(mark); len(hits) != 0 {
			t.Fatalf("the stored artifact cost %+v", hits)
		}
		// npm's gate refuses a version its document does not list: the refusal comes back as the one a pass's plan would see.
		var refusal *mirror.RefusedError
		if _, found, err := u.Artifact(ctx, tarballPath(pkg, "9.9.9"), 1<<20); found || !errors.As(err, &refusal) {
			t.Fatalf("a version the document does not list: found %v, %v", found, err)
		}
		if body, found, err := u.Artifact(ctx, tarballPath(pkg, "1.0.0"), 1); err != nil || !found || body != nil {
			t.Fatalf("an artifact above max is found with no body: found %v, %d bytes, %v", found, len(body), err)
		}
	})

	t.Run("R-REPO-043 the egress rules apply: a host the repository was not given is refused and nothing is sent", func(t *testing.T) {
		foreign, _ := url.Parse("https://elsewhere.example.org/bg-pad")
		mark := r.traf.mark()
		_, err := u.Get(ctx, foreign, "", 0)
		if ee, ok := errs.As(err); !ok || ee.Code != errs.CodeForeignHostDenied {
			t.Fatalf("%v", err)
		}
		own, _ := url.Parse(r.url + pkg)
		res, err := u.Get(ctx, own, "application/json", 1<<20)
		if err != nil || res.Status != 200 {
			t.Fatalf("a request to the repository's own host: %+v %v", res, err)
		}
		hits := r.traf.since(mark)
		if len(hits) != 1 || hits[0].auth != "bearer" {
			t.Fatalf("%+v", hits)
		}
	})

	t.Run("R-REPO-055 no mirror pass, fence or state is involved", func(t *testing.T) {
		if n := r.number(`SELECT count(*) FROM job WHERE kind LIKE 'mirror%' AND state <> 'cancelled'`); n != 0 {
			t.Fatalf("%d mirror jobs", n)
		}
		if n := r.number(`SELECT count(*) FROM mirror_state WHERE pass_job_id IS NOT NULL OR pass_started_at IS NOT NULL OR last_pass IS NOT NULL OR rate_limited_until IS NOT NULL`); n != 0 {
			t.Fatalf("the mirror state changed: %d rows", n)
		}
	})

	t.Run("R-REPO-055 the background share bounds the requests in flight: at most min(concurrency, budget / 4) = 2", func(t *testing.T) {
		const n = 8
		for i := range n {
			r.publish("share-"+string(rune('a'+i)), "1.0.0")
		}
		r.setStub(npmstub.Mode{DelayMS: map[string]int{"tarball": 200}})
		defer r.setStub(npmstub.Mode{})
		r.traf.resetPeak()
		var wg sync.WaitGroup
		errc := make(chan error, n)
		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, found, err := u.Artifact(ctx, tarballPath("share-"+string(rune('a'+i)), "1.0.0"), 1<<20)
				if err == nil && !found {
					err = context.DeadlineExceeded
				}
				errc <- err
			}()
		}
		wg.Wait()
		close(errc)
		for err := range errc {
			if err != nil {
				t.Fatal(err)
			}
		}
		if peak := r.traf.peak(); peak < 1 || peak > 2 {
			t.Fatalf("%d tarball requests were in flight at once, want between 1 and 2", peak)
		}
	})

	t.Run("R-REPO-054 a rate-limit answer pauses the run for the time it names, then the request is repeated and stored", func(t *testing.T) {
		r.publish("limited", "1.0.0")
		r.setStub(npmstub.Mode{Throttle: &npmstub.Throttle{Kind: npmstub.Throttle429, Class: npmstub.KindTarball, From: 1, Count: 1, RetryAfterSeconds: 3}})
		defer r.setStub(npmstub.Mode{})
		before := len(tarballHits(r.traf.since(0)))
		start := time.Now()
		body, found, err := u.Artifact(ctx, tarballPath("limited", "1.0.0"), 1<<20)
		if err != nil || !found || len(body) == 0 {
			t.Fatalf("found %v, %d bytes, %v", found, len(body), err)
		}
		within(t, "the call", time.Since(start), 2800*time.Millisecond, 8*time.Second)
		hits := tarballHits(r.traf.since(0))[before:]
		if len(hits) != 2 {
			t.Fatalf("%d tarball requests, want the throttled one and its repetition", len(hits))
		}
		within(t, "the pause between them", hits[1].at.Sub(hits[0].at), 2800*time.Millisecond, 8*time.Second)
		if n := r.stub.Throttled(); n != 1 {
			t.Fatalf("the stub throttled %d requests", n)
		}
		if !r.storedItem(tarballPath("limited", "1.0.0")) {
			t.Fatal("not stored")
		}
	})

	t.Run("R-REPO-054 a wait longer than 300 s ends the run with a deferred error and later calls send nothing", func(t *testing.T) {
		r.publish("deferred", "1.0.0")
		r.setStub(npmstub.Mode{Throttle: &npmstub.Throttle{Kind: npmstub.Throttle429, Class: npmstub.KindTarball, From: 1, Count: 1, RetryAfterSeconds: 3600}})
		defer r.setStub(npmstub.Mode{})
		fresh := r.backgroundUpstream(t, rep, "job-background-2")
		_, _, err := fresh.Artifact(ctx, tarballPath("deferred", "1.0.0"), 1<<20)
		if _, ok := discover.AsRateLimited(err); !ok {
			t.Fatalf("%v", err)
		}
		mark := r.traf.mark()
		if _, _, err := fresh.Document(ctx, pkg, "full"); err == nil {
			t.Fatal("a call after the long wait was answered")
		}
		if hits := r.traf.since(mark); len(hits) != 0 {
			t.Fatalf("a call after the long wait sent %+v", hits)
		}
	})
}
