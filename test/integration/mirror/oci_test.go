//go:build integration

package mirror

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/mirror"
	"github.com/guided-traffic/trove/test/integration/testenv"
	"github.com/guided-traffic/trove/test/ocistub"
)

// An OCI remote in mode mirror against the counting registry of test/ocistub (docs/spec/formats/oci.md "Mirror discovery", R-OCI-026,
// R-REPO-051, R-REPO-052, R-REPO-054): the replicas are the real stack with the real format and host; the stub runs in the test
// process on TLS under the three loopback names (registry, realm, blob host), which the replicas reach through
// app.Options.DialOverride. Every pass runs on one of two worker processes that claim the queue mirror, and the clients pull through
// a third, client-serving process.

const (
	ociManifestTypes = "application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, " +
		"application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json"
	hubRegistry, hubRealm, hubCDN = "registry-1.docker.io", "auth.docker.io", "production.cloudfront.docker.com"
)

type ociOptions struct {
	include []string
	mirror  settings
	// oci is the member oci.mirror of the repository document.
	oci map[string]any
	// mode is the stub's switches.
	mode ocistub.Mode
	// hub makes the stub answer under the names of Docker Hub and the repository point at registry-1.docker.io.
	hub bool
}

// ociRig is a rig whose upstream is the OCI stub.
type ociRig struct {
	*rig
	stub *ocistub.Server
	run  *ocistub.Running
}

func newOCIRig(t *testing.T, o ociOptions) *ociRig {
	t.Helper()
	hosts := append([]string(nil), ocistub.LoopbackHosts...)
	cfg := ocistub.Config{}
	if o.hub {
		hosts = append(hosts, hubRegistry, hubRealm, hubCDN)
		cfg = ocistub.Config{PublicURL: "https://" + hubRegistry, RealmURL: "https://" + hubRealm, CDNURL: "https://" + hubCDN}
	}
	ss, err := ocistub.NewSelfSigned(hosts...)
	if err != nil {
		t.Fatal(err)
	}
	stub, err := ocistub.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := stub.SetMode(o.mode); err != nil {
		t.Fatal(err)
	}
	run, err := stub.Start("127.0.0.1:0", ss.ServerConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = run.Close() })

	realm, cdn, upstream := ocistub.LoopbackRealmHost, ocistub.LoopbackCDNHost, run.URL
	if o.hub {
		realm, cdn, upstream = hubRealm, hubCDN, "https://"+hubRegistry+"/"
	}
	extra := []any{
		map[string]any{"pattern": realm, "sendCredentials": false},
		map[string]any{"pattern": cdn, "sendCredentials": false},
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(remoteDocAt(upstream, "mirror", o.include, o.mirror, map[string]any{"extraHosts": extra})), &doc); err != nil {
		t.Fatal(err)
	}
	if o.oci != nil {
		doc["oci"] = map[string]any{"mirror": o.oci}
	}
	raw, _ := json.Marshal(doc)

	r := &rig{t: t, format: "oci", traf: &traffic{}, url: run.URL, upstream: upstream, dial: run.DialOverride()}
	ca := testenv.WriteFile(t, t.TempDir(), "stub-ca.pem", string(ss.CAPEM))
	st := testenv.NewStack(t, testenv.StackOptions{Name: "mirrorit", Env: append([]string{
		"TROVE_PROXY_OUTBOUND_TLS_CA_FILES=[" + strconv.Quote(filepath.ToSlash(ca)) + "]",
		`TROVE_PROXY_OUTBOUND_ADDRESS_POLICY_ALLOW_CIDRS=["127.0.0.1/32"]`,
		"TROVE_PROXY_LOCK_POLL_INTERVAL_MS=50",
		"TROVE_VULNERABILITIES_SOURCES_OSV_MODE=off",
		"TROVE_API_RATE_LIMITS_ENABLED=false",
	}, fastJobs...)})
	st.Repository(repoKey, "remote", "oci", string(raw))
	st.Exec(`INSERT INTO mirror_state (repo_id, next_pass_at) SELECT id, now() + interval '1 hour' FROM repositories WHERE key = $1`, repoKey)
	reader, rita := st.User("reader"), st.User("rita")
	st.Bind("admin", rita)
	st.Policy("readers", []string{"read"}, []string{repoKey}, reader)
	r.reader = st.Token(reader, scope.Default())
	r.admin = st.Token(rita, scope.Scope{Inherit: true, Admin: true})

	db, err := postgres.Open(context.Background(), st.DatabaseConfig(), "mirror-it", "web", testenv.Quiet())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	r.env = &env{t: t, st: st, db: db, store: mirror.NewPGStore(db)}
	r.repoN = st.Number(`SELECT id FROM repositories WHERE key = $1`, repoKey)
	r.dumpOnFailure()
	return &ociRig{rig: r, stub: stub, run: run}
}

