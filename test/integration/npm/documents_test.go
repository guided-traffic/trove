//go:build integration

package npm

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/integration/testenv"
)

const abbreviatedAccept = "application/vnd.npm.install-v1+json; q=1.0, application/json; q=0.8, */*"

// document decodes the body of a package document.
func document(t testing.TB, a testenv.Answer) map[string]any {
	t.Helper()
	if a.Status != 200 {
		t.Fatalf("document: %d %s", a.Status, a.Body)
	}
	var m map[string]any
	if err := json.Unmarshal(a.Body, &m); err != nil {
		t.Fatalf("document: %v\n%s", err, a.Body)
	}
	return m
}

func versionNames(doc map[string]any) []string {
	var out []string
	vs, _ := doc["versions"].(map[string]any)
	for v := range vs {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

func tagsOfDoc(doc map[string]any) map[string]string {
	out := map[string]string{}
	tags, _ := doc["dist-tags"].(map[string]any)
	for k, v := range tags {
		out[k], _ = v.(string)
	}
	return out
}

// TestR_NPM_010_PublishThenViewOnAnotherReplica: every publish is visible on another
// replica as soon as it has returned: three replicas, a different one for every request,
// no pause, and Valkey running as well as unreachable. Nothing is sent between replicas;
// each reads packages.rev_seq from PostgreSQL (R-ARCH-012, Q-NPM-10).
func TestR_NPM_010_PublishThenViewOnAnotherReplica(t *testing.T) {
	for _, valkey := range []bool{true, false} {
		t.Run(fmt.Sprintf("valkey reachable=%v", valkey), func(t *testing.T) {
			e := newEnvOpts(t, valkey)
			reps := []*testenv.Replica{e.replica("web-a"), e.replica("web-b"), e.replica("web-c")}
			const runs = 100
			for i := range runs {
				version := fmt.Sprintf("1.0.%d", i)
				pub, view := reps[i%3], reps[(i+1)%3]
				e.mustPublish(pub, "npm-local", e.tokA, pkg{Name: "@acme/lib", Version: version})
				accept := ""
				if i%2 == 1 {
					accept = abbreviatedAccept // `npm view` sends this; both forms must see the commit
				}
				doc := document(t, e.get(view, "npm-local", "@acme%2flib", e.tokA, "Accept", accept))
				if !slices.Contains(versionNames(doc), version) || tagsOfDoc(doc)["latest"] != version || len(versionNames(doc)) != i+1 {
					t.Fatalf("run %d: published %s on %s, the document on %s has %v and latest %q", i, version, pub.Name, view.Name, versionNames(doc), tagsOfDoc(doc)["latest"])
				}
				// and the single-version document and the tag list of a third replica
				third := reps[(i+2)%3]
				if a := e.get(third, "npm-local", "@acme%2flib/"+version, e.tokA); a.Status != 200 {
					t.Fatalf("run %d: single-version document on %s: %d %s", i, third.Name, a.Status, a.Body)
				}
				if a := e.get(third, "npm-local", "-/package/@acme%2flib/dist-tags", e.tokA); a.Status != 200 || !strings.Contains(string(a.Body), `"latest":"`+version+`"`) {
					t.Fatalf("run %d: dist-tags on %s: %d %s", i, third.Name, a.Status, a.Body)
				}
			}
		})
	}
}

// TestR_ARCH_012_ReadYourWritesAcrossReplicas: the 50 iterations of the acceptance
// criterion over three replicas, without a pause, for every kind of change: a publish, a
// tag change and an unpublish are each in force on the next replica at once.
func TestR_ARCH_012_ReadYourWritesAcrossReplicas(t *testing.T) {
	e := newEnv(t)
	reps := []*testenv.Replica{e.replica("web-a"), e.replica("web-b"), e.replica("web-c")}
	e.mustPublish(reps[0], "npm-local", e.tokA, pkg{Name: "lib", Version: "0.0.1"})
	for i := range 50 {
		w, r1, r2 := reps[i%3], reps[(i+1)%3], reps[(i+2)%3]
		version := fmt.Sprintf("1.0.%d", i)
		e.mustPublish(w, "npm-local", e.tokA, pkg{Name: "lib", Version: version})
		for _, r := range []*testenv.Replica{r1, r2} {
			if doc := document(t, e.get(r, "npm-local", "lib", e.tokA)); !slices.Contains(versionNames(doc), version) {
				t.Fatalf("iteration %d: publish on %s not visible on %s", i, w.Name, r.Name)
			}
		}
		tag := fmt.Sprintf("t%d", i)
		if a := e.send(r1, "PUT", "npm-local", "-/package/lib/dist-tags/"+tag, e.tokA, `"`+version+`"`); a.Status != 200 {
			t.Fatalf("iteration %d: tag: %d %s", i, a.Status, a.Body)
		}
		for _, r := range []*testenv.Replica{w, r2} {
			if tagsOfDoc(document(t, e.get(r, "npm-local", "lib", e.tokA)))[tag] != version {
				t.Fatalf("iteration %d: tag change on %s not visible on %s", i, r1.Name, r.Name)
			}
		}
		// the read-modify-write of npm unpublish may use a different replica for each request
		read := document(t, e.get(r2, "npm-local", "lib?write=true", e.tokA))
		rev, _ := read["_rev"].(string)
		delete(read["versions"].(map[string]any), "0.0.1")
		if i == 0 {
			a := e.send(w, "PUT", "npm-local", "lib/-rev/"+rev, e.tokA, read)
			if a.Status != 200 {
				t.Fatalf("iteration %d: unpublish: %d %s", i, a.Status, a.Body)
			}
			for _, r := range reps {
				if slices.Contains(versionNames(document(t, e.get(r, "npm-local", "lib", e.tokA))), "0.0.1") {
					t.Fatalf("iteration %d: the unpublish is not visible on %s", i, r.Name)
				}
			}
		}
	}
	if got := len(versionNames(document(t, e.get(reps[1], "npm-local", "lib", e.tokA)))); got != 50 {
		t.Fatalf("%d versions", got)
	}
}

// TestR_NPM_013_ValidatorsOnEveryReplica: the ETag of a document is the same on every
// replica, a conditional request is a 304 on any replica, a date never validates, and a
// change makes the old validator obsolete everywhere.
func TestR_NPM_013_ValidatorsOnEveryReplica(t *testing.T) {
	e := newEnv(t)
	a, b, c := e.replica("web-a"), e.replica("web-b"), e.replica("web-c")
	e.mustPublish(a, "npm-local", e.tokA, pkg{Name: "@acme/ui", Version: "1.0.0"})
	e.mustPublish(b, "npm-local", e.tokA, pkg{Name: "@acme/ui", Version: "1.1.0"})
	first := e.get(a, "npm-local", "@acme%2fui", e.tokA)
	etag := first.Header.Get("ETag")
	if !strings.HasPrefix(etag, `W/"`) || first.Header.Get("Last-Modified") == "" {
		t.Fatalf("ETag %q Last-Modified %q", etag, first.Header.Get("Last-Modified"))
	}
	t.Run("the same ETag and a 304 on every replica, for the full and the abbreviated form", func(t *testing.T) {
		for _, accept := range []string{"application/json", abbreviatedAccept} {
			want := e.get(a, "npm-local", "@acme%2fui", e.tokA, "Accept", accept).Header.Get("ETag")
			for _, r := range []*testenv.Replica{a, b, c} {
				ans := e.get(r, "npm-local", "@acme%2fui", e.tokA, "Accept", accept)
				if ans.Header.Get("ETag") != want || ans.Header.Get("Vary") != "Accept, Accept-Encoding" {
					t.Fatalf("%s: ETag %q (want %q) Vary %q", r.Name, ans.Header.Get("ETag"), want, ans.Header.Get("Vary"))
				}
				cond := e.get(r, "npm-local", "@acme%2fui", e.tokA, "Accept", accept, "If-None-Match", want)
				if cond.Status != 304 || len(cond.Body) != 0 || cond.Header.Get("ETag") != want {
					t.Fatalf("%s: conditional: %d %q", r.Name, cond.Status, cond.Body)
				}
			}
		}
	})
	t.Run("the validator of the full and of the abbreviated form differ", func(t *testing.T) {
		if e.get(a, "npm-local", "@acme%2fui", e.tokA, "Accept", abbreviatedAccept).Header.Get("ETag") == etag {
			t.Fatal("one validator for two forms")
		}
	})
	t.Run("If-Modified-Since never produces a 304", func(t *testing.T) {
		lm := first.Header.Get("Last-Modified")
		for _, r := range []*testenv.Replica{a, b, c} {
			if ans := e.get(r, "npm-local", "@acme%2fui", e.tokA, "If-Modified-Since", lm); ans.Status != 200 || len(ans.Body) == 0 {
				t.Fatalf("%s: %d", r.Name, ans.Status)
			}
		}
		if ans := e.get(b, "npm-local", "@acme%2fui", e.tokA, "If-None-Match", etag, "If-Modified-Since", "Mon, 01 Jan 2001 00:00:00 GMT"); ans.Status != 304 {
			t.Fatalf("matching If-None-Match with a date in the past: %d", ans.Status)
		}
	})
	t.Run("no document response is public", func(t *testing.T) {
		for _, p := range []string{"@acme%2fui", "@acme%2fui/latest", "@acme%2fui/1.0.0", "@acme%2fui?write=true"} {
			for _, accept := range []string{"", abbreviatedAccept} {
				ans := e.get(c, "npm-local", p, e.tokA, "Accept", accept)
				if cc := ans.Header.Get("Cache-Control"); ans.Status != 200 || strings.Contains(cc, "public") || cc == "" {
					t.Fatalf("%s: %d Cache-Control %q", p, ans.Status, cc)
				}
			}
		}
		if cc := e.get(c, "npm-local", "@acme%2fui", e.tokA).Header.Get("Cache-Control"); cc != "private, max-age=0, must-revalidate" {
			t.Fatalf("Cache-Control %q", cc)
		}
	})
	t.Run("a dist-tag change through another replica makes the old validator obsolete", func(t *testing.T) {
		if ans := e.send(c, "PUT", "npm-local", "-/package/@acme%2fui/dist-tags/stable", e.tokA, `"1.0.0"`); ans.Status != 200 {
			t.Fatalf("%d %s", ans.Status, ans.Body)
		}
		for _, r := range []*testenv.Replica{a, b, c} {
			ans := e.get(r, "npm-local", "@acme%2fui", e.tokA, "If-None-Match", etag)
			if ans.Status != 200 || ans.Header.Get("ETag") == etag {
				t.Fatalf("%s: %d ETag %q", r.Name, ans.Status, ans.Header.Get("ETag"))
			}
		}
	})
	t.Run("a reader without read on the package gets no validator and no 304", func(t *testing.T) {
		outsider := e.st.Token(e.st.User("mallory"), scopeDefault())
		for _, r := range []*testenv.Replica{a, b} {
			ans := e.get(r, "npm-local", "@acme%2fui", outsider, "If-None-Match", etag)
			if ans.Status == 200 || ans.Status == 304 || ans.Header.Get("ETag") != "" {
				t.Fatalf("%s: %d ETag %q", r.Name, ans.Status, ans.Header.Get("ETag"))
			}
		}
	})
}

// TestR_NPM_011_NegotiationAndEncoding: Accept chooses the form by quality and label
// and body agree; gzip is offered and decodes to the identity form.
func TestR_NPM_011_NegotiationAndEncoding(t *testing.T) {
	e := newEnv(t)
	a := e.replica("web-a")
	e.mustPublish(a, "npm-local", e.tokA, pkg{Name: "lodash", Version: "4.17.21", Deps: map[string]string{"a": "^1"}})
	abbr := e.get(a, "npm-local", "lodash", e.tokA, "Accept", abbreviatedAccept)
	if abbr.Header.Get("Content-Type") != "application/vnd.npm.install-v1+json" {
		t.Fatalf("Content-Type %q", abbr.Header.Get("Content-Type"))
	}
	doc := document(t, abbr)
	for _, k := range []string{"_rev", "readme", "_id"} {
		if _, ok := doc[k]; ok {
			t.Errorf("the abbreviated document holds %q", k)
		}
	}
	if doc["modified"] == nil || doc["time"] == nil || doc["dist-tags"] == nil {
		t.Fatalf("%v", doc)
	}
	full := e.get(a, "npm-local", "lodash", e.tokA, "Accept", "application/json")
	if full.Header.Get("Content-Type") != "application/json" || document(t, full)["_rev"] == nil || document(t, full)["readme"] == nil {
		t.Fatalf("full: %s", full.Body)
	}
	t.Run("?write=true returns the full document whatever Accept says, with a _rev and no attachments", func(t *testing.T) {
		ans := e.get(a, "npm-local", "lodash?write=true", e.tokA, "Accept", abbreviatedAccept)
		d := document(t, ans)
		if ans.Header.Get("Content-Type") != "application/json" || d["_rev"] == nil || d["_attachments"] != nil || ans.Header.Get("ETag") != "" {
			t.Fatalf("%d %s", ans.Status, ans.Header)
		}
	})
	t.Run("gzip", func(t *testing.T) {
		z := e.get(a, "npm-local", "lodash", e.tokA, "Accept-Encoding", "gzip")
		if z.Header.Get("Content-Encoding") != "gzip" || z.Header.Get("ETag") != full.Header.Get("ETag") {
			t.Fatalf("Content-Encoding %q", z.Header.Get("Content-Encoding"))
		}
	})
	t.Run("a package that does not exist is 404 in the native shape", func(t *testing.T) {
		ans := e.get(a, "npm-local", "nope", e.tokA)
		if ans.Status != 404 || code(t, ans) != "not_found" {
			t.Fatalf("%d %s", ans.Status, ans.Body)
		}
	})
	t.Run("R-NPM-014 the single-version document is the entry of the full document", func(t *testing.T) {
		one := e.get(a, "npm-local", "lodash/latest", e.tokA)
		byNumber := e.get(a, "npm-local", "lodash/4.17.21", e.tokA)
		if one.Status != 200 || string(one.Body) != string(byNumber.Body) {
			t.Fatalf("%d / %d", one.Status, byNumber.Status)
		}
		var got, want any
		_ = json.Unmarshal(one.Body, &got)
		want = document(t, full)["versions"].(map[string]any)["4.17.21"]
		g, _ := json.Marshal(got)
		w, _ := json.Marshal(want)
		if string(g) != string(w) {
			t.Fatalf("\n got %s\nwant %s", g, w)
		}
		if ans := e.get(a, "npm-local", "lodash/9.9.9", e.tokA); ans.Status != 404 || code(t, ans) != "not_found" {
			t.Fatalf("%d %s", ans.Status, ans.Body)
		}
	})
}

var isoMillis = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$`)

func timeOf(t testing.TB, s any) time.Time {
	t.Helper()
	str, _ := s.(string)
	if !isoMillis.MatchString(str) {
		t.Fatalf("%v is no ISO 8601 UTC timestamp with milliseconds", s)
	}
	v, err := time.Parse("2006-01-02T15:04:05.000Z", str)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// TestR_NPM_012_TimesComeFromThePostgreSQLClock: the times of a document are those the
// database wrote with its own clock after the row lock; they never move backwards, even
// when a row holds a time ahead of the clock.
func TestR_NPM_012_TimesComeFromThePostgreSQLClock(t *testing.T) {
	e := newEnv(t)
	a, b := e.replica("web-a"), e.replica("web-b")
	before := e.st.Text(`SELECT clock_timestamp()::text`)
	e.mustPublish(a, "npm-local", e.tokA, pkg{Name: "timed", Version: "1.0.0"})
	after := e.st.Text(`SELECT clock_timestamp()::text`)
	doc := document(t, e.get(b, "npm-local", "timed", e.tokA))
	tm := doc["time"].(map[string]any)
	published := timeOf(t, tm["1.0.0"])
	row := e.st.Text(`SELECT to_char(nv.published_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"')
		FROM npm_versions nv JOIN package_versions pv ON pv.id = nv.version_id JOIN packages p ON p.id = pv.package_id WHERE p.name = 'timed'`)
	if row != tm["1.0.0"] {
		t.Fatalf("time[1.0.0] = %v, the row says %s", tm["1.0.0"], row)
	}
	if e.st.Text(`SELECT ($1::timestamptz BETWEEN $2::timestamptz - interval '1 millisecond' AND $3::timestamptz + interval '1 millisecond')::text`, published.Format(time.RFC3339Nano), before, after) != "true" {
		t.Fatalf("the publish time %s is outside the window of the database clock (%s .. %s)", tm["1.0.0"], before, after)
	}
	if tm["created"] == nil || timeOf(t, tm["modified"]).Before(published) {
		t.Fatalf("time %v", tm)
	}
	t.Run("modified moves forward with every write, also within one millisecond", func(t *testing.T) {
		last := timeOf(t, tm["modified"])
		for i := range 30 {
			r := a
			if i%2 == 1 {
				r = b
			}
			var ans testenv.Answer
			if i%2 == 0 {
				ans = e.send(r, "PUT", "npm-local", "-/package/timed/dist-tags/wip", e.tokA, `"1.0.0"`)
			} else {
				ans = e.send(r, "DELETE", "npm-local", "-/package/timed/dist-tags/wip", e.tokA, nil)
			}
			if ans.Status != 200 {
				t.Fatalf("write %d: %d %s", i, ans.Status, ans.Body)
			}
			now := timeOf(t, document(t, e.get(b, "npm-local", "timed", e.tokA))["time"].(map[string]any)["modified"])
			if !now.After(last) {
				t.Fatalf("write %d: modified %s after %s", i, now.Format(time.RFC3339Nano), last.Format(time.RFC3339Nano))
			}
			last = now
		}
	})
	t.Run("a time ahead of the clock is never moved back", func(t *testing.T) {
		e.st.Exec(`UPDATE npm_packages SET modified_at = now() + interval '1 hour' WHERE package_id = (SELECT id FROM packages WHERE name = 'timed')`)
		ahead := e.st.Text(`SELECT to_char(modified_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"') FROM npm_packages WHERE package_id = (SELECT id FROM packages WHERE name = 'timed')`)
		if ans := e.send(a, "PUT", "npm-local", "-/package/timed/dist-tags/y", e.tokA, `"1.0.0"`); ans.Status != 200 {
			t.Fatalf("%d %s", ans.Status, ans.Body)
		}
		got := document(t, e.get(b, "npm-local", "timed", e.tokA))["time"].(map[string]any)["modified"].(string)
		if got <= ahead {
			t.Fatalf("modified %s after a row at %s", got, ahead)
		}
	})
	t.Run("a publish sets modified_at too, and a version's time is never after it", func(t *testing.T) {
		e.mustPublish(b, "npm-local", e.tokA, pkg{Name: "timed", Version: "1.0.1"})
		tm := document(t, e.get(a, "npm-local", "timed", e.tokA))["time"].(map[string]any)
		if timeOf(t, tm["1.0.1"]).After(timeOf(t, tm["modified"])) {
			t.Fatalf("time %v", tm)
		}
	})
}

// TestR_NPM_010_ThousandVersions: the document of a package with 1,000 versions renders
// on a miss in under 200 ms and is served from the cache in under 10 ms (R-NPM-010
// targets); the rendering itself is benchmarked in document.
func TestR_NPM_010_ThousandVersions(t *testing.T) {
	e := newEnv(t)
	a := e.replica("web-a")
	e.mustPublish(a, "npm-local", e.tokA, pkg{Name: "big", Version: "1.0.0"})
	// 999 more versions straight into the rows: a publish each would take minutes
	e.st.Exec(`
		WITH p AS (SELECT id FROM packages WHERE name = 'big'),
		     v AS (
		        INSERT INTO package_versions (package_id, version, display_version)
		        SELECT p.id, '1.' || g || '.0', '1.' || g || '.0' FROM p, generate_series(1, 999) g
		        RETURNING id, version)
		INSERT INTO npm_versions (version_id, manifest, integrity, shasum, file_count, unpacked_size, has_install_script, has_shrinkwrap, publish_tag, published_at)
		SELECT v.id,
		       jsonb_build_object('name', 'big', 'version', v.version, '_id', 'big@' || v.version, 'description', repeat('d', 200),
		         'dependencies', (SELECT jsonb_object_agg('dependency-' || d, '^1.0.0') FROM generate_series(1, 40) d),
		         'dist', jsonb_build_object('integrity', 'sha512-AAAA', 'shasum', repeat('a', 40), 'fileCount', 3, 'unpackedSize', 1200),
		         '_npmUser', jsonb_build_object('name', 'alice')),
		       'sha512-AAAA', repeat('a', 40), 3, 1200, false, false, 'latest', now()
		FROM v`)
	e.st.Exec(`UPDATE packages SET rev_seq = rev_seq + 1 WHERE name = 'big'`)
	url := "big"
	timed := func(hdr ...string) (time.Duration, int) {
		start := time.Now()
		ans := e.get(a, "npm-local", url, e.tokA, hdr...)
		d := time.Since(start)
		if ans.Status != 200 {
			t.Fatalf("%d %s", ans.Status, ans.Body[:min(len(ans.Body), 300)])
		}
		return d, len(ans.Body)
	}
	miss, size := timed()
	if got := len(versionNames(document(t, e.get(a, "npm-local", url, e.tokA)))); got != 1000 {
		t.Fatalf("%d versions", got)
	}
	t.Logf("1,000 versions: %d KiB, miss %s", size/1024, miss)
	if miss > 200*time.Millisecond {
		t.Errorf("the document of 1,000 versions took %s on a miss, the target is 200 ms", miss)
	}
	var hits []time.Duration
	for range 7 {
		d, _ := timed()
		hits = append(hits, d)
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i] < hits[j] })
	t.Logf("from the cache: median %s", hits[len(hits)/2])
	if m := hits[len(hits)/2]; m > 10*time.Millisecond {
		t.Errorf("the document of 1,000 versions took %s from the cache (median of 7), the target is 10 ms", m)
	}
	// a conditional request needs no rendering at all
	etag := e.get(a, "npm-local", url, e.tokA).Header.Get("ETag")
	start := time.Now()
	if ans := e.get(a, "npm-local", url, e.tokA, "If-None-Match", etag); ans.Status != 304 {
		t.Fatalf("%d", ans.Status)
	}
	if d := time.Since(start); d > 50*time.Millisecond {
		t.Errorf("a 304 took %s", d)
	}
}
