//go:build integration

package ansibleremote

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/ansiblestub"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

const docsPath = "v3/collections/acme/tools/versions/1.0.0/docs-blob/"

// jobsReplica starts a replica that also claims jobs: the documentation fetch is a job (R-ANS-042).
func (e *aenv) jobsReplica() *testenv.Replica {
	e.t.Helper()
	return e.st.Start(e.t, testenv.ReplicaOptions{Name: "all-1", Role: "all", DialOverride: e.dial})
}

func (e *aenv) docsRequests() int { return e.stub.Counts().OfKind(ansiblestub.ClassDocs, "GET") }

func (e *aenv) docsState(repo string) string {
	return e.st.Text(`SELECT COALESCE((SELECT 'available' FROM ansible_version_docs WHERE repo_id = $1 AND version = '1.0.0'),
		(SELECT state || ':' || reason FROM ansible_doc_fetches WHERE repo_id = $1 AND version = '1.0.0'), 'none')`, e.repoID(repo))
}

// TestR_ANS_035_TheUpstreamsDocumentationIsServedSanitizedAndFetchedOnce: a client's request for docs-blob/ fetches the upstream's document once however
// many times it is read, serves it without script, and writes the rows with the source upstream.
func TestR_ANS_035_TheUpstreamsDocumentationIsServedSanitizedAndFetchedOnce(t *testing.T) {
	e := newEnv(t, options{})
	r := e.jobsReplica()
	repo := e.fresh()
	var first testenv.Answer
	for i := range 3 {
		a := e.get(r, repo, docsPath)
		if a.Status != http.StatusOK {
			t.Fatalf("read %d: %d %s", i, a.Status, a.Body)
		}
		if i == 0 {
			first = a
		}
	}
	body := string(first.Body)
	for _, bad := range []string{"<script", "onerror", "javascript:"} {
		if strings.Contains(body, bad) {
			t.Errorf("the served document holds %q: %s", bad, body)
		}
	}
	for _, want := range []string{`"content_name":"ping_host"`, `"content_name":"webserver"`, ">acme.tools</h1>", `"install.md"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the served document lacks %s", want)
		}
	}
	if n := e.docsRequests(); n != 1 {
		t.Errorf("the stub saw %d requests for the documentation of three reads, want 1", n)
	}
	eventually(t, 10*time.Second, "the rows of the fetched document", func() bool { return e.docsState(repo) == "available" })
	if got := e.st.Text(`SELECT source || ':' || entry_count FROM ansible_version_docs WHERE repo_id = $1`, e.repoID(repo)); got != "upstream:3" {
		t.Errorf("the rows are %q", got)
	}
	if got := e.st.Number(`SELECT count(*) FROM ansible_doc_entries WHERE repo_id = $1 AND content_type = 'role' AND readme_html LIKE '%<script>%'`, e.repoID(repo)); got != 1 {
		t.Errorf("the role README is stored as received: %d", got)
	}
}

// TestR_ANS_042_TheVersionDocumentQueuesTheFetchAndTheJobFillsTheRows: filling the version document queues one job, which fetches the document
// through the engine; a second fill of the same version queues nothing and the stub counts one documentation request.
func TestR_ANS_042_TheVersionDocumentQueuesTheFetchAndTheJobFillsTheRows(t *testing.T) {
	e := newEnv(t, options{})
	r := e.jobsReplica()
	repo := e.fresh()
	if a := e.get(r, repo, "v3/collections/acme/tools/versions/1.0.0/"); a.Status != http.StatusOK {
		t.Fatalf("version document: %d %s", a.Status, a.Body)
	}
	eventually(t, 30*time.Second, "the documentation job", func() bool { return e.docsState(repo) == "available" })
	e.expire(repo)
	e.get(r, repo, "v3/collections/acme/tools/versions/1.0.0/")
	if n := e.st.Number(`SELECT count(*) FROM job WHERE kind = 'ansible-docs-fill'`); n != 1 {
		t.Errorf("%d jobs ansible-docs-fill after two fills of the version document, want 1", n)
	}
	if n := e.docsRequests(); n != 1 {
		t.Errorf("the stub saw %d documentation requests, want 1", n)
	}
}

// TestR_ANS_035_AFailureOfTheUpstreamIsTheStateUnavailableAndAnAbsentDocumentIsNone: docs_500 is a 502 and unavailable upstream_error, a
// document above the bound too_large, one with an item without a name invalid_document, and a 404 the state none.
func TestR_ANS_035_AFailureOfTheUpstreamIsTheStateUnavailableAndAnAbsentDocumentIsNone(t *testing.T) {
	e := newEnv(t, options{})
	r := e.jobsReplica()
	for mode, tc := range map[string]struct {
		status int
		state  string
	}{
		ansiblestub.DocsError:    {http.StatusBadGateway, "unavailable:upstream_error"},
		ansiblestub.DocsHuge:     {http.StatusBadGateway, "unavailable:too_large"},
		ansiblestub.DocsInvalid:  {http.StatusBadGateway, "unavailable:invalid_document"},
		ansiblestub.DocsNotFound: {http.StatusNotFound, "none:"},
	} {
		if err := e.stub.SetMode(ansiblestub.Mode{Docs: mode}); err != nil {
			t.Fatal(err)
		}
		repo := e.fresh()
		a := e.get(r, repo, docsPath)
		if a.Status != tc.status {
			t.Errorf("%s: status %d %s, want %d", mode, a.Status, a.Body, tc.status)
		}
		if got := e.docsState(repo); got != tc.state {
			t.Errorf("%s: the state is %q, want %q", mode, got, tc.state)
		}
		if got := e.st.Number(`SELECT count(*) FROM ansible_doc_entries WHERE repo_id = $1`, e.repoID(repo)); got != 0 {
			t.Errorf("%s: %d entry rows of a refused document", mode, got)
		}
	}
}

// TestR_ANS_035_ARepositoryThatDoesNotFetchDocumentationAsksTheStubNothing: with ansible.fetchDocumentation false the route answers 404 and the
// version document queues no job.
func TestR_ANS_035_ARepositoryThatDoesNotFetchDocumentationAsksTheStubNothing(t *testing.T) {
	e := newEnv(t, options{})
	r := e.jobsReplica()
	if a := e.get(r, rDocsOff, docsPath); a.Status != http.StatusNotFound {
		t.Errorf("docs-blob: %d %s", a.Status, a.Body)
	}
	if a := e.get(r, rDocsOff, "v3/collections/acme/tools/versions/1.0.0/"); a.Status != http.StatusOK {
		t.Errorf("version document: %d %s", a.Status, a.Body)
	}
	time.Sleep(time.Second)
	if n := e.docsRequests(); n != 0 {
		t.Errorf("the stub saw %d documentation requests, want none", n)
	}
	if n := e.st.Number(`SELECT count(*) FROM job WHERE kind = 'ansible-docs-fill'`); n != 0 {
		t.Errorf("%d documentation jobs", n)
	}
}

// TestR_ANS_035_AnHrefOnAnotherHostIsNotFetched: a version document whose href is on another host gives no address for the documentation:
// the stub counts no request for it and the state is none.
func TestR_ANS_035_AnHrefOnAnotherHostIsNotFetched(t *testing.T) {
	e := newEnv(t, options{})
	r := e.jobsReplica()
	if err := e.stub.SetMode(ansiblestub.Mode{DocsForeignHref: true}); err != nil {
		t.Fatal(err)
	}
	repo := e.fresh()
	if a := e.get(r, repo, "v3/collections/acme/tools/versions/1.0.0/"); a.Status != http.StatusOK {
		t.Fatalf("version document: %d %s", a.Status, a.Body)
	}
	eventually(t, 10*time.Second, "the fetch row", func() bool { return e.docsState(repo) == "none:" })
	if a := e.get(r, repo, docsPath); a.Status != http.StatusNotFound {
		t.Errorf("docs-blob: %d %s", a.Status, a.Body)
	}
	if n := e.docsRequests(); n != 0 {
		t.Errorf("the stub saw %d documentation requests, want none", n)
	}
}