// workers starts the two processes that claim the queue mirror and the web process; the pass may run on either worker.
func (r *ociRig) workers() (first, second, web *testenv.Replica) {
	return r.runner("runner-1"), r.runner("runner-2"), r.web("web")
}

// paths lists the items of the repository below a prefix, sorted.
func (r *ociRig) paths(prefix string) []string {
	var out []string
	r.st.Query(`SELECT i.path FROM items i JOIN repositories r ON r.id = i.repo_id WHERE r.key = $1 AND i.kind = 'file' AND i.path LIKE $2`, func(rows pgx.Rows) error {
		var p string
		if err := rows.Scan(&p); err != nil {
			return err
		}
		out = append(out, p)
		return nil
	}, repoKey, prefix+"%")
	sort.Strings(out)
	return out
}

// tagEntries counts the stored tag documents of an image.
func (r *ociRig) tagEntries(name, tag string) int64 {
	return r.st.Number(`SELECT count(*) FROM remote_cache_entries c JOIN repositories r ON r.id = c.repo_id WHERE r.key = $1 AND c.path = $2`, repoKey, name+"/_tags/"+tag)
}

// blobsStored reports whether the repository holds every blob of the image, and how many of them it holds.
func (r *ociRig) blobsStored(name string, img ocistub.ImageInfo) (held, total int) {
	for _, d := range img.Blobs() {
		total++
		if slices.Contains(r.paths(name+"/_blobs/"), name+"/_blobs/"+d) {
			held++
		}
	}
	return held, total
}

func (r *ociRig) image(name, ref string) ocistub.ImageInfo {
	r.t.Helper()
	img, err := r.stub.Image(name, ref)
	if err != nil {
		r.t.Fatal(err)
	}
	return img
}

// do sends a request as a container client does: a registry token for the image first, then the request.
func (r *ociRig) do(rep *testenv.Replica, method, name, rest string) testenv.Answer {
	r.t.Helper()
	q := url.Values{"scope": {"repository:" + testenv.DefaultProject + "/" + repoKey + "/" + name + ":pull"}}
	a := r.st.Do(rep, http.MethodGet, "/v2/_token?"+q.Encode(), r.reader, nil)
	var tok struct {
		Token string `json:"token"`
	}
	if a.Status != http.StatusOK || json.Unmarshal(a.Body, &tok) != nil || tok.Token == "" {
		r.t.Fatalf("registry token for %s: %d %s", name, a.Status, a.Body)
	}
	return r.st.Do(rep, method, "/v2/"+testenv.DefaultProject+"/"+repoKey+"/"+name+"/"+rest, tok.Token, nil, "Accept", ociManifestTypes)
}

