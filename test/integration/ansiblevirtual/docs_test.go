//go:build integration

package ansiblevirtual

import (
	"net/http"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/test/ansiblestub"
)

func docsPathOf(ns, name, version string) string {
	return "v3/collections/" + ns + "/" + name + "/versions/" + version + "/docs-blob/"
}

// TestR_ANS_041_AVirtualRepositoryServesTheDocumentationOfTheFirstMemberThatHasIt: a version both members hold is documented by the local
// member whatever the order of the members, a collection only the remote member holds by the remote member, and a version nobody documents is 404.
func TestR_ANS_041_AVirtualRepositoryServesTheDocumentationOfTheFirstMemberThatHasIt(t *testing.T) {
	e := newEnv(t, options{})
	s := spec("acme", "tools", "1.0.0")
	s.Files["plugins/modules/local_only.py"] = "DOCUMENTATION = '''\nshort_description: only in the local member\n'''\n"
	if a, _ := e.publish(rLocal, e.writer, s); a.Status != http.StatusAccepted {
		t.Fatalf("publish: %d %s", a.Status, a.Body)
	}

	for _, repo := range []string{rVirtual, rReverse} {
		a := e.get(repo, docsPathOf("acme", "tools", "1.0.0"), e.reader)
		if a.Status != http.StatusOK {
			t.Fatalf("%s: %d %s", repo, a.Status, a.Body)
		}
		body := string(a.Body)
		if !strings.Contains(body, `"content_name":"local_only"`) || strings.Contains(body, "ping_host") {
			t.Errorf("%s serves the documentation of the wrong member: %s", repo, body)
		}
	}
	if n := e.stub.Counts().OfKind(ansiblestub.ClassDocs, "GET"); n != 0 {
		t.Errorf("the stub saw %d documentation requests though the local member has the documentation", n)
	}

	remote := e.get(rVirtual, docsPathOf("acme", "base", "1.0.0"), e.reader)
	if remote.Status != http.StatusOK || !strings.Contains(string(remote.Body), `"content_name":"ping_host"`) || strings.Contains(string(remote.Body), "<script") {
		t.Errorf("the remote member's documentation: %d %s", remote.Status, remote.Body)
	}
	if again := e.get(rVirtual, docsPathOf("acme", "base", "1.0.0"), e.reader); again.Status != http.StatusOK {
		t.Errorf("a second read: %d", again.Status)
	}
	if n := e.stub.Counts().OfKind(ansiblestub.ClassDocs, "GET"); n != 1 {
		t.Errorf("the stub saw %d documentation requests of two reads through the virtual repository, want 1", n)
	}
	if missing := e.get(rVirtual, docsPathOf("acme", "nosuch", "1.0.0"), e.reader); missing.Status != http.StatusNotFound {
		t.Errorf("a collection nobody holds: %d", missing.Status)
	}
	if noLocal := e.get(rNoLocal, docsPathOf("acme", "tools", "1.0.0"), e.reader); noLocal.Status != http.StatusOK || !strings.Contains(string(noLocal.Body), "ping_host") {
		t.Errorf("a virtual repository over the remote member alone: %d %s", noLocal.Status, noLocal.Body)
	}
}
