// Self-tests of the counting stub of R-OCI-035; they check the test tool, not trove, and carry no requirement ID (Q-ROAD-5).

package ocistub

import (
	"net/http"
	"slices"
	"strings"
	"testing"
)

func TestCountsPerHostMethodPathAndStatus(t *testing.T) {
	f := newFixture(t, Config{})
	f.get(f.reg+"/v2/", nil) // 401
	hdr := f.bearer("repository:library/alpine:pull")
	hdr["Accept"] = acceptAll
	f.get(f.reg+"/v2/library/alpine/manifests/3", hdr)
	f.get(f.reg+"/v2/library/alpine/manifests/3", hdr)
	f.do(http.MethodHead, f.reg+"/v2/library/alpine/manifests/3", hdr)
	f.get(f.reg+"/v2/library/alpine/manifests/missing", hdr)
	f.get(f.reg+"/v2/library/alpine/tags/list?n=2", hdr)

	c := f.stub.Counts()
	if got := c.ManifestGets("library/alpine", "3"); got != 2 {
		t.Errorf("manifest GETs = %d, want 2", got)
	}
	if got := c.ManifestHeads("library/alpine", "3"); got != 1 {
		t.Errorf("manifest HEADs = %d, want 1", got)
	}
	if got := c.ManifestGets("library/alpine", ""); got != 3 {
		t.Errorf("manifest GETs of any reference = %d, want 3", got)
	}
	if got := c.TagLists("library/alpine"); got != 1 || c.TagLists("") != 1 {
		t.Errorf("tag lists = %d", got)
	}
	if c.TokenRequests("") != 1 || c.TokenRequests("none") != 1 || c.TokenRequests("basic") != 0 {
		t.Errorf("token requests by scheme: any %d, none %d, basic %d", c.TokenRequests(""), c.TokenRequests("none"), c.TokenRequests("basic"))
	}
	if c.WithStatus(401) != 1 || c.WithStatus(404) != 1 || c.WithStatus(200) != 5 {
		t.Errorf("statuses: 401 %d, 404 %d, 200 %d", c.WithStatus(401), c.WithStatus(404), c.WithStatus(200))
	}
	if c.Requests("GET", "/v2/library/alpine/manifests/3") != 2 || c.Requests("HEAD", "/v2/library/alpine/manifests/3") != 1 {
		t.Error("requests by method and path")
	}
	if c.RepositoryRequests("library/alpine") != 5 || c.OfKind(KindManifest, "") != 4 || c.OfKind(KindManifest, "HEAD") != 1 {
		t.Errorf("by repository %d, by kind %d", c.RepositoryRequests("library/alpine"), c.OfKind(KindManifest, ""))
	}
	if got := c.Hosts(); len(got) != 2 {
		t.Errorf("hosts = %v, want the registry and the realm", got)
	}
	if got := c.OnHost(hostPort(t, f.realm)); got != 1 {
		t.Errorf("realm host requests = %d", got)
	}
	if c.Total() != 7 || !strings.HasPrefix(c.Summary(), "7 requests") {
		t.Errorf("total %d, summary %q", c.Total(), c.Summary())
	}
	// A query is part of the entry: the pages of a list are told apart.
	for _, e := range c.Entries {
		if e.Kind == KindTags && e.Query != "n=2" {
			t.Errorf("tags entry query = %q", e.Query)
		}
	}
	// The credential itself is never kept, only its scheme.
	for _, e := range c.Entries {
		if strings.Contains(e.Auth, "eyJ") {
			t.Errorf("entry keeps a credential: %+v", e)
		}
	}
}

func TestCountsResetAndConditional(t *testing.T) {
	f := newFixture(t, Config{})
	f.open()
	f.manifestOf("library/alpine", "3")
	d, _ := f.stub.Digest("library/alpine", "3")
	f.get(f.reg+"/v2/library/alpine/manifests/3", map[string]string{"Accept": acceptAll, "If-None-Match": `"` + d + `"`})
	c := f.stub.Counts()
	conditional := 0
	for _, e := range c.Entries {
		if e.Conditional {
			conditional += e.Count
		}
	}
	if len(c.Entries) != 2 || conditional != 1 || c.WithStatus(304) != 1 {
		t.Errorf("entries %d, conditional %d, 304s %d", len(c.Entries), conditional, c.WithStatus(304))
	}
	f.stub.Reset()
	if f.stub.Counts().Total() != 0 {
		t.Error("Reset left counters")
	}
	if f.stub.Mode().Auth != AuthNone {
		t.Error("Reset cleared the mode")
	}
}

