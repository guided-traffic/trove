//go:build integration

package oci

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// TestProtocol runs the acceptance criteria of docs/spec/formats/oci.md that name the protocol against three replicas of one
// installation (PostgreSQL, Valkey with Sentinel, S3), every request pinned to a replica by the test. The installation is
// built once, so the criteria are subtests whose names start with the requirement ID they prove; a criterion that needs a real
// client (Buildx, Podman, Kubernetes), the conformance suite or the restore drill is not here and is named in
// docs/projektplan/oci-work-packages.md ("WP13 as built").
func TestProtocol(t *testing.T) {
	p := newProto(t, protoOptions{replicas: 3, setup: pathPeople})
	for _, c := range []struct {
		name string
		run  func(*testing.T, *proto)
	}{
		{"R-OCI-002_names_tags_and_digests_that_are_none_are_refused", names},
		{"R-OCI-003_the_version_check_and_the_paths_that_reach_no_core_route", versionCheck},
		{"R-OCI-004_the_token_service_and_the_direct_credential", tokenService},
		{"R-OCI-006_overwrite_and_delete_are_actions_of_their_own", grants},
		{"R-OCI-007_one_object_for_a_tag_and_its_manifest_and_for_a_layer_of_two_names", storedOnce},
		{"R-OCI-009_the_headers_of_a_pull_and_the_media_type_served", pullHeaders},
		{"R-OCI-010_a_range_and_a_blob_of_another_name", blobPulls},
		{"R-OCI-011_a_chunked_upload_over_three_replicas_and_the_416_form", uploads},
		{"R-OCI-012_a_mount_by_the_grants_of_its_source", mounts},
		{"R-OCI-013_push_refusals_artifacts_and_concurrent_tags", pushes},
		{"R-OCI-013_indexes_record_platforms_and_an_overwritten_tag_leaves_the_index", indexes},
		{"R-OCI-013_a_push_by_digest_makes_no_version_and_a_later_tag_links_to_it", digestPush},
		{"R-OCI-013_a_tag_push_and_a_digest_push_of_one_manifest_both_end_201", tagAndDigestPush},
		{"R-OCI-015_a_tags_list_pages_with_n_and_a_link", tagPaging},
		{"R-OCI-016_concurrent_signatures_are_listed_and_filtered", referrers},
		{"R-OCI-017_the_catalog_lists_what_the_caller_may_read", catalog},
		{"R-OCI-018_deleting_an_index_and_the_refusal_to_delete_a_child", deletes},
		{"R-OCI-020_crafted_manifests_are_refused_before_a_blob_is_looked_up", crafted},
		{"R-OCI-033_a_withheld_manifest_is_refused_on_the_pull_and_the_push", withheld},
		{"R-AUTHZ-005_path_patterns_decide_the_protocol_routes_like_the_files_routes", pathPatterns},
	} {
		t.Run(c.name, func(t *testing.T) { c.run(t, p) })
	}
}

func hdr(a testenv.Answer, name string) string { return a.Header.Get(name) }

// do is a request of a person, on the next replica.
func (p *proto) as1(who, method, path string, body []byte, h ...string) testenv.Answer {
	p.t.Helper()
	return p.do(call{rep: p.next(), method: method, path: path, cred: p.as(who), body: body, hdr: h})
}

func (p *proto) count(sql string, args ...any) int64 {
	p.t.Helper()
	return p.st.Number(sql, args...)
}

func (p *proto) items(r, like string) int64 {
	p.t.Helper()
	return p.count(`SELECT count(*) FROM items i JOIN repositories r ON r.id = i.repo_id WHERE r.key = $1 AND i.path LIKE $2 AND i.trashed_at IS NULL`, r, like)
}

// names: the grammars of a name, a tag and a digest answer their own codes through /v2/.
func names(t *testing.T, p *proto) {
	img := newImage("names")
	want(t, p.as1("admin", "PUT", repo(repoLocal, "App")+"/manifests/1", img.manifest, "Content-Type", manifestType), 400, "NAME_INVALID")
	long := strings.Repeat("a", 256-len(project+"/"+repoLocal+"/"))
	want(t, p.as1("admin", "PUT", repo(repoLocal, long)+"/manifests/1", img.manifest, "Content-Type", manifestType), 400, "NAME_INVALID")
	want(t, p.as1("admin", "PUT", repo(repoLocal, "app")+"/manifests/-x", img.manifest, "Content-Type", manifestType), 400, "TAG_INVALID")
	want(t, p.as1("admin", "GET", repo(repoLocal, "app")+"/blobs/sha512:"+strings.Repeat("a", 128), nil), 400, "DIGEST_INVALID")
	want(t, p.as1("admin", "GET", repo(repoLocal, "app")+"/blobs/sha256:"+strings.Repeat("a", 63), nil), 400, "DIGEST_INVALID")
}

// versionCheck: R-OCI-003.
func versionCheck(t *testing.T, p *proto) {
	for rep := range p.reps {
		a := p.do(call{rep: rep, method: "GET", path: "/v2/"})
		want(t, a, 401, "UNAUTHORIZED")
		if c := hdr(a, "Www-Authenticate"); !strings.HasPrefix(c, `Bearer realm="http://trove.test/v2/_token",service="trove.test"`) {
			t.Fatalf("challenge %q", c)
		}
		if hdr(a, "Docker-Distribution-Api-Version") != "registry/2.0" {
			t.Fatalf("headers %v", a.Header)
		}
		ok := p.do(call{rep: rep, method: "GET", path: "/v2/", cred: p.as("reader")})
		if ok.Status != 200 || string(ok.Body) != "{}" {
			t.Fatalf("version check with a credential: %d %s", ok.Status, ok.Body)
		}
	}
	for _, path := range []string{"/v2/%5Fcatalog", "/v2/_catalog/"} {
		want(t, p.do(call{rep: p.next(), method: "GET", path: path, cred: p.as("admin")}), 404, "NAME_UNKNOWN")
	}
}

