package npmstub

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"testing"
)

// The tests of the replication paths carry R-REPO-059 (the counting stub of the mirror's test suites) and R-REPO-051 (the
// requests the npm format's discovery sends).

// replGet sends a GET to the stub as the replication host: the same listener, another Host header.
func (f *fixture) replGet(host, path string, hdr ...string) reply {
	f.t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, f.run.URL+path, nil)
	if err != nil {
		f.t.Fatal(err)
	}
	req.Host = host
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := f.client.Do(req)
	if err != nil {
		f.t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close() //nolint:errcheck // test client
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		f.t.Fatal(err)
	}
	return reply{resp.StatusCode, resp.Header, data}
}

func (f *fixture) repl(path string, hdr ...string) reply {
	f.t.Helper()
	return f.replGet(DefaultReplicationHost, path, hdr...)
}

func keyQuery(start, end string, limit string) string {
	q := url.Values{}
	if start != "" {
		b, _ := json.Marshal(start)
		q.Set("startkey", string(b))
	}
	if end != "" {
		b, _ := json.Marshal(end)
		q.Set("endkey", string(b))
	}
	if limit != "" {
		q.Set("limit", limit)
	}
	return q.Encode()
}

type allDocs struct {
	TotalRows int `json:"total_rows"`
	Rows      []struct {
		ID, Key string
	} `json:"rows"`
}

func (f *fixture) allDocs(query string) (allDocs, int) {
	f.t.Helper()
	r := f.repl("/registry/_all_docs?" + query)
	var out allDocs
	if r.status == 200 {
		if err := json.Unmarshal(r.body, &out); err != nil {
			f.t.Fatalf("%s: %v", r.body, err)
		}
	}
	return out, r.status
}

type changesAnswer struct {
	Results []struct {
		Seq     int    `json:"seq"`
		ID      string `json:"id"`
		Deleted bool   `json:"deleted"`
	} `json:"results"`
	LastSeq int `json:"last_seq"`
}

func (f *fixture) changes(query string) changesAnswer {
	f.t.Helper()
	r := f.repl("/registry/_changes?" + query)
	if r.status != 200 {
		f.t.Fatalf("_changes?%s: %d %s", query, r.status, r.body)
	}
	var out changesAnswer
	if err := json.Unmarshal(r.body, &out); err != nil {
		f.t.Fatalf("%s: %v", r.body, err)
	}
	return out
}

func TestReplicationRootAnswersUpdateSeq(t *testing.T) {
	// R-REPO-059: the root of the replication host answers update_seq; the registry host does not answer these paths.
	f := newFixture(t, Config{})
	r := f.repl("/")
	var root struct {
		DBName    string `json:"db_name"`
		UpdateSeq int    `json:"update_seq"`
		DocCount  int    `json:"doc_count"`
	}
	if err := json.Unmarshal(r.body, &root); err != nil || r.status != 200 {
		t.Fatalf("%d %s: %v", r.status, r.body, err)
	}
	if root.UpdateSeq != len(f.stub.cfg.Corpus.Packages)+len(f.stub.recorded) || root.UpdateSeq != root.DocCount || root.DBName != "registry" {
		t.Fatalf("root %+v, %d corpus packages, %d recorded", root, len(f.stub.cfg.Corpus.Packages), len(f.stub.recorded))
	}
	if st := f.status("/"); st != http.StatusNotFound {
		t.Errorf("the registry host answered the root with %d", st)
	}
	if st := f.status("/registry/_all_docs"); st != http.StatusNotFound {
		t.Errorf("the registry host answered _all_docs with %d", st)
	}
	if st := f.replGet("registry.example.test:8443", "/"); st.status != http.StatusNotFound {
		t.Errorf("a host that is no replication host answered the root with %d", st.status)
	}
	// The host may carry a port and any case.
	if st := f.replGet("Replicate.NPMJS.com:443", "/").status; st != 200 {
		t.Errorf("a replication host with a port: %d", st)
	}
}

func TestReplicationHostsAreConfigurable(t *testing.T) {
	// R-REPO-059: the second host name is a setting of the stub.
	f := newFixture(t, Config{ReplicationHosts: []string{"npm-replica.test"}})
	if st := f.replGet("npm-replica.test", "/").status; st != 200 {
		t.Errorf("the configured host: %d", st)
	}
	if st := f.repl("/").status; st != http.StatusNotFound {
		t.Errorf("the default host with another configured: %d", st)
	}
	none := newFixture(t, Config{ReplicationHosts: []string{}})
	if st := none.repl("/").status; st != http.StatusNotFound {
		t.Errorf("no replication host configured: %d", st)
	}
}