func TestViolationsAndBlobFetches(t *testing.T) {
	f := newFixture(t, Config{})
	f.open()
	layer := firstLayer(t, f.stub, "acme/a", "1.0")
	fetch := func() {
		loc := f.get(f.reg+"/v2/acme/a/blobs/"+layer, nil).header.Get("Location")
		f.get(loc, nil)
	}
	fetch()
	f.manifestOf("acme/a", "1.0")
	if v := f.stub.Counts().Violations(1, 1); len(v) != 0 {
		t.Fatalf("violations after one pull: %v", v)
	}
	fetch()
	f.manifestOf("acme/a", "1.0")
	v := f.stub.Counts().Violations(1, 1)
	if len(v) != 2 || !strings.Contains(v[0], "blob "+layer) && !strings.Contains(v[1], "blob "+layer) {
		t.Errorf("violations = %v, want the manifest and the blob", v)
	}
	if n := f.stub.Counts().BlobFetches(layer); n != 2 {
		t.Errorf("blob fetches = %d", n)
	}
	if v := f.stub.Counts().Violations(2, 2); len(v) != 0 {
		t.Errorf("violations at 2/2: %v", v)
	}
}

func TestCatalogAndReferrersCounted(t *testing.T) {
	f := newFixture(t, Config{})
	if err := f.stub.SetMode(Mode{Auth: AuthNone, Catalog: true, Referrers: true}); err != nil {
		t.Fatal(err)
	}
	d, _ := f.stub.Digest("acme/signed", "1.0")
	f.get(f.reg+"/v2/_catalog", nil)
	f.get(f.reg+"/v2/acme/signed/referrers/"+d, nil)
	f.get(f.reg+"/v2/acme/signed/referrers/"+d+"?artifactType=x", nil)
	c := f.stub.Counts()
	if c.Catalogs() != 1 || c.ReferrersRequests("acme/signed") != 2 || c.ReferrersRequests("") != 2 {
		t.Errorf("catalogs %d, referrers %d", c.Catalogs(), c.ReferrersRequests("acme/signed"))
	}
}

func TestMoveTag(t *testing.T) {
	f := newFixture(t, Config{})
	f.open()
	before, _ := f.stub.Digest("acme/a", "latest")
	other, _ := f.stub.Digest("acme/a", "1.0")
	infoBefore, _ := f.stub.Image("acme/a", "latest")

	after, err := f.stub.MoveTag("acme/a", "latest")
	if err != nil {
		t.Fatal(err)
	}
	if after == before {
		t.Fatal("the tag did not move")
	}
	if r := f.manifestOf("acme/a", "latest"); r.header.Get("Docker-Content-Digest") != after || sha(r.body) != after {
		t.Errorf("latest serves %s, want %s", r.header.Get("Docker-Content-Digest"), after)
	}
	// The earlier manifest and its blobs stay, as in a registry.
	if r := f.manifestOf("acme/a", before); r.status != 200 {
		t.Errorf("earlier manifest by digest = %d", r.status)
	}
	infoAfter, _ := f.stub.Image("acme/a", "latest")
	if infoAfter.Layers[1].Digest == infoBefore.Layers[1].Digest || infoAfter.Layers[0].Digest != infoBefore.Layers[0].Digest {
		t.Error("a new revision changes the application layer and keeps the shared base layer")
	}
	if r := f.get(f.reg+"/v2/acme/a/blobs/"+infoBefore.Layers[1].Digest, map[string]string{}); r.status != 307 {
		t.Errorf("earlier layer = %d", r.status)
	}
	if d, _ := f.stub.Digest("acme/a", "1.0"); d != other {
		t.Error("another tag moved")
	}
	// A second move gives a third manifest; the platforms of an index stay.
	if again, _ := f.stub.MoveTag("acme/a", "latest"); again == after || again == before {
		t.Errorf("second move = %s", again)
	}
	if _, err := f.stub.MoveTag("acme/b", "latest"); err != nil {
		t.Fatal(err)
	}
	idx, _ := f.stub.Image("acme/b", "latest")
	if len(idx.Manifests) != 4 {
		t.Errorf("moved index has %d manifests, want 4", len(idx.Manifests))
	}
	if _, ok := idx.ForPlatform("linux/arm64"); !ok {
		t.Error("moved index lost a platform")
	}
}