// tokenService: R-OCI-004.
func tokenService(t *testing.T, p *proto) {
	sc := scopeOf(repoLocal, "app", "pull,push")
	if g := grantOf(t, p.registryToken(0, "pusher", sc)); g[project+"/"+repoLocal+"/app"] != pull|push {
		t.Fatalf("a caller with read and deploy: grant %v", g)
	}
	if g := grantOf(t, p.registryToken(1, "reader", sc)); g[project+"/"+repoLocal+"/app"] != pull {
		t.Fatalf("a caller with read only: grant %v", g)
	}
	// A revoked trove token is refused by the token service.
	revoked := p.st.Token(p.st.User("revoked"), scope.Default())
	p.st.Exec(`UPDATE api_token SET revoked_at = now() WHERE secret_sha256 = decode(encode(sha256($1::bytea), 'hex'), 'hex')`, revoked)
	p.troveToken["revoked"] = revoked
	if a := p.tokenAnswer(2, "revoked", sc); a.Status != 401 {
		t.Fatalf("a revoked token: %d %s", a.Status, a.Body)
	}
	// The POST form of the token service.
	form := "grant_type=password&username=x&password=" + p.troveToken["pusher"] + "&service=trove.test&scope=" + scopeOf(repoLocal, "app", "pull")
	a := p.do(call{rep: 1, method: "POST", path: "/v2/_token", body: []byte(form), hdr: []string{"Content-Type", "application/x-www-form-urlencoded"}})
	var body struct {
		AccessToken string `json:"access_token"`
	}
	if a.Status != 200 || json.Unmarshal(a.Body, &body) != nil || body.AccessToken == "" {
		t.Fatalf("POST token request: %d %s", a.Status, a.Body)
	}
	// A direct credential is answered without a token request.
	img := newImage("direct")
	want(t, p.pushImage(p.as("pusher"), repoLocal, "direct", "1", img), 201, "")
	want(t, p.as1("reader", "GET", repo(repoLocal, "direct")+"/manifests/1", nil), 200, "")
	// A token granted pull only, used for an upload start: 401 with insufficient_scope and the scope that is missing.
	pullOnly := p.registryToken(0, "pusher", scopeOf(repoLocal, "direct", "pull"))
	up := p.do(call{rep: 2, method: "POST", path: repo(repoLocal, "direct") + "/blobs/uploads/", cred: bearer(pullOnly)})
	c := hdr(up, "Www-Authenticate")
	if up.Status != 401 || !strings.Contains(c, `error="insufficient_scope"`) || !strings.Contains(c, `scope="repository:`+project+"/"+repoLocal+`/direct:push"`) {
		t.Fatalf("pull-only token on an upload: %d %q", up.Status, c)
	}
	// Anonymous: a push without a login fails with unauthorized.
	anon := p.do(call{rep: 0, method: "POST", path: repo(repoLocal, "direct") + "/blobs/uploads/"})
	want(t, anon, 401, "UNAUTHORIZED")
}

// grants: R-OCI-006.
func grants(t *testing.T, p *proto) {
	a, b := newImage("one"), newImage("two")
	want(t, p.pushImage(p.as("pusher"), repoLocal, "g/app", "1", a), 201, "")
	want(t, p.pushImage(p.as("pusher"), repoLocal, "g/app", "1", a), 201, "")
	for _, blob := range append([][]byte{b.config}, b.layers...) {
		want(t, p.uploadBlob(p.next(), p.as("pusher"), repoLocal, "g/app", blob), 201, "")
	}
	want(t, p.putManifest(p.next(), p.as("pusher"), repoLocal, "g/app", "1", manifestType, b.manifest), 403, "DENIED")
	if got := p.as1("reader", "GET", repo(repoLocal, "g/app")+"/manifests/1", nil); hdr(got, "Docker-Content-Digest") != a.digest() {
		t.Fatalf("the tag moved without overwrite")
	}
	want(t, p.as1("pusher", "DELETE", repo(repoLocal, "g/app")+"/manifests/1", nil), 403, "DENIED")
	// With overwrite the tag moves; the old manifest stays pullable by digest.
	want(t, p.pushImage(p.as("mover"), repoLocal, "g/app", "1", b), 201, "")
	want(t, p.as1("reader", "GET", repo(repoLocal, "g/app")+"/manifests/"+a.digest(), nil), 200, "")
	if n := p.count(`SELECT count(*) FROM package_versions pv JOIN packages k ON k.id = pv.package_id WHERE k.name = 'g/app' AND pv.trashed_at IS NULL`); n != 1 {
		t.Fatalf("%d versions for one tag", n)
	}
	// A caller without a grant gets nothing from the repository.
	if got := p.as1("nobody", "PUT", repo(repoLocal, "g/app")+"/manifests/9", a.manifest, "Content-Type", manifestType); got.Status != 403 && got.Status != 404 {
		t.Fatalf("a caller without a grant: %d %s", got.Status, got.Body)
	}
}

