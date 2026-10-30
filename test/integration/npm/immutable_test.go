//go:build integration

package npm

import (
	"testing"
	"time"
)

// TestR_NPM_008_AVersionIsNeverAcceptedAgainWithOtherBytes: a second publish with
// other bytes, by another principal, or of an unpublished version answers 409 with
// the code of its reason, and changes nothing.
func TestR_NPM_008_AVersionIsNeverAcceptedAgainWithOtherBytes(t *testing.T) {
	e := newEnv(t)
	a := e.replica("web-a")
	e.mustPublish(a, "npm-local", e.tokA, pkg{Name: "@acme/ui", Version: "1.2.0"})
	rev := e.revSeq("npm-local", "@acme/ui")

	t.Run("R-NPM-008 other bytes", func(t *testing.T) {
		ans := e.publish(a, "npm-local", e.tokA, pkg{Name: "@acme/ui", Version: "1.2.0", Files: map[string]string{"extra.js": "x"}})
		if ans.Status != 409 || code(t, ans) != "npm_version_exists" {
			t.Fatalf("%d %s", ans.Status, ans.Body)
		}
	})
	t.Run("R-NPM-008 another principal with the same bytes", func(t *testing.T) {
		ans := e.publish(a, "npm-local", e.tokB, pkg{Name: "@acme/ui", Version: "1.2.0"})
		// bob is not an owner of @acme/ui (alice claimed it): the ownership refusal comes
		// first or the version conflict does; either way nothing is stored.
		if ans.Status != 409 && ans.Status != 403 {
			t.Fatalf("%d %s", ans.Status, ans.Body)
		}
	})
	t.Run("R-NPM-008 the same bytes under another tag", func(t *testing.T) {
		ans := e.publish(a, "npm-local", e.tokA, pkg{Name: "@acme/ui", Version: "1.2.0", Tag: "next"})
		if ans.Status != 409 || code(t, ans) != "npm_version_exists" {
			t.Fatalf("%d %s", ans.Status, ans.Body)
		}
	})
	t.Run("R-NPM-008 an unpublished version stays taken, also for the same bytes", func(t *testing.T) {
		// Unpublish is built with the documents; the tombstone is what it writes.
		e.st.Exec(`INSERT INTO npm_tombstones (repo_id, name, version, integrity, source, unpublished_at, unpublished_by)
			SELECT r.id, '@acme/ui', '1.3.0', 'sha512-x', 'unpublish', now(), $1::uuid FROM repositories r WHERE r.key = 'npm-local'`, e.alice.ID)
		ans := e.publish(a, "npm-local", e.tokA, pkg{Name: "@acme/ui", Version: "1.3.0"})
		if ans.Status != 409 || code(t, ans) != "npm_version_unpublished" {
			t.Fatalf("%d %s", ans.Status, ans.Body)
		}
		e.mustPublish(a, "npm-local", e.tokA, pkg{Name: "@acme/ui", Version: "1.3.1"})
	})
	if got := e.versions("npm-local", "@acme/ui"); got != 2 {
		t.Fatalf("versions %d, want the two that were accepted", got)
	}
	if got := e.revSeq("npm-local", "@acme/ui"); got != rev+1 {
		t.Fatalf("rev_seq %d, want %d: a refused publish raised it", got, rev+1)
	}
}