// Moves are deterministic: two stubs given the same moves serve the same digests.
func TestMoveTagIsDeterministic(t *testing.T) {
	a, _ := New(Config{})
	b, _ := New(Config{})
	da, _ := a.MoveTag("acme/a", "latest")
	db, _ := b.MoveTag("acme/a", "latest")
	if da != db {
		t.Errorf("two stubs moved the tag to %s and %s", da, db)
	}
}

func TestMoveTagErrors(t *testing.T) {
	s, _ := New(Config{})
	if _, err := s.MoveTag("nope/x", "1"); err == nil {
		t.Error("unknown repository")
	}
	if _, err := s.MoveTag("acme/a", "nope"); err == nil {
		t.Error("unknown tag")
	}
	d, _ := s.Digest("acme/signed", "1.0")
	if _, err := s.MoveTag("acme/signed", FallbackTag(d)); err == nil {
		t.Error("the fallback tag has no image recipe")
	}
}

func TestSetTagAndDeleteTag(t *testing.T) {
	f := newFixture(t, Config{})
	f.open()
	d1, _ := f.stub.Digest("acme/a", "1.0")
	if err := f.stub.SetTag("acme/a", "stable", d1); err != nil {
		t.Fatal(err)
	}
	if r := f.manifestOf("acme/a", "stable"); r.header.Get("Docker-Content-Digest") != d1 {
		t.Errorf("new tag serves %s", r.header.Get("Docker-Content-Digest"))
	}
	var doc struct{ Tags []string }
	f.get(f.reg+"/v2/acme/a/tags/list", nil).json(t, &doc)
	if !slices.Contains(doc.Tags, "stable") {
		t.Errorf("tags = %v", doc.Tags)
	}
	if err := f.stub.DeleteTag("acme/a", "stable"); err != nil {
		t.Fatal(err)
	}
	if r := f.manifestOf("acme/a", "stable"); r.status != 404 {
		t.Errorf("deleted tag = %d", r.status)
	}
	if r := f.manifestOf("acme/a", d1); r.status != 200 {
		t.Errorf("manifest of a deleted tag by digest = %d", r.status)
	}
	other, _ := f.stub.Digest("other/c", "1.0")
	if err := f.stub.SetTag("acme/a", "x", other); err == nil {
		t.Error("a tag was pointed at a manifest the repository does not hold")
	}
	if err := f.stub.SetTag("nope/x", "x", d1); err == nil {
		t.Error("unknown repository")
	}
	if err := f.stub.DeleteTag("acme/a", "stable"); err == nil {
		t.Error("deleting a missing tag")
	}
}

func TestResetAllRestoresTheCorpus(t *testing.T) {
	f := newFixture(t, Config{})
	f.open()
	before, _ := f.stub.Digest("acme/a", "latest")
	if _, err := f.stub.MoveTag("acme/a", "latest"); err != nil {
		t.Fatal(err)
	}
	f.stub.RevokeTokens()
	if err := f.stub.SetThrottle(Throttle{Mode: ThrottleHub}); err != nil {
		t.Fatal(err)
	}
	f.stub.ResetAll()
	if d, _ := f.stub.Digest("acme/a", "latest"); d != before {
		t.Error("ResetAll left the moved tag")
	}
	if f.stub.Mode().Auth != "" || f.stub.ThrottleState().Throttle.Mode != "" || f.stub.Counts().Total() != 0 {
		t.Error("ResetAll left a switch or a counter")
	}
	if r := f.get(f.reg+"/v2/", f.bearer()); r.status != 200 {
		t.Errorf("a token after ResetAll = %d", r.status)
	}
}