// storedOnce: R-OCI-007.
func storedOnce(t *testing.T, p *proto) {
	img := newImage("once", []byte("shared layer bytes"))
	want(t, p.pushImage(p.as("pusher"), repoLocal, "once/app", "1", img), 201, "")
	hex := strings.TrimPrefix(img.digest(), "sha256:")
	tag := p.as1("reader", "GET", repo(repoLocal, "once/app")+"/manifests/1", nil)
	if digestOf(tag.Body) != img.digest() || hdr(tag, "Docker-Content-Digest") != img.digest() {
		t.Fatalf("the tag serves bytes of %s", digestOf(tag.Body))
	}
	if n := p.count(`SELECT count(DISTINCT blob_sha256) FROM items i JOIN repositories r ON r.id = i.repo_id WHERE r.key = $1 AND i.path IN ('once/app/_tags/1', $2)`,
		repoLocal, "once/app/_manifests/"+img.digest()); n != 1 {
		t.Fatalf("the tag and the manifest hold %d objects, want one (%s)", n, hex)
	}
	before := p.count(`SELECT count(*) FROM blobs`)
	want(t, p.pushImage(p.as("pusher"), repoOther, "once/other", "1", img), 201, "")
	if after := p.count(`SELECT count(*) FROM blobs`); after != before {
		t.Fatalf("the same image pushed under a second name and repository grew the blobs by %d", after-before)
	}
}

// pullHeaders: R-OCI-009.
func pullHeaders(t *testing.T, p *proto) {
	img := newImage("headers")
	want(t, p.pushImage(p.as("pusher"), repoLocal, "hdr/app", "1", img), 201, "")
	tok := p.registryToken(0, "reader", scopeOf(repoLocal, "hdr/app", "pull"))
	head := p.do(call{rep: p.next(), method: "HEAD", path: repo(repoLocal, "hdr/app") + "/manifests/1", cred: bearer(tok)})
	if head.Status != 200 || len(head.Body) != 0 || hdr(head, "Docker-Content-Digest") != img.digest() ||
		hdr(head, "Content-Length") != fmt.Sprint(len(img.manifest)) || hdr(head, "Content-Type") != manifestType {
		t.Fatalf("HEAD: %d %v", head.Status, head.Header)
	}
	get := p.do(call{rep: p.next(), method: "GET", path: repo(repoLocal, "hdr/app") + "/manifests/1", cred: bearer(tok)})
	if digestOf(get.Body) != hdr(head, "Docker-Content-Digest") {
		t.Fatalf("the GET bytes are %s", digestOf(get.Body))
	}
	// A manifest pushed as a Docker schema 2 manifest is served with that type to an Accept that lists only the OCI type.
	cfg := []byte(`{"architecture":"amd64","os":"linux","docker":true}`)
	layer := []byte("docker layer")
	dm, _ := json.Marshal(map[string]any{
		"schemaVersion": 2, "mediaType": dockerType,
		"config": map[string]any{"mediaType": "application/vnd.docker.container.image.v1+json", "digest": digestOf(cfg), "size": len(cfg)},
		"layers": []any{map[string]any{"mediaType": "application/vnd.docker.image.rootfs.diff.tar.gzip", "digest": digestOf(layer), "size": len(layer)}},
	})
	for _, b := range [][]byte{cfg, layer} {
		want(t, p.uploadBlob(p.next(), p.as("pusher"), repoLocal, "hdr/docker", b), 201, "")
	}
	want(t, p.putManifest(p.next(), p.as("pusher"), repoLocal, "hdr/docker", "1", dockerType, dm), 201, "")
	got := p.as1("reader", "GET", repo(repoLocal, "hdr/docker")+"/manifests/1", nil, "Accept", manifestType)
	if got.Status != 200 || hdr(got, "Content-Type") != dockerType {
		t.Fatalf("a Docker manifest asked with an OCI Accept: %d %v", got.Status, got.Header)
	}
}

// blobPulls: R-OCI-010.
func blobPulls(t *testing.T, p *proto) {
	big := bytes.Repeat([]byte("0123456789"), 40)
	img := newImage("range", big)
	want(t, p.pushImage(p.as("pusher"), repoLocal, "rng/app", "1", img), 201, "")
	d := digestOf(big)
	part := p.as1("reader", "GET", repo(repoLocal, "rng/app")+"/blobs/"+d, nil, "Range", "bytes=0-99")
	if part.Status != 206 || len(part.Body) != 100 {
		t.Fatalf("range: %d with %d bytes", part.Status, len(part.Body))
	}
	want(t, p.as1("reader", "GET", repo(repoLocal, "rng/other")+"/blobs/"+d, nil), 404, "BLOB_UNKNOWN")
}

// chunk is one request of a chunked upload.
func (p *proto) startUpload(rep int, c cred, r, name string) string {
	p.t.Helper()
	a := p.do(call{rep: rep, method: "POST", path: repo(r, name) + "/blobs/uploads/", cred: c})
	want(p.t, a, 202, "")
	loc := hdr(a, "Location")
	return strings.TrimPrefix(loc, "http://"+testenv.DefaultHost)
}