// pull is what a client does for a tag: the manifest, the platform manifests of an index and every blob; it fails the test on any
// answer but 200.
func (r *ociRig) pull(rep *testenv.Replica, name, ref string) {
	r.t.Helper()
	a := r.do(rep, http.MethodGet, name, "manifests/"+ref)
	if a.Status != http.StatusOK {
		r.t.Fatalf("GET %s:%s: %d %.200s", name, ref, a.Status, a.Body)
	}
	var m struct {
		Config    *struct{ Digest string }
		Layers    []struct{ Digest string }
		Manifests []struct{ Digest string }
	}
	if err := json.Unmarshal(a.Body, &m); err != nil {
		r.t.Fatalf("%s:%s: %v", name, ref, err)
	}
	for _, c := range m.Manifests {
		r.pull(rep, name, c.Digest)
	}
	var blobs []string
	if m.Config != nil {
		blobs = append(blobs, m.Config.Digest)
	}
	for _, l := range m.Layers {
		blobs = append(blobs, l.Digest)
	}
	for _, d := range blobs {
		if b := r.do(rep, http.MethodGet, name, "blobs/"+d); b.Status != http.StatusOK {
			r.t.Fatalf("GET blob %s of %s: %d", d, name, b.Status)
		}
	}
}

func (r *ociRig) summaryIs(method, kind string) {
	r.t.Helper()
	if st := r.summary(); st["method"] != method || st["kind"] != kind {
		r.t.Errorf("summary %v, want the method %s and a %s pass", st, method, kind)
	}
}

// R-OCI-026, R-REPO-051: a registry with a catalog is listed, the include patterns and the tag patterns select, the newest
// maxVersionsPerPackage wildcard matches are kept besides the exact tag, nothing outside the patterns is asked for, and a pull after
// the pass works with the upstream gone.
func TestR_OCI_026_ACatalogPassStoresTheSelectedTagsWithTheirBlobsAndSurvivesTheUpstream(t *testing.T) {
	r := newOCIRig(t, ociOptions{
		include: []string{"acme/*"}, mirror: settings{"maxVersionsPerPackage": 2},
		oci:  map[string]any{"tags": []string{"1.*", "latest"}},
		mode: ocistub.Mode{Catalog: true},
	})
	one, two, web := r.workers()
	t0 := r.pass(one)
	r.waitPass(t0, "full", "complete", passWithin)
	_ = two

	t.Run("R-OCI-026 the pass lists the catalog and stores 1.2, 1.1 and latest of acme/a with their blobs", func(t *testing.T) {
		r.summaryIs("listing", "full")
		for _, tag := range []string{"1.2", "1.1", "latest"} {
			if r.tagEntries("acme/a", tag) != 1 {
				t.Errorf("the tag %s of acme/a is not stored", tag)
			}
			if held, total := r.blobsStored("acme/a", r.image("acme/a", tag)); held != total || total == 0 {
				t.Errorf("acme/a:%s: %d of %d blobs stored", tag, held, total)
			}
		}
		for _, tag := range []string{"1.0", "2.0"} {
			if r.tagEntries("acme/a", tag) != 0 {
				t.Errorf("the tag %s of acme/a is stored", tag)
			}
		}
	})

	t.Run("R-OCI-026 the stub saw no request for other/c and no manifest request for 1.0 or 2.0 of acme/a", func(t *testing.T) {
		c := r.stub.Counts()
		if n := c.RepositoryRequests("other/c"); n != 0 {
			t.Errorf("%d requests for other/c", n)
		}
		for _, tag := range []string{"1.0", "2.0"} {
			if n := c.ManifestGets("acme/a", tag) + c.ManifestHeads("acme/a", tag); n != 0 {
				t.Errorf("%d manifest requests for acme/a:%s", n, tag)
			}
		}
		if c.Catalogs() < 1 {
			t.Error("the catalog was not read")
		}
	})

	t.Run("R-OCI-026 the images of acme/b are mirrored with the same tag patterns", func(t *testing.T) {
		if r.tagEntries("acme/b", "1.0") != 1 || r.tagEntries("acme/b", "latest") != 1 {
			t.Error("acme/b is not stored")
		}
	})

	t.Run("R-OCI-026 with the stub stopped, a mirrored tag pulls and an unmirrored one does not", func(t *testing.T) {
		if err := r.run.Close(); err != nil {
			t.Fatal(err)
		}
		r.pull(web, "acme/a", "1.2")
		r.pull(web, "acme/a", "latest")
		r.pull(web, "acme/b", "1.0")
		if a := r.do(web, http.MethodGet, "acme/a", "manifests/2.0"); a.Status == http.StatusOK {
			t.Errorf("acme/a:2.0 answered 200 with the upstream gone")
		}
	})
}