func TestServersDoNotShareState(t *testing.T) {
	shared := Default()
	a, _ := New(Config{Corpus: shared})
	b, _ := New(Config{Corpus: shared})
	if _, err := a.MoveTag("acme/a", "latest"); err != nil {
		t.Fatal(err)
	}
	da, _ := a.Digest("acme/a", "latest")
	db, _ := b.Digest("acme/a", "latest")
	ds, _ := shared.Digest("acme/a", "latest")
	if da == db || db != ds {
		t.Errorf("a %s, b %s, corpus %s: a move reached another server or the corpus", da, db, ds)
	}
}

// A request that is challenged and then repeated with the token is one manifest request, not two.
func TestChallengesAreNotManifestRequests(t *testing.T) {
	f := newFixture(t, Config{})
	f.get(f.reg+"/v2/library/alpine/manifests/3", map[string]string{"Accept": acceptAll}) // 401
	hdr := f.bearer("repository:library/alpine:pull")
	hdr["Accept"] = acceptAll
	f.get(f.reg+"/v2/library/alpine/manifests/3", hdr)
	f.do(http.MethodHead, f.reg+"/v2/library/alpine/manifests/3", hdr)
	c := f.stub.Counts()
	if c.ManifestGets("library/alpine", "3") != 1 || c.ManifestHeads("library/alpine", "3") != 1 {
		t.Errorf("GETs %d, HEADs %d, want 1 and 1", c.ManifestGets("library/alpine", "3"), c.ManifestHeads("library/alpine", "3"))
	}
	if v := c.Violations(1, 1); len(v) != 0 {
		t.Errorf("violations: %v", v)
	}
	if n := c.OfKind(KindManifest, "GET"); n != 2 {
		t.Errorf("the raw count of manifest GETs = %d, want 2 (the challenge is in it)", n)
	}
	if n := c.WithStatus(401); n != 1 {
		t.Errorf("401 answers = %d", n)
	}
}

// Docker Hub counts GET requests: the Hub throttle leaves a HEAD alone and uncounted.
func TestHubThrottleExemptsHead(t *testing.T) {
	f := newFixture(t, Config{})
	f.open()
	if err := f.stub.SetThrottle(Throttle{Mode: ThrottleHub, Class: ClassManifest}); err != nil {
		t.Fatal(err)
	}
	if r := f.do(http.MethodHead, f.reg+"/v2/library/alpine/manifests/3", map[string]string{"Accept": acceptAll}); r.status != 200 {
		t.Errorf("HEAD under the Hub throttle = %d", r.status)
	}
	if r := f.manifestOf("library/alpine", "3"); r.status != 429 {
		t.Errorf("GET under the Hub throttle = %d", r.status)
	}
	if st := f.stub.ThrottleState(); st.Seen != 1 || st.Throttled != 1 {
		t.Errorf("state = %+v", st)
	}
}

// Two tags that share an image move to different images; a tag moved twice never repeats.
func TestMoveTagSeedsByTag(t *testing.T) {
	s, _ := New(Config{})
	a, _ := s.MoveTag("library/alpine", "3")
	b, _ := s.MoveTag("library/alpine", "latest")
	if a == b {
		t.Error("two tags of one image moved to the same image")
	}
	a2, _ := s.MoveTag("library/alpine", "3")
	if a2 == a {
		t.Error("a tag moved twice came back to its first move")
	}
	other, _ := New(Config{})
	if again, _ := other.MoveTag("library/alpine", "3"); again != a {
		t.Error("the same move on another stub gives another digest")
	}
}

// Changing a mode value after SetMode, or the value Mode returned, does not change the stub.
func TestModeIsCopied(t *testing.T) {
	s, _ := New(Config{})
	m := Mode{FailStatus: map[string]int{ClassBlob: 502}, NotFound: []string{"acme/a"}, PullQuota: &PullQuota{Limit: 2}}
	if err := s.SetMode(m); err != nil {
		t.Fatal(err)
	}
	m.FailStatus[ClassBlob] = 500
	m.NotFound[0] = "acme/b"
	m.PullQuota.Limit = 9
	got := s.Mode()
	got.FailStatus[ClassTags] = 500
	got.NotFound[0] = "x"
	got.PullQuota.Limit = 7
	now := s.Mode()
	if now.FailStatus[ClassBlob] != 502 || len(now.FailStatus) != 1 || now.NotFound[0] != "acme/a" || now.PullQuota.Limit != 2 {
		t.Errorf("mode changed through a copy: %+v", now)
	}
}