// uploads: R-OCI-011.
func uploads(t *testing.T, p *proto) {
	data := bytes.Repeat([]byte("chunked payload "), 1000)
	third := len(data) / 3
	cuts := []int{0, third, 2 * third, len(data)}
	loc := p.startUpload(0, p.as("pusher"), repoLocal, "up/app")
	for i := 0; i < 3; i++ {
		part := data[cuts[i]:cuts[i+1]]
		a := p.do(call{rep: (i + 1) % 3, method: "PATCH", path: loc, cred: p.as("pusher"), body: part, hdr: []string{
			"Content-Type", octet, "Content-Range", fmt.Sprintf("%d-%d", cuts[i], cuts[i+1]-1),
		}})
		want(t, a, 202, "")
		loc = strings.TrimPrefix(hdr(a, "Location"), "http://"+testenv.DefaultHost)
	}
	sep := "?"
	if strings.Contains(loc, "?") {
		sep = "&"
	}
	done := p.do(call{rep: 2, method: "PUT", path: loc + sep + "digest=" + digestOf(data), cred: p.as("pusher")})
	want(t, done, 201, "")
	got := p.as1("reader", "GET", repo(repoLocal, "up/app")+"/blobs/"+digestOf(data), nil)
	if got.Status != 200 || digestOf(got.Body) != digestOf(data) {
		t.Fatalf("the blob: %d", got.Status)
	}
	// A PATCH with Content-Range 10-19 on a session that received nothing: 416 with Range 0-0.
	fresh := p.startUpload(1, p.as("pusher"), repoLocal, "up/app")
	a := p.do(call{rep: 2, method: "PATCH", path: fresh, cred: p.as("pusher"), body: []byte("0123456789"), hdr: []string{"Content-Type", octet, "Content-Range", "10-19"}})
	want(t, a, 416, "BLOB_UPLOAD_INVALID")
	if hdr(a, "Range") != "0-0" {
		t.Fatalf("Range %q, want 0-0", hdr(a, "Range"))
	}
}

// mounts: R-OCI-012 over three replicas.
func mounts(t *testing.T, p *proto) {
	base := []byte("base layer for the mount")
	want(t, p.uploadBlob(0, p.as("pusher"), repoOther, "mnt/base", base), 201, "")
	d := digestOf(base)
	target := repo(repoLocal, "mnt/app")
	mount := func(who, from string) testenv.Answer {
		return p.as1(who, "POST", target+"/blobs/uploads/?mount="+d+"&from="+from, nil)
	}
	objects := p.count(`SELECT count(*) FROM blobs`)
	sessions := p.count(`SELECT count(*) FROM upload_sessions`)
	got := mount("pusher", project+"/"+repoOther+"/mnt/base")
	want(t, got, 201, "")
	for rep := range p.reps {
		if a := p.do(call{rep: rep, method: "GET", path: target + "/blobs/" + d, cred: p.as("reader")}); a.Status != 200 {
			t.Fatalf("the mounted blob on replica %d: %d", rep, a.Status)
		}
	}
	if p.count(`SELECT count(*) FROM blobs`) != objects || p.count(`SELECT count(*) FROM upload_sessions`) != sessions {
		t.Fatal("a mount stored an object or opened a session")
	}
	// Refused mounts answer the same 202 and link nothing.
	other := repo(repoLocal, "mnt/second")
	for name, a := range map[string]testenv.Answer{
		"without read on the source":                p.as1("deployonly", "POST", other+"/blobs/uploads/?mount="+d+"&from="+project+"/"+repoOther+"/mnt/base", nil),
		"without from":                              p.as1("pusher", "POST", other+"/blobs/uploads/?mount="+d, nil),
		"from a repository that does not exist":     p.as1("pusher", "POST", other+"/blobs/uploads/?mount="+d+"&from="+project+"/nope/mnt/base", nil),
		"from a name that lacks the blob":           p.as1("pusher", "POST", other+"/blobs/uploads/?mount="+d+"&from="+project+"/"+repoOther+"/mnt/empty", nil),
		"from a repository the caller may not read": p.as1("pusher", "POST", other+"/blobs/uploads/?mount="+d+"&from="+project+"/"+repoSecret+"/mnt/base", nil),
	} {
		if a.Status != 202 {
			t.Fatalf("%s: %d %s", name, a.Status, a.Body)
		}
	}
	if n := p.items(repoLocal, "mnt/second/_blobs/%"); n != 0 {
		t.Fatalf("refused mounts stored %d items", n)
	}
}