func TestReplicationAllDocsKeyRange(t *testing.T) {
	// R-REPO-059, R-REPO-051: startkey, endkey and limit, the ids sorted by bytes, both ends inclusive.
	f := newFixture(t, Config{})
	for _, name := range []string{"@acme/a", "@acme/b", "@acme/c", "@acme2/x", "@acmf/z"} {
		if err := f.stub.Publish(name, "1.0.0", nil); err != nil {
			t.Fatal(err)
		}
	}
	got, st := f.allDocs(keyQuery("@acme/", "@acme/￰", "10000"))
	if st != 200 {
		t.Fatalf("status %d", st)
	}
	var ids []string
	for _, r := range got.Rows {
		ids = append(ids, r.ID)
		if r.ID != r.Key {
			t.Errorf("id %q, key %q", r.ID, r.Key)
		}
	}
	if strings.Join(ids, ",") != "@acme/a,@acme/b,@acme/c" {
		t.Fatalf("the range holds %v", ids)
	}
	// The whole key space is sorted, and a limit pages by startkey (inclusive).
	all, _ := f.allDocs(keyQuery("", "", "10000"))
	if !sort.SliceIsSorted(all.Rows, func(i, j int) bool { return all.Rows[i].ID < all.Rows[j].ID }) || all.TotalRows != len(all.Rows) {
		t.Fatalf("%d rows of %d, sorted %v", len(all.Rows), all.TotalRows, sort.SliceIsSorted(all.Rows, func(i, j int) bool { return all.Rows[i].ID < all.Rows[j].ID }))
	}
	first, _ := f.allDocs(keyQuery("", "", "3"))
	next, _ := f.allDocs(keyQuery(first.Rows[2].ID, "", "3"))
	if len(first.Rows) != 3 || len(next.Rows) != 3 || next.Rows[0].ID != first.Rows[2].ID || next.Rows[1].ID != all.Rows[3].ID {
		t.Fatalf("paging: %v then %v", first.Rows, next.Rows)
	}
	// A package a deleted change removed is not listed.
	f.stub.Change("@acme/b", true)
	got, _ = f.allDocs(keyQuery("@acme/", "@acme/￰", "10000"))
	if len(got.Rows) != 2 {
		t.Fatalf("after the deletion: %v", got.Rows)
	}
}

func TestReplicationRefusesWhatTheServiceRefuses(t *testing.T) {
	// R-REPO-051: no include_docs, no skip, a limit of at most 10000, JSON string keys.
	f := newFixture(t, Config{})
	for _, path := range []string{
		"/registry/_all_docs?include_docs=true", "/registry/_all_docs?skip=5", "/registry/_changes?include_docs=true",
		"/registry/_all_docs?limit=10001", "/registry/_all_docs?limit=0", "/registry/_changes?limit=x",
		"/registry/_all_docs?startkey=abc", "/registry/_changes?since=abc",
	} {
		if st := f.repl(path).status; st != http.StatusBadRequest {
			t.Errorf("%s answered %d, want 400", path, st)
		}
	}
	if st := f.repl("/registry/_all_docs?limit=10000").status; st != 200 {
		t.Errorf("limit 10000: %d", st)
	}
	if st := f.repl("/registry/nope").status; st != http.StatusNotFound {
		t.Errorf("an unknown path: %d", st)
	}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, f.run.URL+"/registry/_all_docs", strings.NewReader("{}"))
	req.Host = DefaultReplicationHost
	resp, err := f.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST: %d", resp.StatusCode)
	}
}