// R-OCI-026: a registry without a catalog is mirrored by the exact include patterns.
func TestR_OCI_026_AnExactNamePassStoresTheSameWithoutACatalog(t *testing.T) {
	r := newOCIRig(t, ociOptions{
		include: []string{"acme/a"}, mirror: settings{"maxVersionsPerPackage": 2},
		oci: map[string]any{"tags": []string{"1.*", "latest"}},
	})
	one, _, _ := r.workers()
	t0 := r.pass(one)
	r.waitPass(t0, "full", "complete", passWithin)

	r.summaryIs("names", "full")
	// The pass asks once whether the registry has a catalog: the request that is challenged and the one that carries the token.
	if got := r.stub.Counts().Catalogs(); got > 2 {
		t.Errorf("%d catalog requests: the pass asks once whether the registry has one", got)
	}
	for _, tag := range []string{"1.2", "1.1", "latest"} {
		if r.tagEntries("acme/a", tag) != 1 {
			t.Errorf("the tag %s of acme/a is not stored", tag)
		}
	}
	if r.tagEntries("acme/a", "1.0") != 0 || r.tagEntries("acme/a", "2.0") != 0 {
		t.Error("a tag outside the bound is stored")
	}
	if n := r.stub.Counts().RepositoryRequests("acme/b") + r.stub.Counts().RepositoryRequests("other/c"); n != 0 {
		t.Errorf("%d requests for names no pattern names", n)
	}
}

// R-OCI-026: oci.mirror.platforms keeps the index, the selected manifest, its attestation and their blobs, and nothing of the platforms
// left out; linux/arm64 matches the variant v8 of the stub's index.
func TestR_OCI_026_APlatformFilterKeepsTheArm64ManifestWithItsAttestationOnly(t *testing.T) {
	r := newOCIRig(t, ociOptions{
		include: []string{"acme/b"}, oci: map[string]any{"tags": []string{"1.0"}, "platforms": []string{"linux/arm64"}},
	})
	one, _, web := r.workers()
	t0 := r.pass(one)
	r.waitPass(t0, "full", "complete", passWithin)

	index := r.image("acme/b", "1.0")
	arm, ok := index.ForPlatform("linux/arm64")
	if !ok {
		t.Fatal("the stub's index has no arm64 manifest")
	}
	amd, _ := index.ForPlatform("linux/amd64")
	var attestation ocistub.ImageInfo
	for _, m := range index.Manifests {
		if m.AttestationFor == arm.Digest {
			attestation = m
		}
	}
	if attestation.Digest == "" {
		t.Fatal("no attestation of the arm64 manifest")
	}

	t.Run("R-OCI-026 the index, the arm64 manifest, its attestation and their blobs are stored", func(t *testing.T) {
		if r.tagEntries("acme/b", "1.0") != 1 {
			t.Error("the index is not stored")
		}
		manifests := r.paths("acme/b/_manifests/")
		for _, d := range []string{index.Digest, arm.Digest, attestation.Digest} {
			if !slices.Contains(manifests, "acme/b/_manifests/"+d) {
				t.Errorf("manifest %s is not stored: %v", d, manifests)
			}
		}
		for _, img := range []ocistub.ImageInfo{arm, attestation} {
			if held, total := r.blobsStored("acme/b", img); held != total || total == 0 {
				t.Errorf("%s: %d of %d blobs stored", img.Digest, held, total)
			}
		}
	})

	t.Run("R-OCI-026 nothing of the amd64 manifest is stored or asked for", func(t *testing.T) {
		have := map[string]bool{}
		for _, p := range r.paths("acme/b/") {
			have[p] = true
		}
		kept := map[string]bool{}
		for _, d := range append(arm.Blobs(), attestation.Blobs()...) {
			kept[d] = true
		}
		for _, d := range amd.Blobs() {
			if !kept[d] && have["acme/b/_blobs/"+d] {
				t.Errorf("the amd64 blob %s is stored", d)
			}
		}
		if have["acme/b/_manifests/"+amd.Digest] {
			t.Error("the amd64 manifest is stored")
		}
		if n := r.stub.Counts().ManifestGets("acme/b", amd.Digest); n != 0 {
			t.Errorf("%d manifest requests for the amd64 manifest", n)
		}
	})

	t.Run("R-OCI-026 a client pulls the amd64 manifest through the remote all the same", func(t *testing.T) {
		if a := r.do(web, http.MethodGet, "acme/b", "manifests/"+amd.Digest); a.Status != http.StatusOK {
			t.Errorf("GET the amd64 manifest: %d", a.Status)
		}
	})
}