// pushes: R-OCI-013.
func pushes(t *testing.T, p *proto) {
	pusher := p.as("pusher")
	img := newImage("pushes")
	for _, b := range append([][]byte{img.config}, img.layers...) {
		want(t, p.uploadBlob(p.next(), pusher, repoLocal, "psh/app", b), 201, "")
	}
	tags := 0
	put := func(body []byte, ct string) testenv.Answer {
		tags++
		return p.putManifest(p.next(), pusher, repoLocal, "psh/app", fmt.Sprint("t", tags), ct, body, "Expect", "100-continue")
	}
	want(t, put(img.manifest, "application/vnd.docker.distribution.manifest.v1+prettyjws"), 400, "MANIFEST_INVALID")
	want(t, put(img.manifest, indexType), 400, "MANIFEST_INVALID")
	// The other type's field: a document whose mediaType says index, sent as a manifest.
	asIndex := bytes.Replace(img.manifest, []byte(manifestType), []byte(indexType), 1)
	want(t, put(asIndex, manifestType), 400, "MANIFEST_INVALID")
	// Size bounds: 4 MiB + 1 is refused, 4 MiB is accepted (padding after the document is whitespace).
	pad := func(n int) []byte {
		return append(append([]byte{}, img.manifest...), bytes.Repeat([]byte(" "), n-len(img.manifest))...)
	}
	want(t, put(pad(4<<20+1), manifestType), 413, "SIZE_INVALID")
	want(t, put(pad(4<<20), manifestType), 201, "")
	// A layer that was never uploaded; the same layer with urls and the foreign media type is accepted.
	ghost := []byte("never uploaded")
	missing := newImage("missing", ghost)
	want(t, put(missing.manifest, manifestType), 400, "MANIFEST_BLOB_UNKNOWN")
	foreign := fmt.Sprintf(`{"schemaVersion":2,"mediaType":%q,"config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":%q,"size":%d},`+
		`"layers":[{"mediaType":"application/vnd.docker.image.rootfs.foreign.diff.tar.gzip","digest":%q,"size":%d,"urls":["https://example.test/layer"]}]}`,
		manifestType, digestOf(img.config), len(img.config), digestOf(ghost), len(ghost))
	want(t, put([]byte(foreign), manifestType), 201, "")
	// An artifact manifest with an artifactType and the empty config is accepted and pulled back byte for byte.
	empty := []byte("{}")
	want(t, p.uploadBlob(p.next(), pusher, repoLocal, "psh/app", empty), 201, "")
	art := fmt.Appendf(nil, `{"schemaVersion":2,"mediaType":%q,"artifactType":"application/vnd.example+json","config":{"mediaType":"application/vnd.oci.empty.v1+json","digest":%q,"size":2},`+
		`"layers":[{"mediaType":"application/vnd.oci.empty.v1+json","digest":%q,"size":2}]}`, manifestType, digestOf(empty), digestOf(empty))
	want(t, p.putManifest(p.next(), pusher, repoLocal, "psh/app", "art", manifestType, art), 201, "")
	if got := p.as1("reader", "GET", repo(repoLocal, "psh/app")+"/manifests/art", nil); !bytes.Equal(got.Body, art) {
		t.Fatalf("the artifact came back as %q", got.Body)
	}
	// Two concurrent pushes of different manifests as one tag, from three replicas: one digest, one version row naming it.
	for round := range 3 {
		tag := fmt.Sprint("race", round)
		var wg sync.WaitGroup
		statuses := make([]int, 6)
		for i := range statuses {
			m := newImage(fmt.Sprint("race", round, i))
			for _, b := range append([][]byte{m.config}, m.layers...) {
				want(t, p.uploadBlob(p.next(), p.as("mover"), repoLocal, "psh/app", b), 201, "")
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				statuses[i] = p.putManifest(i%3, p.as("mover"), repoLocal, "psh/app", tag, manifestType, m.manifest).Status
			}()
		}
		wg.Wait()
		for _, s := range statuses {
			if s != 201 {
				t.Fatalf("round %d: statuses %v, want every push to end 201", round, statuses)
			}
		}
		got := p.as1("reader", "GET", repo(repoLocal, "psh/app")+"/manifests/"+tag, nil)
		d := hdr(got, "Docker-Content-Digest")
		if v := p.count(`SELECT count(*) FROM package_versions pv JOIN packages k ON k.id = pv.package_id JOIN items i ON i.id = pv.lead_item_id
			WHERE k.name = 'psh/app' AND pv.version = $1 AND pv.trashed_at IS NULL AND i.blob_sha256 = decode($2, 'hex')`, tag, strings.TrimPrefix(d, "sha256:")); v != 1 {
			t.Fatalf("round %d: %d version rows name the digest the tag serves", round, v)
		}
	}
}

// indexes: an index is pushed after its children, records the platforms and refuses a missing child.
func indexes(t *testing.T, p *proto) {
	pusher := p.as("pusher")
	var kids []image
	for _, arch := range []string{"amd64", "arm64"} {
		img := newImage("multi-" + arch)
		want(t, p.pushImage(pusher, repoLocal, "idx/multi", img.digest(), img), 201, "")
		kids = append(kids, img)
	}
	idx := fmt.Appendf(nil, `{"schemaVersion":2,"mediaType":%q,"manifests":[`+
		`{"mediaType":%q,"digest":%q,"size":%d,"platform":{"os":"linux","architecture":"amd64"}},`+
		`{"mediaType":%q,"digest":%q,"size":%d,"platform":{"os":"linux","architecture":"arm64","variant":"v8"}}]}`,
		indexType, manifestType, kids[0].digest(), len(kids[0].manifest), manifestType, kids[1].digest(), len(kids[1].manifest))
	want(t, p.putManifest(p.next(), pusher, repoLocal, "idx/multi", "1", indexType, idx), 201, "")
	got := p.st.Text(`SELECT string_agg(platform, ',' ORDER BY platform) FROM oci_references WHERE name = 'idx/multi' AND kind = 'manifest'`)
	if got != "linux/amd64,linux/arm64/v8" {
		t.Fatalf("platforms = %q", got)
	}
	bad := bytes.Replace(idx, []byte(kids[0].digest()), []byte(digestOf([]byte("never pushed"))), 1)
	want(t, p.putManifest(p.next(), pusher, repoLocal, "idx/multi", "2", indexType, bad), 400, "MANIFEST_BLOB_UNKNOWN")
	// Moving the tag to a single manifest leaves the index pullable by digest.
	single := newImage("single")
	want(t, p.pushImage(p.as("mover"), repoLocal, "idx/multi", "1", single), 201, "")
	got2 := p.as1("reader", "GET", repo(repoLocal, "idx/multi")+"/manifests/"+digestOf(idx), nil)
	if got2.Status != 200 || !bytes.Equal(got2.Body, idx) || hdr(got2, "Content-Type") != indexType {
		t.Fatalf("the old index: %d %v", got2.Status, got2.Header)
	}
}

