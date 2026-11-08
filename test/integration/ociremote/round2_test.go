//go:build integration

package ociremote

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/ocistub"
)

// Round 2 of WP8: what the first review and the audit found.

func TestR_OCI_024_ACutPageSentWithAContentLengthIsStoredAndFollowedByTheUpstreamsPages(t *testing.T) {
	// The registry cuts its lists at two tags and sends them with a Content-Length, as every small answer has one.
	e := newEnv(t, options{stub: ocistub.Config{PageSize: 2}})
	rep := e.start(1)[0]
	type page struct {
		Tags []string `json:"tags"`
	}
	read := func(path string) (page, string) {
		a := e.get(rep, rAnon, path)
		if a.Status != 200 {
			t.Fatalf("%s: %d %s", path, a.Status, a.Body)
		}
		var p page
		if err := json.Unmarshal(a.Body, &p); err != nil {
			t.Fatal(err)
		}
		return p, a.Header.Get("Link")
	}
	p, link := read("acme/a/tags/list?n=5")
	if strings.Join(p.Tags, ",") != "1.0,1.1,1.2,2.0,latest" || link == "" {
		// Five tags exist and five were asked for: the upstream may have more, so there is a Link, and its page is empty.
		t.Fatalf("tags %v link %q", p.Tags, link)
	}
	p, link = read("acme/a/tags/list?n=3")
	if strings.Join(p.Tags, ",") != "1.0,1.1,1.2" || link == "" {
		t.Errorf("n=3: %v link %q", p.Tags, link)
	}
	p, link = read("acme/a/tags/list?n=9")
	if len(p.Tags) != 5 || link != "" {
		t.Errorf("n=9: %v link %q", p.Tags, link)
	}
}

func TestR_OCI_024_EveryPageOfALongListHasALinkUntilTheListEnds(t *testing.T) {
	e := newEnv(t, options{})
	rep := e.start(1)[0]
	// The upstream cuts the 2,500 tags at 700 a page.
	total, path := 0, "bulk/tags/tags/list?n=1000"
	for range 10 {
		a := e.get(rep, rAnon, path)
		if a.Status != 200 {
			t.Fatalf("%s: %d %s", path, a.Status, a.Body)
		}
		var p struct {
			Tags []string `json:"tags"`
		}
		_ = json.Unmarshal(a.Body, &p)
		total += len(p.Tags)
		link := a.Header.Get("Link")
		if link == "" {
			break
		}
		i := strings.Index(link, "last=")
		path = "bulk/tags/tags/list?n=1000&last=" + link[i+5:strings.IndexAny(link[i:], "&>")+i]
	}
	if total != ocistub.BulkTags {
		t.Errorf("walked %d tags, want %d", total, ocistub.BulkTags)
	}
}

func TestR_OCI_021_AnImageTheRegistryDeniesToAValidTokenIsMissingAndTheChallengeIsKept(t *testing.T) {
	e := newEnv(t, options{})
	rep := e.start(1)[0]
	if err := e.stub.SetMode(ocistub.Mode{PrivateRepos: []string{"acme/a"}}); err != nil {
		t.Fatal(err)
	}
	e.pull(rep, rAnon, "acme/b", "1.0") // the first request of the replica: the challenge is learnt
	for range 2 {
		a := e.get(rep, rAnon, "acme/a/manifests/1.0")
		if a.Status != http.StatusNotFound || !strings.Contains(string(a.Body), "MANIFEST_UNKNOWN") {
			t.Fatalf("%d %s", a.Status, a.Body)
		}
	}
	// The second answer was the negative entry: the registry saw the private image's manifest once.
	got := 0
	for _, en := range e.stub.Counts().ManifestEntries("acme/a", "1.0", "GET") {
		got += en.Count
	}
	if got != 1 {
		t.Errorf("%d requests for the private image, want 1 (a negative entry answers the next)", got)
	}
	before := e.stub.Counts().WithStatus(http.StatusUnauthorized)
	e.pull(rep, rAnon, "acme/tool", "1")
	if after := e.stub.Counts().WithStatus(http.StatusUnauthorized); after != before {
		t.Errorf("the pull after the refusal was challenged %d times: the remembered challenge was forgotten", after-before)
	}
}