// TestR_NPM_007_AcknowledgedRetry: a publish repeated after its answer was lost is
// answered 201 with an npm-notice on any replica, changes nothing and is audited
// (Q-NPM-11); each of the five conditions is needed.
func TestR_NPM_007_AcknowledgedRetry(t *testing.T) {
	e := newEnv(t)
	a := e.replica("web-a")
	b := e.replica("web-b", "TROVE_NPM_PUBLISH_RETRY_WINDOW=2s")
	p := pkg{Name: "@acme/ui", Version: "1.0.3", Sigstore: []byte(`{"bundle":1}`)}
	first := e.mustPublish(a, "npm-local", e.tokA, p)
	if first.Header.Get("npm-notice") != "" {
		t.Fatalf("the first publish carries a notice: %q", first.Header.Get("npm-notice"))
	}
	rev, versions := e.revSeq("npm-local", "@acme/ui"), e.versions("npm-local", "@acme/ui")
	items := e.st.Number(`SELECT count(*) FROM items WHERE kind = 'file'`)
	objects := e.st.Number(`SELECT count(*) FROM blobs`)

	t.Run("R-NPM-007 the retry reaches another replica and is acknowledged", func(t *testing.T) {
		ans := e.mustPublish(b, "npm-local", e.tokA, p)
		notice := ans.Header.Get("npm-notice")
		if !contains(notice, "@acme/ui@1.0.3 was already published by you at ") || !contains(notice, "; nothing changed") {
			t.Fatalf("npm-notice %q", notice)
		}
		if e.revSeq("npm-local", "@acme/ui") != rev || e.versions("npm-local", "@acme/ui") != versions ||
			e.st.Number(`SELECT count(*) FROM items WHERE kind = 'file'`) != items || e.st.Number(`SELECT count(*) FROM blobs`) != objects {
			t.Fatal("the acknowledgement changed something")
		}
		if e.events("npm.publish", "@acme/ui", "") != 2 || e.events("npm.publish", "@acme/ui", "retry_acknowledged") != 1 {
			t.Fatalf("events: %d, retry_acknowledged %d", e.events("npm.publish", "@acme/ui", ""), e.events("npm.publish", "@acme/ui", "retry_acknowledged"))
		}
		// the repo label is capped by the metrics section (R-OPS-007); which keys get a series
		// of their own is not this format's decision, so the test sums over the label
		if got := sumSeries(b.Metrics(t), "trove_npm_publish_retries_acknowledged_total{"); got != 1 {
			t.Fatalf("retries acknowledged = %v", got)
		}
	})
	t.Run("R-NPM-007 another tarball, another tag, no bundle and another bundle are 409", func(t *testing.T) {
		for name, q := range map[string]pkg{
			"another tarball": {Name: "@acme/ui", Version: "1.0.3", Sigstore: p.Sigstore, Files: map[string]string{"x.js": "1"}},
			"another tag":     {Name: "@acme/ui", Version: "1.0.3", Sigstore: p.Sigstore, Tag: "next"},
			"no bundle":       {Name: "@acme/ui", Version: "1.0.3"},
			"another bundle":  {Name: "@acme/ui", Version: "1.0.3", Sigstore: []byte(`{"bundle":2}`)},
		} {
			if ans := e.publish(b, "npm-local", e.tokA, q); ans.Status != 409 || code(t, ans) != "npm_version_exists" {
				t.Errorf("%s: %d %s", name, ans.Status, ans.Body)
			}
		}
	})
	t.Run("R-NPM-007 a bundle the version has not is 409", func(t *testing.T) {
		e.mustPublish(a, "npm-local", e.tokA, pkg{Name: "@acme/ui", Version: "1.0.4"})
		ans := e.publish(b, "npm-local", e.tokA, pkg{Name: "@acme/ui", Version: "1.0.4", Sigstore: []byte(`{"bundle":1}`)})
		if ans.Status != 409 || code(t, ans) != "npm_version_exists" {
			t.Fatalf("%d %s", ans.Status, ans.Body)
		}
	})
	t.Run("R-NPM-007 another principal is not the retry", func(t *testing.T) {
		ans := e.publish(b, "npm-local", e.tokB, p)
		if ans.Status != 409 && ans.Status != 403 {
			t.Fatalf("%d %s", ans.Status, ans.Body)
		}
	})
	t.Run("R-NPM-007 after the retry window it is 409", func(t *testing.T) {
		time.Sleep(2200 * time.Millisecond) // the window of web-b is 2 s, on PostgreSQL's clock
		ans := e.publish(b, "npm-local", e.tokA, p)
		if ans.Status != 409 || code(t, ans) != "npm_version_exists" {
			t.Fatalf("%d %s", ans.Status, ans.Body)
		}
		// the replica with the default window decides on the same facts and still acknowledges
		if got := e.publish(a, "npm-local", e.tokA, p); got.Status != 201 {
			t.Fatalf("web-a: %d %s", got.Status, got.Body)
		}
	})
	t.Run("R-NPM-007 a window of zero turns the acknowledgement off", func(t *testing.T) {
		c := e.replica("web-c", "TROVE_NPM_PUBLISH_RETRY_WINDOW=0s")
		if ans := e.publish(c, "npm-local", e.tokA, p); ans.Status != 409 {
			t.Fatalf("%d %s", ans.Status, ans.Body)
		}
	})
}