// tagAndDigestPush: a push by tag and a push by digest of one new manifest cross their locks; the database breaks the cycle
// and every push ends 201.
func tagAndDigestPush(t *testing.T, p *proto) {
	for round := range 4 {
		m := newImage(fmt.Sprint("cross", round))
		for _, b := range append([][]byte{m.config}, m.layers...) {
			want(t, p.uploadBlob(p.next(), p.as("mover"), repoLocal, "cross/app", b), 201, "")
		}
		var wg sync.WaitGroup
		statuses := make([]int, 4)
		for i, ref := range []string{fmt.Sprint("t", round), m.digest(), fmt.Sprint("u", round), m.digest()} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				statuses[i] = p.putManifest(i%3, p.as("mover"), repoLocal, "cross/app", ref, manifestType, m.manifest).Status
			}()
		}
		wg.Wait()
		for _, s := range statuses {
			if s != 201 {
				t.Fatalf("round %d: statuses %v", round, statuses)
			}
		}
		want(t, p.as1("reader", "GET", repo(repoLocal, "cross/app")+"/manifests/"+m.digest(), nil), 200, "")
	}
}

// tagPaging: R-OCI-015.
func tagPaging(t *testing.T, p *proto) {
	img := newImage("tags")
	want(t, p.pushImage(p.as("pusher"), repoLocal, "tg/app", "a", img), 201, "")
	for _, tag := range []string{"b", "c"} {
		want(t, p.putManifest(p.next(), p.as("pusher"), repoLocal, "tg/app", tag, manifestType, img.manifest), 201, "")
	}
	first := p.as1("reader", "GET", repo(repoLocal, "tg/app")+"/tags/list?n=2", nil)
	var page struct {
		Tags []string `json:"tags"`
	}
	if first.Status != 200 || json.Unmarshal(first.Body, &page) != nil || strings.Join(page.Tags, ",") != "a,b" {
		t.Fatalf("first page: %d %s", first.Status, first.Body)
	}
	link := hdr(first, "Link")
	if !strings.Contains(link, "last=b") || !strings.Contains(link, `rel="next"`) {
		t.Fatalf("Link %q", link)
	}
	next := strings.TrimSuffix(strings.TrimPrefix(strings.SplitN(link, ";", 2)[0], "<"), ">")
	second := p.as1("reader", "GET", next, nil)
	page.Tags = nil
	if second.Status != 200 || json.Unmarshal(second.Body, &page) != nil || strings.Join(page.Tags, ",") != "c" || hdr(second, "Link") != "" {
		t.Fatalf("second page: %d %s %v", second.Status, second.Body, second.Header)
	}
}

// referrers: R-OCI-016.
func referrers(t *testing.T, p *proto) {
	pusher := p.as("pusher")
	subject := newImage("subject")
	want(t, p.pushImage(pusher, repoLocal, "ref/app", "1", subject), 201, "")
	empty := []byte("{}")
	want(t, p.uploadBlob(p.next(), pusher, repoLocal, "ref/app", empty), 201, "")
	const sig, sbom = "application/vnd.dev.cosign.artifact.sig.v1+json", "application/spdx+json"
	var wg sync.WaitGroup
	for i, at := range []string{sig, sig, sbom} {
		art := fmt.Appendf(nil, `{"schemaVersion":2,"mediaType":%q,"artifactType":%q,"config":{"mediaType":"application/vnd.oci.empty.v1+json","digest":%q,"size":2},`+
			`"layers":[{"mediaType":"application/vnd.oci.empty.v1+json","digest":%q,"size":2}],"subject":{"mediaType":%q,"digest":%q,"size":%d},"annotations":{"n":"%d"}}`,
			manifestType, at, digestOf(empty), digestOf(empty), manifestType, subject.digest(), len(subject.manifest), i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if a := p.putManifest(i%3, pusher, repoLocal, "ref/app", digestOf(art), manifestType, art); a.Status != 201 {
				t.Errorf("referrer %d: %d %s", i, a.Status, a.Body)
			}
		}()
	}
	wg.Wait()
	list := func(q string) (n int, a testenv.Answer) {
		a = p.as1("reader", "GET", repo(repoLocal, "ref/app")+"/referrers/"+subject.digest()+q, nil)
		var idx struct {
			Manifests []struct{} `json:"manifests"`
		}
		if a.Status != 200 || json.Unmarshal(a.Body, &idx) != nil {
			t.Fatalf("referrers: %d %s", a.Status, a.Body)
		}
		return len(idx.Manifests), a
	}
	if n, _ := list(""); n != 3 {
		t.Fatalf("%d referrers, want the three concurrent pushes", n)
	}
	n, a := list("?artifactType=" + url.QueryEscape(sig))
	if n != 2 || hdr(a, "OCI-Filters-Applied") != "artifactType" {
		t.Fatalf("filtered: %d referrers, header %q", n, hdr(a, "OCI-Filters-Applied"))
	}
}