// R-OCI-026: a second pass with nothing changed costs one HEAD per kept tag and no manifest GET; a tag the upstream moved costs one
// manifest GET and its new blobs, and the earlier manifest stays.
func TestR_OCI_026_ASecondPassCostsAHeadAndAMovedTagCostsAGet(t *testing.T) {
	r := newOCIRig(t, ociOptions{include: []string{"acme/a"}, oci: map[string]any{"tags": []string{"latest", "1.0"}}})
	one, _, _ := r.workers()
	t0 := r.pass(one)
	r.waitPass(t0, "full", "complete", passWithin)
	first := r.image("acme/a", "latest")
	if held, total := r.blobsStored("acme/a", first); held != total {
		t.Fatalf("first pass: %d of %d blobs", held, total)
	}

	t.Run("R-OCI-026 nothing changed: the tags list and one HEAD per kept tag, no manifest GET", func(t *testing.T) {
		r.stub.Reset()
		r.waitPass(r.pass(one), "full", "complete", passWithin)
		c := r.stub.Counts()
		if c.ManifestGets("acme/a", "") != 0 {
			t.Errorf("%d manifest GET requests: %s", c.ManifestGets("acme/a", ""), c.Summary())
		}
		if c.ManifestHeads("acme/a", "latest") != 1 || c.ManifestHeads("acme/a", "1.0") != 1 {
			t.Errorf("HEAD requests: latest %d, 1.0 %d: %s", c.ManifestHeads("acme/a", "latest"), c.ManifestHeads("acme/a", "1.0"), c.Summary())
		}
		if c.TagLists("acme/a") < 1 {
			t.Error("the tags list was not read")
		}
		if n := c.BlobFetches(first.Layers[0].Digest); n != 0 {
			t.Errorf("a stored layer was fetched %d times", n)
		}
	})

	t.Run("R-OCI-026 latest moved: one manifest GET, the new blobs are stored and the old manifest stays", func(t *testing.T) {
		if _, err := r.stub.MoveTag("acme/a", "latest"); err != nil {
			t.Fatal(err)
		}
		r.stub.Reset()
		r.waitPass(r.pass(one), "full", "complete", passWithin)
		moved := r.image("acme/a", "latest")
		if moved.Digest == first.Digest {
			t.Fatal("the stub did not move the tag")
		}
		c := r.stub.Counts()
		if n := c.ManifestGets("acme/a", "latest"); n != 1 {
			t.Errorf("%d manifest GET requests for the moved tag, want 1: %s", n, c.Summary())
		}
		if n := c.ManifestGets("acme/a", "1.0"); n != 0 {
			t.Errorf("%d manifest GET requests for the tag that did not move", n)
		}
		if held, total := r.blobsStored("acme/a", moved); held != total {
			t.Errorf("the moved tag: %d of %d blobs stored", held, total)
		}
		if !slices.Contains(r.paths("acme/a/_manifests/"), "acme/a/_manifests/"+first.Digest) {
			t.Error("the earlier manifest was removed: the mirror deletes nothing")
		}
	})
}