func TestReplicationChangesFeed(t *testing.T) {
	// R-REPO-059, R-REPO-051: _changes with since and limit, a change for a published version, a deleted change, one entry per
	// id, and the feed's position (last_seq) that the next call sends back as since.
	f := newFixture(t, Config{})
	base := f.stub.UpdateSeq()
	if base == 0 {
		t.Fatal("the feed starts empty")
	}
	if got := f.changes("since=" + itoa(base) + "&limit=10000"); len(got.Results) != 0 || got.LastSeq != base {
		t.Fatalf("nothing changed yet: %+v", got)
	}
	if err := f.stub.Publish("@acme/a", "1.0.0", nil); err != nil {
		t.Fatal(err)
	}
	f.stub.Change("@acme/b", true)
	if err := f.stub.Publish("@acme/a", "1.1.0", nil); err != nil {
		t.Fatal(err)
	}
	got := f.changes("since=" + itoa(base) + "&limit=10000")
	if len(got.Results) != 2 || got.Results[0].ID != "@acme/b" || !got.Results[0].Deleted || got.Results[1].ID != "@acme/a" || got.Results[1].Deleted {
		t.Fatalf("results %+v (one entry per id, the last change)", got.Results)
	}
	if got.LastSeq != f.stub.UpdateSeq() || got.LastSeq != base+3 {
		t.Fatalf("last_seq %d, update_seq %d, base %d", got.LastSeq, f.stub.UpdateSeq(), base)
	}
	// A limit cuts the feed, and last_seq is then the last result's.
	page := f.changes("since=" + itoa(base) + "&limit=1")
	if len(page.Results) != 1 || page.LastSeq != page.Results[0].Seq {
		t.Fatalf("limited page %+v", page)
	}
	// Without since, the feed is read from its beginning.
	if all := f.changes("limit=10000"); len(all.Results) != len(f.stub.namesLocked())+1 { // +1: @acme/b is deleted, not listed
		t.Fatalf("%d results from the beginning", len(all.Results))
	}
	// The control API adds the same changes.
	f2 := newFixture(t, Config{ControlToken: "tok"})
	h := []string{"Authorization", "Bearer tok"}
	if st := f2.do(http.MethodPost, "/-/stub/change", map[string]string{"Authorization": "Bearer tok"}, strings.NewReader(`{"name":"@acme/x","deleted":true}`)).status; st != http.StatusNoContent {
		t.Fatalf("change: %d", st)
	}
	if st := f2.do(http.MethodPost, "/-/stub/publish", map[string]string{"Authorization": "Bearer tok"}, strings.NewReader(`{"name":"@acme/y","version":"1.0.0"}`)).status; st != http.StatusNoContent {
		t.Fatalf("publish: %d", st)
	}
	if st := f2.do(http.MethodPost, "/-/stub/change", map[string]string{"Authorization": "Bearer tok"}, strings.NewReader(`{}`)).status; st != http.StatusBadRequest {
		t.Fatalf("change without a name: %d", st)
	}
	var log struct {
		UpdateSeq int `json:"update_seq"`
	}
	if err := json.Unmarshal(f2.get("/-/stub/replication", h...).body, &log); err != nil || log.UpdateSeq != f2.stub.UpdateSeq() {
		t.Fatalf("control log %+v, %v", log, err)
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func TestReplicationDeletedPackageAnswers404(t *testing.T) {
	// R-REPO-059: the stub answers the document and the tarballs of a package a deleted change removed with 404, and a later
	// publish brings it back.
	f := newFixture(t, Config{})
	if err := f.stub.Publish("@acme/b", "1.0.0", nil); err != nil {
		t.Fatal(err)
	}
	doc := docPath("@acme/b")
	if st := f.status(doc); st != 200 {
		t.Fatalf("before: %d", st)
	}
	f.stub.Change("@acme/b", true)
	if st := f.status(doc); st != http.StatusNotFound {
		t.Fatalf("document after the deletion: %d", st)
	}
	if st := f.status("/@acme/b/-/b-1.0.0.tgz"); st != http.StatusNotFound {
		t.Fatalf("tarball after the deletion: %d", st)
	}
	if err := f.stub.Publish("@acme/b", "1.0.1", nil); err != nil {
		t.Fatal(err)
	}
	if st := f.status(doc); st != 200 {
		t.Fatalf("after a new publish: %d", st)
	}
}

func TestReplicationLogRecordsTheCredential(t *testing.T) {
	// R-REPO-059, R-REPO-051: the stub records whether the replication host received an Authorization header, with the raw
	// query, and counts the requests under the host; the registry host's requests are not in the log.
	f := newFixture(t, Config{})
	f.repl("/")
	f.repl("/registry/_changes?since=3&limit=10000", "Authorization", "Bearer secret")
	f.get(docPath("left-pad"))
	log := f.stub.Replications()
	if len(log) != 2 || log[0].Authorized() || !log[1].Authorized() || log[1].Auth != "bearer" ||
		log[1].Query != "since=3&limit=10000" || log[1].Path != "/registry/_changes" || log[0].Host != DefaultReplicationHost {
		t.Fatalf("log %+v", log)
	}
	for _, e := range f.stub.Counts().Entries {
		if e.Kind == KindReplication && (e.Host != DefaultReplicationHost || e.Name != "") {
			t.Errorf("entry %+v", e)
		}
		if e.Kind != KindReplication && e.Host != "" {
			t.Errorf("a registry request carries the host %q", e.Host)
		}
	}
	if n := f.stub.Counts().Requests(http.MethodGet, "/registry/_changes"); n != 1 {
		t.Errorf("%d counted", n)
	}
	f.stub.Reset()
	if len(f.stub.Replications()) != 0 {
		t.Error("Reset kept the log")
	}
}

func TestReplicationFollowsTheModes(t *testing.T) {
	// The failure switches apply to the replication host as to the registry host: down, delay by class "replication".
	f := newFixture(t, Config{})
	if err := f.stub.SetMode(Mode{Down: DownUnavailable}); err != nil {
		t.Fatal(err)
	}
	if st := f.repl("/").status; st != http.StatusServiceUnavailable {
		t.Errorf("down: %d", st)
	}
	if err := f.stub.SetMode(Mode{DelayMS: map[string]int{KindReplication: 1}}); err != nil {
		t.Fatalf("a delay by class replication: %v", err)
	}
	if st := f.repl("/").status; st != 200 {
		t.Errorf("delayed: %d", st)
	}
	f.stub.ResetAll()
	if f.stub.UpdateSeq() != len(f.stub.cfg.Corpus.Packages)+len(f.stub.recorded) {
		t.Errorf("ResetAll left the feed at %d", f.stub.UpdateSeq())
	}
}