// catalog: R-OCI-017.
func catalog(t *testing.T, p *proto) {
	img := newImage("catalog")
	for _, n := range []string{"team/a", "team/b", "other/x"} {
		want(t, p.pushImage(p.as("pusher"), repoLocal, n, "1", img), 201, "")
	}
	want(t, p.pushImage(p.as("admin"), repoSecret, "hidden", "1", img), 201, "")
	list := func(c cred, q string) (names []string, a testenv.Answer) {
		a = p.do(call{rep: p.next(), method: "GET", path: "/v2/_catalog" + q, cred: c})
		var b struct {
			Repositories []string `json:"repositories"`
		}
		if a.Status != 200 || json.Unmarshal(a.Body, &b) != nil {
			t.Fatalf("catalog: %d %s", a.Status, a.Body)
		}
		return b.Repositories, a
	}
	tok := p.registryToken(0, "reader", "registry:catalog:*")
	all, _ := list(bearer(tok), "")
	for _, n := range all {
		if strings.Contains(n, repoSecret) {
			t.Fatalf("the catalog names %s to a caller without read on it", n)
		}
	}
	if !contains(all, project+"/"+repoLocal+"/team/a") {
		t.Fatalf("the catalog lacks the images of the repository the caller reads: %v", all)
	}
	// A token scope with a package pattern lists team/* and nothing else of the repository.
	names, a := list(p.as("catscope"), "?n=1")
	if len(names) != 1 || !strings.HasPrefix(names[0], project+"/"+repoLocal+"/team/") || !strings.Contains(hdr(a, "Link"), "last=") {
		t.Fatalf("scoped catalog page: %v %q", names, hdr(a, "Link"))
	}
	var seen []string
	for q := "?n=1"; ; {
		names, a = list(p.as("catscope"), q)
		seen = append(seen, names...)
		l := hdr(a, "Link")
		if l == "" {
			break
		}
		q = "?" + strings.SplitN(strings.TrimSuffix(strings.TrimPrefix(strings.SplitN(l, ";", 2)[0], "<"), ">"), "?", 2)[1]
	}
	if strings.Join(seen, ",") != project+"/"+repoLocal+"/team/a,"+project+"/"+repoLocal+"/team/b" {
		t.Fatalf("the scoped catalog, followed to its end: %v", seen)
	}
	// A caller without a credential reads what a public level admits and nothing else (R-AUTHZ-003, R-AUTHZ-033): no repository of
	// the installation is public, so its catalog is the empty list R-OCI-017 names, and no name of a repository leaks.
	if anon, a := list(cred{}, ""); len(anon) != 0 || strings.Contains(string(a.Body), repoLocal) || strings.Contains(string(a.Body), repoSecret) {
		t.Fatalf("the catalog without a credential: %v %s", anon, a.Body)
	}
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// deletes: R-OCI-018.
func deletes(t *testing.T, p *proto) {
	pusher := p.as("pusher")
	var kids []image
	for _, arch := range []string{"amd64", "arm64"} {
		img := newImage("del-" + arch)
		want(t, p.pushImage(p.as("pusher"), repoLocal, "del/app", img.digest(), img), 201, "")
		kids = append(kids, img)
	}
	mk := func(children ...image) []byte {
		var ms []string
		for _, c := range children {
			ms = append(ms, fmt.Sprintf(`{"mediaType":%q,"digest":%q,"size":%d,"platform":{"os":"linux","architecture":"amd64"}}`, manifestType, c.digest(), len(c.manifest)))
		}
		return fmt.Appendf(nil, `{"schemaVersion":2,"mediaType":%q,"manifests":[%s]}`, indexType, strings.Join(ms, ","))
	}
	both, onlyFirst := mk(kids...), mk(kids[0])
	want(t, p.putManifest(p.next(), pusher, repoLocal, "del/app", "multi", indexType, both), 201, "")
	want(t, p.putManifest(p.next(), pusher, repoLocal, "del/app", "single", indexType, onlyFirst), 201, "")
	// A child while an index lists it: 409 DENIED.
	want(t, p.as1("deleter", "DELETE", repo(repoLocal, "del/app")+"/manifests/"+kids[1].digest(), nil), 409, "DENIED")
	// The index by digest takes the index and the child no other index lists; the shared child stays.
	want(t, p.as1("deleter", "DELETE", repo(repoLocal, "del/app")+"/manifests/"+digestOf(both), nil), 202, "")
	want(t, p.as1("reader", "GET", repo(repoLocal, "del/app")+"/manifests/"+digestOf(both), nil), 404, "MANIFEST_UNKNOWN")
	want(t, p.as1("reader", "GET", repo(repoLocal, "del/app")+"/manifests/"+kids[1].digest(), nil), 404, "MANIFEST_UNKNOWN")
	want(t, p.as1("reader", "GET", repo(repoLocal, "del/app")+"/manifests/"+kids[0].digest(), nil), 200, "")
}

// crafted: R-OCI-020.
func crafted(t *testing.T, p *proto) {
	pusher := p.as("pusher")
	cfg := []byte("{}")
	want(t, p.uploadBlob(p.next(), pusher, repoLocal, "crf/app", cfg), 201, "")
	unknown := "sha256:" + strings.Repeat("ab", 32)
	layer := `{"mediaType":"application/vnd.oci.image.layer.v1.tar","digest":"` + unknown + `","size":2}`
	cd := digestOf(cfg)
	for _, tc := range []struct {
		name string
		body string
		ct   string
	}{
		{"nested 100,000 deep", `{"schemaVersion":2,"x":` + strings.Repeat("[", 100_000) + strings.Repeat("]", 100_000) + `}`, manifestType},
		{"20,000 layers", `{"schemaVersion":2,"config":{"mediaType":"a/b","digest":"` + cd + `","size":2},"layers":[` + strings.TrimSuffix(strings.Repeat(layer+",", 20_000), ",") + `]}`, manifestType},
		{"a 5 MiB index", `{"schemaVersion":2,"manifests":[],"annotations":{"k":"` + strings.Repeat("a", 5<<20) + `"}}`, indexType},
		{"a descriptor digest that climbs", `{"schemaVersion":2,"config":{"mediaType":"a/b","digest":"sha256:../../x","size":2},"layers":[]}`, manifestType},
	} {
		var before, peak uint64
		var m runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&m)
		before = m.HeapInuse
		stop := make(chan struct{})
		done := make(chan struct{})
		go func() {
			defer close(done)
			for {
				select {
				case <-stop:
					return
				case <-time.After(10 * time.Millisecond):
					var s runtime.MemStats
					runtime.ReadMemStats(&s)
					peak = max(peak, s.HeapInuse)
				}
			}
		}()
		a := p.putManifest(p.next(), pusher, repoLocal, "crf/app", "x", tc.ct, []byte(tc.body), "Expect", "100-continue")
		close(stop)
		<-done
		if a.Status != 400 && a.Status != 413 {
			t.Fatalf("%s: %d %s", tc.name, a.Status, a.Body)
		}
		if c := errorCode(a); c == "MANIFEST_BLOB_UNKNOWN" {
			t.Fatalf("%s: refused only after the referenced blob was looked up", tc.name)
		}
		// The replicas run in this process, so the heap in use is their memory: it stays within 512 MiB of the baseline.
		if peak > before && peak-before > 512<<20 {
			t.Fatalf("%s: the heap grew by %d MiB", tc.name, (peak-before)>>20)
		}
	}
	if n := p.items(repoLocal, "crf/app/_tags/%") + p.items(repoLocal, "crf/app/_manifests/%"); n != 0 {
		t.Fatalf("crafted manifests left %d items", n)
	}
}