func TestR_OCI_023_ABlobRedirectIsFollowedWhateverExtraHostsSaysAndTheDefaultsListNoCDN(t *testing.T) {
	e := newEnv(t, options{})
	rep := e.start(1)[0]
	e.pull(rep, rNoCDN, "acme/a", "1.0")
	if e.stub.Counts().CDNRequests("") == 0 {
		t.Error("no blob came from the CDN")
	}
	if got := e.stub.Counts().AuthSchemes(e.host(ocistub.LoopbackCDNHost)); strings.Join(got, ",") != "none" {
		t.Errorf("the CDN saw %v", got)
	}
	// The same through the Docker Hub defaults, which list the realm only.
	h := e.get(rep, rHub, "alpine/manifests/3.19")
	if h.Status != 200 {
		t.Fatalf("%d %s", h.Status, h.Body)
	}
	e.pull(rep, rHub, "alpine", "3.19")
	if e.hub.Counts().CDNRequests("") == 0 {
		t.Error("Docker Hub's blobs did not come from its CDN host")
	}
}

func TestR_OCI_020_ATagWhoseBytesAreNotItsDigestRaisesTheIntegrityEvent(t *testing.T) {
	e := newEnv(t, options{})
	rep := e.start(1)[0]
	if err := e.stub.SetMode(ocistub.Mode{WrongDigest: []string{"acme/a:1.0"}}); err != nil {
		t.Fatal(err)
	}
	if a := e.get(rep, rAnon, "acme/a/manifests/1.0"); a.Status != http.StatusBadGateway {
		t.Fatalf("%d %s", a.Status, a.Body)
	}
	eventually(t, 15*time.Second, "the integrity event", func() bool {
		return e.st.Number(`SELECT count(*) FROM audit_event WHERE action = 'proxy.integrity_mismatch'`) >= 1
	})
}

func TestR_OCI_025_AnExpiredTagIsServedFromTheCacheWhileTheUpstreamAnswers429(t *testing.T) {
	e := newEnv(t, options{})
	rep := e.start(1)[0]
	if err := e.stub.SetMode(ocistub.Mode{PullQuota: &ocistub.PullQuota{Limit: 1}}); err != nil {
		t.Fatal(err)
	}
	a := e.get(rep, rFast, "acme/a/manifests/latest")
	if a.Status != 200 {
		t.Fatalf("%d %s", a.Status, a.Body)
	}
	first := a.Header.Get("Docker-Content-Digest")
	if _, err := e.stub.MoveTag("acme/a", "latest"); err != nil {
		t.Fatal(err)
	}
	// The period passes, the HEAD shows the tag moved, the GET is the 429 of an used-up quota: the stored copy answers.
	eventually(t, 20*time.Second, "the expired tag to be revalidated against a moved tag", func() bool {
		b := e.get(rep, rFast, "acme/a/manifests/latest")
		return b.Status == 200 && b.Header.Get("Docker-Content-Digest") == first && e.stub.Counts().WithStatus(http.StatusTooManyRequests) >= 1
	})
}

func TestR_OCI_021_AWriteToARemoteRepositoryIsRefused(t *testing.T) {
	e := newEnv(t, options{})
	rep := e.start(1)[0]
	for _, c := range []struct{ method, path string }{
		{http.MethodPut, "acme/a/manifests/1.0"},
		{http.MethodPost, "acme/a/blobs/uploads/"},
		{http.MethodDelete, "acme/a/manifests/1.0"},
	} {
		// An administrator holds every action, so the refusal is the class of the repository and not a missing permission.
		a := e.doAs(rep, e.admin, "pull,push,delete", c.method, rAnon, c.path)
		if a.Status != http.StatusMethodNotAllowed {
			t.Errorf("%s %s: %d %s", c.method, c.path, a.Status, a.Body)
		}
	}
}

func TestR_REPO_024_TheUpstreamTestOfAPrefixedRegistryAsksTheRootOfTheHost(t *testing.T) {
	e := newEnv(t, options{})
	e.start(1)
	a := e.st.Do(e.st.Pick(), http.MethodPost, "/api/v1/projects/it/repositories/"+rPrefix+"/upstream-test", e.admin, nil)
	if a.Status != 200 {
		t.Fatalf("%d %s", a.Status, a.Body)
	}
	var r struct {
		HTTPStatus    *int  `json:"httpStatus"`
		Authenticated *bool `json:"authenticated"`
	}
	if err := json.Unmarshal(a.Body, &r); err != nil {
		t.Fatal(err)
	}
	if r.HTTPStatus == nil || *r.HTTPStatus != 401 || r.Authenticated != nil {
		t.Errorf("result %s: the version check is /v2/ at the host, not below the prefix", a.Body)
	}
	for _, en := range e.stub.Counts().Entries {
		if en.Kind == ocistub.KindBase && en.Path != "/v2/" {
			t.Errorf("the probe asked %s", en.Path)
		}
	}
	// A pull through the prefix asks for acme/<name>.
	if m := e.get(e.st.Pick(), rPrefix, "a/manifests/1.0"); m.Status != 200 {
		t.Errorf("pull of acme/a through the prefix: %d %s", m.Status, m.Body)
	}
}