// R-OCI-026, R-REPO-052: with maxBytes below the image, the pass ends bound_reached and a client's pull of a layer the pass did not
// store still succeeds by pull-through. The bound is written into the document directly, as the service refuses less than 1 MiB.
func TestR_OCI_026_TheByteBoundEndsThePassAndAPullStillWorksByPullThrough(t *testing.T) {
	r := newOCIRig(t, ociOptions{include: []string{"acme/a"}, mirror: settings{"maxBytes": 8000}, oci: map[string]any{"tags": []string{"latest"}}})
	one, _, web := r.workers()
	t0 := r.pass(one)
	r.waitPass(t0, "full", "bound_reached", passWithin)

	img := r.image("acme/a", "latest")
	held, total := r.blobsStored("acme/a", img)
	if held >= total {
		t.Fatalf("all %d blobs are stored under a bound of 8000 bytes", total)
	}
	if n := r.number(`SELECT count(*) FROM mirror_pending WHERE repo_id = $1`, r.repoN); n != 1 {
		t.Errorf("%d names pending, want acme/a", n)
	}
	r.pull(web, "acme/a", "latest")
	if held2, _ := r.blobsStored("acme/a", img); held2 != total {
		t.Errorf("the pull stored %d of %d blobs", held2, total)
	}
}

// R-OCI-025, R-OCI-026, R-REPO-054: a Docker Hub mirror takes `nginx` as library/nginx; the pull quota's answer (429 with
// RateLimit-Remaining 0;w=21600 and no Retry-After) is counted as quota_header and the pass goes on after the wait.
func TestR_OCI_025_ADockerHubQuotaAnswerIsAQuotaHeaderRateLimitOfThePass(t *testing.T) {
	r := newOCIRig(t, ociOptions{hub: true, include: []string{"nginx"}, oci: map[string]any{"tags": []string{"1", "latest"}}})
	if err := r.stub.SetThrottle(ocistub.Throttle{Mode: ocistub.ThrottleHub, Class: ocistub.ClassManifest, Nth: 1, Count: 1}); err != nil {
		t.Fatal(err)
	}
	one, _, _ := r.workers()
	t0 := r.pass(one)
	r.waitPass(t0, "full", "complete", 3*passWithin)

	t.Run("R-OCI-025 the answer is counted with the reason quota_header", func(t *testing.T) {
		got := r.metric(one, "trove_mirror_rate_limited_total", "reason", "quota_header")
		if two := r.metric(r.st.Replicas()[1], "trove_mirror_rate_limited_total", "reason", "quota_header"); got+two < 1 {
			t.Errorf("trove_mirror_rate_limited_total{reason=quota_header} = %v and %v", got, two)
		}
	})

	t.Run("R-OCI-026 the pass stores library/nginx under its Docker Hub name once the limit lifts", func(t *testing.T) {
		for _, tag := range []string{"1", "latest"} {
			if r.tagEntries("library/nginx", tag) != 1 {
				t.Errorf("library/nginx:%s is not stored", tag)
			}
		}
		if got := r.paths("nginx/"); len(got) != 0 {
			t.Errorf("items below the unmapped name: %v", got)
		}
		if held, total := r.blobsStored("library/nginx", r.image("library/nginx", "latest")); held != total || total == 0 {
			t.Errorf("%d of %d blobs stored", held, total)
		}
	})
}