// withheld: R-OCI-033, the part of the second criterion a protocol client sees.
func withheld(t *testing.T, p *proto) {
	img := newImage("withheld")
	want(t, p.pushImage(p.as("pusher"), repoLocal, "wh/app", "1", img), 201, "")
	want(t, p.as1("reader", "GET", repo(repoLocal, "wh/app")+"/manifests/1", nil), 200, "")
	p.st.Exec(`INSERT INTO oci_tombstones (repo_id, name, digest) SELECT id, 'wh/app', decode($1, 'hex') FROM repositories WHERE key = $2`, strings.TrimPrefix(img.digest(), "sha256:"), repoLocal)
	want(t, p.as1("reader", "GET", repo(repoLocal, "wh/app")+"/manifests/1", nil), 404, "MANIFEST_UNKNOWN")
	want(t, p.as1("reader", "GET", repo(repoLocal, "wh/app")+"/manifests/"+img.digest(), nil), 404, "MANIFEST_UNKNOWN")
	want(t, p.putManifest(p.next(), p.as("pusher"), repoLocal, "wh/app", "2", manifestType, img.manifest), 409, "DENIED")
	want(t, p.putManifest(p.next(), p.as("pusher"), repoLocal, "wh/app", img.digest(), manifestType, img.manifest), 409, "DENIED")
	// The same bytes under another name are another manifest.
	want(t, p.pushImage(p.as("pusher"), repoLocal, "wh/other", "1", img), 201, "")
}

// withDigest adds the digest parameter that finishes an upload to the session's location.
func withDigest(loc, digest string) string {
	if strings.Contains(loc, "?") {
		return loc + "&digest=" + digest
	}
	return loc + "?digest=" + digest
}

// digestPush: a manifest pushed by digest makes no version; a digest that is not the manifest's is refused; the tag pushed later
// links to the stored manifest (one manifest item) and makes the version.
func digestPush(t *testing.T, p *proto) {
	pusher := p.as("pusher")
	img := newImage("by-digest")
	for _, b := range append([][]byte{img.config}, img.layers...) {
		want(t, p.uploadBlob(p.next(), pusher, repoLocal, "dg/app", b), 201, "")
	}
	want(t, p.putManifest(p.next(), pusher, repoLocal, "dg/app", img.digest(), manifestType, img.manifest), 201, "")
	versions := func() int64 {
		return p.count(`SELECT count(*) FROM package_versions pv JOIN packages k ON k.id = pv.package_id WHERE k.name = 'dg/app' AND pv.trashed_at IS NULL`)
	}
	if n := versions(); n != 0 {
		t.Fatalf("a push by digest made %d versions", n)
	}
	want(t, p.putManifest(p.next(), pusher, repoLocal, "dg/app", "sha256:"+strings.Repeat("0", 64), manifestType, img.manifest), 400, "DIGEST_INVALID")
	want(t, p.putManifest(p.next(), pusher, repoLocal, "dg/app", "latest", manifestType, img.manifest), 201, "")
	if n := p.items(repoLocal, "dg/app/_manifests/%"); n != 1 {
		t.Fatalf("%d manifest items after the tag was linked, want one", n)
	}
	if n := versions(); n != 1 {
		t.Fatalf("%d versions after the tag push, want one", n)
	}
	if got := p.as1("reader", "GET", repo(repoLocal, "dg/app")+"/manifests/latest", nil); got.Status != 200 || hdr(got, "Docker-Content-Digest") != img.digest() {
		t.Fatalf("pull of the tag: %d %v", got.Status, got.Header)
	}
}
