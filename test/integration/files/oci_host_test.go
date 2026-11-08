//go:build integration

package files

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/api/dispatch"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/formathost"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/oci"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/packages"
	"github.com/guided-traffic/trove/internal/qname"
	"github.com/guided-traffic/trove/internal/repository"

	ocidata "github.com/guided-traffic/trove/internal/formatdata/oci"
)

// The manifest and blob routes of the OCI format on the production host: the real dispatcher, the real item store over PostgreSQL and
// S3, the real storage hooks and package writer, and the data port over the same database. The registry root (`/v2/`) is a work package
// of its own, so the dispatcher here mounts the format at /api/oci/<project>/<key>/ through a wrapper that changes the one fact of the
// descriptor that decides where it is mounted; nothing else of the format differs from the one the registry serves. Ownership and the
// namespace auto claim are stood in for by seams that allow, because their rules are tested with the core (R-AUTHZ-018, R-REPO-023).

// apiOCI is the format `oci` mounted below /api/oci/ instead of the registry root.
type apiOCI struct{ *oci.Format }

func (a apiOCI) Descriptor() formats.Descriptor {
	d := a.Format.Descriptor()
	d.RegistryRoot = false
	d.ProtocolBasePath = "/api/oci/{project}/{key}/"
	return d
}

type allowOwnership struct{}

func (allowOwnership) EnforceTx(context.Context, meta.Tx, formathost.OwnershipCheck) error {
	return nil
}
func (allowOwnership) Precheck(context.Context, formathost.OwnershipCheck) error { return nil }

type noAutoClaim struct{}

func (noAutoClaim) AutoClaimTx(context.Context, meta.Tx, repository.Repository, string, repository.Caller) (repository.AutoClaimResult, error) {
	return repository.AutoClaimResult{}, nil
}

// proceedResolver lets the shadowing check of a first push pass: no virtual repository exists here.
type proceedResolver struct{ formats.Resolver }

func (proceedResolver) ShadowCheck(context.Context, *formats.Request, string) (formats.ShadowOutcome, error) {
	return formats.ShadowOutcome{Proceed: true}, nil
}

type ociFixture struct {
	e    *env
	r    *replica
	host *formathost.Host
}

const (
	ociCT      = "application/vnd.oci.image.manifest.v1+json"
	ociIndexCT = "application/vnd.oci.image.index.v1+json"
)

func newOCIFixture(t *testing.T) *ociFixture {
	t.Helper()
	e := newEnv(t)
	e.breakGlass()
	b := formathost.NewBindings()
	f := &ociFixture{e: e}
	// The cascade of a removal hook removes through the item store of the replica, as the composition root wires it.
	deps := formathost.Deps{Bindings: b, Ownership: allowOwnership{}, AutoClaim: noAutoClaim{}, Remover: func() formathost.ItemRemover {
		if f.r != nil && f.r.items != nil {
			return f.r.items
		}
		return nil
	}}
	seam := formathost.NewFilesSeam(func() (formats.Host, error) { return f.host, nil }, deps)
	f.r = e.newReplica("a", replicaOpts{
		hooks: formathost.NewHooks(deps), formats: seam,
		routes: func(r *replica, router *httpserver.Router) {
			store, err := ocidata.New(ocidata.Options{DB: r.db, Recorder: r.rec})
			if err != nil {
				t.Fatal(err)
			}
			if err := b.Bind("oci", store); err != nil {
				t.Fatal(err)
			}
			pkgs, err := packages.NewStore(r.db)
			if err != nil {
				t.Fatal(err)
			}
			if f.host, err = formathost.New(formathost.Options{
				Runner: r.db, Decider: r.engine, Items: r.items, Packages: pkgs, Repos: r.reader, Deps: deps, Resolver: proceedResolver{},
			}); err != nil {
				t.Fatal(err)
			}
			disp, err := dispatch.New(dispatch.Options{
				Host: f.host, Decider: r.engine, Repos: r.reader, Formats: func() []formats.Format { return []formats.Format{apiOCI{oci.New()}} },
			})
			if err != nil {
				t.Fatal(err)
			}
			disp.Register(router)
		},
	})
	f.r.createRepository("oci-local", map[string]any{"format": "oci"})
	e.user("writer")
	e.user("mover")
	e.user("reader")
	e.user("nobody")
	e.user("scoped")
	e.user("teamonly")
	e.grant(f.r, "oci-write", []string{"oci-local"}, authz.PatternLists{}, map[string][]string{
		"user:writer": {"read", "deploy"}, "user:mover": {"read", "deploy", "overwrite"}, "user:reader": {"read"},
	})
	e.grant(f.r, "oci-scoped", []string{"oci-local"}, authz.PatternLists{Exclude: []string{"secret/**"}}, map[string][]string{"user:scoped": {"read", "deploy"}})
	e.grant(f.r, "oci-team", []string{"oci-local"}, authz.PatternLists{Include: []string{"team/**"}}, map[string][]string{"user:teamonly": {"read", "deploy"}})
	// A caller without the action is refused 403 before the body is looked at, one with it gets as far as the content type.
	waitFor(t, "the policies", 10*time.Second, func() bool {
		for who, name := range map[string]string{"writer": "probe", "mover": "probe", "scoped": "probe", "teamonly": "team/probe"} {
			if f.push(who, name, "x", nil, "text/plain").status != http.StatusBadRequest {
				return false
			}
		}
		return f.r.get("reader", "/files/it/oci-local/").status == http.StatusOK
	})
	return f
}

func shaOf(b []byte) string {
	s := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(s[:])
}

// blob stores a blob item of the name through the item store, as the finalize of an upload does.
func (f *ociFixture) blob(name string, data []byte) string {
	f.e.t.Helper()
	ctx := context.Background()
	repo, err := f.r.reader.ByName(ctx, qname.Repo{Project: "it", Key: "oci-local"})
	if err != nil {
		f.e.t.Fatal(err)
	}
	sp, err := f.r.items.Spool(ctx, bytes.NewReader(data), items.IngestOptions{ContentLength: int64(len(data))})
	if err != nil {
		f.e.t.Fatal(err)
	}
	defer sp.Close()
	d := shaOf(data)
	if _, err := f.r.items.Commit(ctx, items.CommitSpec{
		Repo: repo, Path: repository.Cleaned{Path: oci.BlobPath(name, d)}, Spooled: sp, MediaType: "application/octet-stream", Origin: items.OriginUpload,
	}); err != nil {
		f.e.t.Fatalf("blob %s: %v", d, err)
	}
	return d
}

func ociManifest(cfg string, layers ...string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, `{"schemaVersion":2,"mediaType":%q,"config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":%q,"size":2},"layers":[`, ociCT, cfg)
	for i, l := range layers {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","digest":%q,"size":5}`, l)
	}
	b.WriteString("]}")
	return []byte(b.String())
}

func (f *ociFixture) push(user, name, ref string, body []byte, ct string) response {
	f.e.t.Helper()
	return f.r.do(user, http.MethodPut, "/api/oci/it/oci-local/"+name+"/manifests/"+ref, bytes.NewReader(body), "Content-Type", ct)
}

func (f *ociFixture) pull(user, name, ref string, hdr ...string) response {
	f.e.t.Helper()
	return f.r.get(user, "/api/oci/it/oci-local/"+name+"/manifests/"+ref, hdr...)
}

func (f *ociFixture) count(sql string, args ...any) int64 {
	f.e.t.Helper()
	return f.e.number(sql, args...)
}

func wantOCI(t *testing.T, res response, status int, code string) {
	t.Helper()
	if res.status != status || (code != "" && !strings.Contains(string(res.body), `"`+code+`"`)) {
		t.Fatalf("status %d, want %d %s: %s", res.status, status, code, res.body)
	}
}

func TestR_OCI_013_PushByTagAndPullOnTheProductionHost(t *testing.T) {
	f := newOCIFixture(t)
	cfg, layer := f.blob("team/app", []byte("{}")), f.blob("team/app", []byte("layer"))
	m := ociManifest(cfg, layer)
	d := shaOf(m)

	res := f.push("writer", "team/app", "1.0", m, ociCT)
	wantOCI(t, res, 201, "")
	if res.header.Get("Docker-Content-Digest") != d || !strings.HasSuffix(res.header.Get("Location"), "/team/app/manifests/"+d) {
		t.Fatalf("headers = %v", res.header)
	}
	// One blob, two items (the tag and the manifest), one package with one version, the rows of the format.
	if n := f.count("SELECT count(*) FROM items WHERE path IN ($1, $2)", "team/app/_tags/1.0", "team/app/_manifests/"+d); n != 2 {
		t.Fatalf("%d items", n)
	}
	if n := f.count("SELECT count(DISTINCT blob_sha256) FROM items WHERE path IN ($1, $2)", "team/app/_tags/1.0", "team/app/_manifests/"+d); n != 1 {
		t.Fatalf("the tag and the manifest hold %d blobs, want one", n)
	}
	if n := f.count("SELECT count(*) FROM package_versions pv JOIN packages p ON p.id = pv.package_id WHERE p.name = 'team/app' AND pv.version = '1.0' AND pv.lead_item_id = (SELECT id FROM items WHERE path = 'team/app/_tags/1.0')"); n != 1 {
		t.Fatalf("version rows = %d", n)
	}
	if n := f.count("SELECT count(*) FROM oci_manifests WHERE name = 'team/app'"); n != 1 {
		t.Fatalf("manifest rows = %d", n)
	}
	if n := f.count("SELECT count(*) FROM oci_references WHERE name = 'team/app' AND kind = 'blob'"); n != 2 {
		t.Fatalf("reference rows = %d", n)
	}
	// Pulls by tag and by digest, GET and HEAD, validators in the registry's form.
	for _, ref := range []string{"1.0", d} {
		got := f.pull("reader", "team/app", ref)
		wantOCI(t, got, 200, "")
		if !bytes.Equal(got.body, m) || got.header.Get("Docker-Content-Digest") != d || got.header.Get("ETag") != `"`+d+`"` || got.header.Get("Content-Type") != ociCT {
			t.Fatalf("pull %s: headers %v", ref, got.header)
		}
		head := f.r.do("reader", "HEAD", "/api/oci/it/oci-local/team/app/manifests/"+ref, nil)
		if head.status != 200 || len(head.body) != 0 || head.header.Get("Content-Length") != fmt.Sprint(len(m)) || head.header.Get("Docker-Content-Digest") != d {
			t.Fatalf("HEAD %s: %d %v", ref, head.status, head.header)
		}
		if nm := f.pull("reader", "team/app", ref, "If-None-Match", `"`+d+`"`); nm.status != 304 || nm.header.Get("ETag") != `"`+d+`"` {
			t.Fatalf("If-None-Match: %d %v", nm.status, nm.header)
		}
		if part := f.pull("reader", "team/app", ref, "Range", "bytes=0-9"); part.status != 206 || !bytes.Equal(part.body, m[:10]) {
			t.Fatalf("Range: %d %q", part.status, part.body)
		}
	}
	// The blob is served through its name only.
	got := f.r.get("reader", "/api/oci/it/oci-local/team/app/blobs/"+layer)
	if got.status != 200 || string(got.body) != "layer" || got.header.Get("Content-Type") != "application/octet-stream" || got.header.Get("Docker-Content-Digest") != layer {
		t.Fatalf("blob: %d %v %q", got.status, got.header, got.body)
	}
	if other := f.r.get("reader", "/api/oci/it/oci-local/other/blobs/"+layer); other.status != 404 || !strings.Contains(string(other.body), "BLOB_UNKNOWN") {
		t.Fatalf("blob through another name: %d %s", other.status, other.body)
	}
	if part := f.r.get("reader", "/api/oci/it/oci-local/team/app/blobs/"+layer, "Range", "bytes=1-2"); part.status != 206 || string(part.body) != "ay" {
		t.Fatalf("blob range: %d %q", part.status, part.body)
	}
	// The same push again changes nothing.
	rev := f.count("SELECT rev_seq FROM packages WHERE name = 'team/app'")
	wantOCI(t, f.push("writer", "team/app", "1.0", m, ociCT), 201, "")
	if after := f.count("SELECT rev_seq FROM packages WHERE name = 'team/app'"); after != rev {
		t.Fatalf("rev_seq %d -> %d for a push of what was stored", rev, after)
	}
}

func TestR_OCI_013_PushByDigestMakesNoVersionAndALaterTagLinksToIt(t *testing.T) {
	f := newOCIFixture(t)
	cfg := f.blob("app", []byte("{}"))
	m := ociManifest(cfg)
	d := shaOf(m)
	wantOCI(t, f.push("writer", "app", d, m, ociCT), 201, "")
	if n := f.count("SELECT count(*) FROM package_versions pv JOIN packages p ON p.id = pv.package_id WHERE p.name = 'app'"); n != 0 {
		t.Fatalf("a push by digest made %d versions", n)
	}
	// A digest that is not the manifest's.
	wantOCI(t, f.push("writer", "app", "sha256:"+strings.Repeat("0", 64), m, ociCT), 400, "DIGEST_INVALID")
	// The tag of a stored manifest is linked to it: no second manifest item, one version.
	revBefore := f.count("SELECT rev_seq FROM packages WHERE name = 'app'")
	wantOCI(t, f.push("writer", "app", "latest", m, ociCT), 201, "")
	if n := f.count("SELECT count(*) FROM items WHERE path LIKE 'app/_manifests/%'"); n != 1 {
		t.Fatalf("%d manifest items", n)
	}
	if after := f.count("SELECT rev_seq FROM packages WHERE name = 'app'"); after <= revBefore {
		t.Fatalf("rev_seq %d -> %d for a new tag", revBefore, after)
	}
	if got := f.pull("reader", "app", "latest"); got.status != 200 || got.header.Get("Docker-Content-Digest") != d {
		t.Fatalf("pull: %d %v", got.status, got.header)
	}
}

func TestR_OCI_013_ReferencesAreCheckedAgainstTheRepositoryAndIndexesRecordPlatforms(t *testing.T) {
	f := newOCIFixture(t)
	cfg := f.blob("multi", []byte("{}"))
	missing := shaOf([]byte("never uploaded"))
	wantOCI(t, f.push("writer", "multi", "1", ociManifest(cfg, missing), ociCT), 400, "MANIFEST_BLOB_UNKNOWN")
	var children []string
	for _, seed := range []string{"amd64", "arm64"} {
		m := ociManifest(cfg, f.blob("multi", []byte(seed)))
		wantOCI(t, f.push("writer", "multi", shaOf(m), m, ociCT), 201, "")
		children = append(children, shaOf(m))
	}
	idx := []byte(fmt.Sprintf(`{"schemaVersion":2,"mediaType":%q,"manifests":[`+
		`{"mediaType":%q,"digest":%q,"size":3,"platform":{"os":"linux","architecture":"amd64"}},`+
		`{"mediaType":%q,"digest":%q,"size":3,"platform":{"os":"linux","architecture":"arm64","variant":"v8"}}]}`, ociIndexCT, ociCT, children[0], ociCT, children[1]))
	wantOCI(t, f.push("writer", "multi", "1", idx, ociIndexCT), 201, "")
	if got := f.e.text("SELECT string_agg(platform, ',' ORDER BY platform) FROM oci_references WHERE name = 'multi' AND kind = 'manifest'"); got != "linux/amd64,linux/arm64/v8" {
		t.Fatalf("platforms = %q", got)
	}
	// An index that names a child the name does not hold is refused.
	bad := bytes.Replace(idx, []byte(children[0]), []byte(missing), 1)
	wantOCI(t, f.push("writer", "multi", "2", bad, ociIndexCT), 400, "MANIFEST_BLOB_UNKNOWN")
	// Overwriting the tag with a single manifest moves the tag only: the index stays pullable by digest.
	single := ociManifest(cfg)
	wantOCI(t, f.push("mover", "multi", "1", single, ociCT), 201, "")
	if got := f.pull("reader", "multi", "1"); got.header.Get("Docker-Content-Digest") != shaOf(single) {
		t.Fatalf("the tag points at %s", got.header.Get("Docker-Content-Digest"))
	}
	if got := f.pull("reader", "multi", shaOf(idx)); got.status != 200 || !bytes.Equal(got.body, idx) || got.header.Get("Content-Type") != ociIndexCT {
		t.Fatalf("the old index: %d %v", got.status, got.header)
	}
}

func TestR_OCI_006_AMovedTagNeedsOverwriteOnTheProductionHost(t *testing.T) {
	f := newOCIFixture(t)
	cfg := f.blob("app", []byte("{}"))
	one, two := ociManifest(cfg, f.blob("app", []byte("one"))), ociManifest(cfg, f.blob("app", []byte("two")))
	wantOCI(t, f.push("writer", "app", "1", one, ociCT), 201, "")
	wantOCI(t, f.push("writer", "app", "1", one, ociCT), 201, "") // the same digest: deploy is enough
	wantOCI(t, f.push("writer", "app", "2", one, ociCT), 201, "") // a new tag for it
	wantOCI(t, f.push("writer", "app", "1", two, ociCT), 403, "DENIED")
	if got := f.pull("reader", "app", "1"); got.header.Get("Docker-Content-Digest") != shaOf(one) {
		t.Fatalf("the tag moved without overwrite")
	}
	wantOCI(t, f.push("reader", "app", "3", one, ociCT), 403, "DENIED")
	if res := f.push("nobody", "app", "3", one, ociCT); res.status != 403 && res.status != 404 {
		t.Fatalf("a caller without a grant: %d %s", res.status, res.body)
	}
	wantOCI(t, f.push("mover", "app", "1", two, ociCT), 201, "")
	if got := f.pull("reader", "app", "1"); got.header.Get("Docker-Content-Digest") != shaOf(two) {
		t.Fatalf("the tag did not move")
	}
	if n := f.count("SELECT count(*) FROM package_versions pv JOIN packages p ON p.id = pv.package_id WHERE p.name = 'app' AND pv.trashed_at IS NULL"); n != 2 {
		t.Fatalf("%d versions, want one per tag", n)
	}
	// The moved tag's version names the new digest, through its lead item, and the old manifest stays pullable by digest.
	if n := f.count(`SELECT count(*) FROM package_versions pv JOIN items i ON i.id = pv.lead_item_id WHERE pv.version = '1' AND i.blob_sha256 = decode($1, 'hex')`, strings.TrimPrefix(shaOf(two), "sha256:")); n != 1 {
		t.Fatalf("the version of the tag does not lead with the new manifest")
	}
	wantOCI(t, f.pull("reader", "app", shaOf(one)), 200, "")
}

// Concurrent pushes of different manifests as one tag end with one digest under the tag and one version row that names it, and the
// loser of each round is told the truth (R-OCI-013 step 8).
func TestR_OCI_013_ConcurrentPushesOfOneTagLeaveOneDigestAndOneVersion(t *testing.T) {
	f := newOCIFixture(t)
	cfg := f.blob("app", []byte("{}"))
	const n = 6
	manifests := make([][]byte, n)
	for i := range n {
		manifests[i] = ociManifest(cfg, f.blob("app", []byte(fmt.Sprint("layer ", i))))
	}
	for round := range 3 {
		var wg sync.WaitGroup
		statuses := make([]int, n)
		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				statuses[i] = f.push("mover", "app", fmt.Sprint("race", round), manifests[i], ociCT).status
			}()
		}
		wg.Wait()
		// Every pusher holds overwrite: a push that finds the tag moved meanwhile decides again and commits, so all are 201.
		for _, s := range statuses {
			if s != 201 {
				t.Fatalf("round %d: statuses %v, want every push to end 201", round, statuses)
			}
		}
		tag := fmt.Sprint("race", round)
		got := f.pull("reader", "app", tag)
		if got.status != 200 {
			t.Fatalf("round %d: pull %d", round, got.status)
		}
		if v := f.count("SELECT count(*) FROM package_versions pv JOIN packages p ON p.id = pv.package_id WHERE p.name = 'app' AND pv.version = $1 AND pv.trashed_at IS NULL", tag); v != 1 {
			t.Fatalf("round %d: %d version rows", round, v)
		}
		if v := f.count(`SELECT count(*) FROM package_versions pv JOIN items i ON i.id = pv.lead_item_id WHERE pv.version = $1 AND i.path = $2
			AND i.blob_sha256 = decode($3, 'hex')`, tag, "app/_tags/"+tag, strings.TrimPrefix(got.header.Get("Docker-Content-Digest"), "sha256:")); v != 1 {
			t.Fatalf("round %d: the version row does not name the digest the tag serves", round)
		}
		if m := f.count("SELECT count(*) FROM oci_manifests WHERE name = 'app' AND digest = decode($1, 'hex')", strings.TrimPrefix(got.header.Get("Docker-Content-Digest"), "sha256:")); m != 1 {
			t.Fatalf("round %d: the served manifest has no row", round)
		}
	}
}

func TestR_OCI_033_AWithheldManifestIsRefusedOnThePullAndOnThePushOfTheProductionHost(t *testing.T) {
	f := newOCIFixture(t)
	cfg := f.blob("app", []byte("{}"))
	m := ociManifest(cfg)
	d := shaOf(m)
	wantOCI(t, f.push("writer", "app", "1", m, ociCT), 201, "")
	wantOCI(t, f.pull("reader", "app", "1"), 200, "")
	f.e.exec("INSERT INTO oci_tombstones (repo_id, name, digest) SELECT id, 'app', decode($1, 'hex') FROM repositories WHERE key = 'oci-local'", strings.TrimPrefix(d, "sha256:"))
	// The tag item and the manifest item are refused with the 404 of a missing manifest, from the facts the item lookup read.
	wantOCI(t, f.pull("reader", "app", "1"), 404, "MANIFEST_UNKNOWN")
	wantOCI(t, f.pull("reader", "app", d), 404, "MANIFEST_UNKNOWN")
	// A push of the digest, by tag or by digest, answers 409, and the same bytes under another name are another manifest.
	wantOCI(t, f.push("writer", "app", "2", m, ociCT), 409, "DENIED")
	wantOCI(t, f.push("writer", "app", d, m, ociCT), 409, "DENIED")
	other := f.blob("other", []byte("{}"))
	if other != cfg {
		t.Fatal("same config")
	}
	wantOCI(t, f.push("writer", "other", "1", m, ociCT), 201, "")
}

func TestR_OCI_013_CraftedManifestsAreRefusedOnTheProductionHost(t *testing.T) {
	f := newOCIFixture(t)
	cfg := f.blob("app", []byte("{}"))
	good := ociManifest(cfg)
	for name, tc := range map[string]struct {
		body   []byte
		ct     string
		status int
		code   string
	}{
		"schema 1":               {good, "application/vnd.docker.distribution.manifest.v1+prettyjws", 400, "MANIFEST_INVALID"},
		"a type that is none":    {good, "text/plain", 400, "MANIFEST_INVALID"},
		"the other type's field": {good, ociIndexCT, 400, "MANIFEST_INVALID"},
		"nested 100,000":         {[]byte(`{"schemaVersion":2,"x":` + strings.Repeat("[", 100_000) + strings.Repeat("]", 100_000) + `}`), ociCT, 413, "SIZE_INVALID"},
		"a 5 MiB index":          {[]byte(`{"schemaVersion":2,"manifests":[],"annotations":{"k":"` + strings.Repeat("a", 5<<20) + `"}}`), ociIndexCT, 413, "SIZE_INVALID"},
		"a digest that climbs":   {[]byte(`{"schemaVersion":2,"config":{"mediaType":"a/b","digest":"sha256:../../x","size":1},"layers":[]}`), ociCT, 400, "MANIFEST_INVALID"},
		"a duplicate key":        {[]byte(`{"schemaVersion":2,"schemaVersion":2,"config":{"mediaType":"a/b","digest":"` + cfg + `","size":1},"layers":[]}`), ociCT, 400, "MANIFEST_INVALID"},
	} {
		t.Run(name, func(t *testing.T) { wantOCI(t, f.push("writer", "app", "x", tc.body, tc.ct), tc.status, tc.code) })
	}
	if n := f.count("SELECT count(*) FROM items WHERE path LIKE 'app/_tags/%' OR path LIKE 'app/_manifests/%'"); n != 0 {
		t.Fatalf("crafted manifests left %d items", n)
	}
	// A raw deploy into an OCI repository is refused before the body, and the repository serves nothing from /files/ that was not pushed.
	if res := f.r.put("writer", "/files/it/oci-local/app/_tags/1", string(good)); res.status != 409 {
		t.Fatalf("raw deploy: %d %s", res.status, res.body)
	}
}

// A grant or a policy scoped by path decides on the canonical path of the item a protocol route acts on, as `/files/` does (R-AUTHZ-005,
// R-OCI-006): a path the policy excludes is neither pulled nor pushed through the protocol, and a path outside an include list is not
// reachable either.
func TestR_OCI_006_PathScopedPoliciesApplyToTheProtocolRoutes(t *testing.T) {
	f := newOCIFixture(t)
	cfgS, layerS := f.blob("secret/app", []byte("{}")), f.blob("secret/app", []byte("secret layer"))
	ms := ociManifest(cfgS, layerS)
	wantOCI(t, f.push("writer", "secret/app", "1", ms, ociCT), 201, "")
	cfg := f.blob("app", []byte("{}"))
	m := ociManifest(cfg)
	cfgT := f.blob("team/app", []byte("{}"))
	mt := ociManifest(cfgT)
	denied := func(res response, what string) {
		t.Helper()
		if res.status != 403 && res.status != 404 {
			t.Fatalf("%s: %d %s", what, res.status, res.body)
		}
	}
	// scoped: everything but secret/**
	wantOCI(t, f.push("scoped", "app", "1", m, ociCT), 201, "")
	wantOCI(t, f.pull("scoped", "app", "1"), 200, "")
	denied(f.push("scoped", "secret/app", "2", ms, ociCT), "push to an excluded path")
	denied(f.push("scoped", "secret/app", shaOf(ms), ms, ociCT), "push by digest to an excluded path")
	denied(f.pull("scoped", "secret/app", "1"), "pull by tag of an excluded path")
	denied(f.pull("scoped", "secret/app", shaOf(ms)), "pull by digest of an excluded path")
	denied(f.r.get("scoped", "/api/oci/it/oci-local/secret/app/blobs/"+layerS), "pull of a blob of an excluded path")
	if res := f.r.get("scoped", "/files/it/oci-local/secret/app/_tags/1"); res.status == 200 {
		t.Fatalf("/files/ serves the excluded path: the two routes disagree")
	}
	wantOCI(t, f.pull("writer", "secret/app", "1"), 200, "") // the broad policy still reads it
	// teamonly: team/** only
	wantOCI(t, f.push("teamonly", "team/app", "1", mt, ociCT), 201, "")
	wantOCI(t, f.pull("teamonly", "team/app", "1"), 200, "")
	denied(f.pull("teamonly", "app", "1"), "pull outside the include list")
	denied(f.push("teamonly", "app", "2", m, ociCT), "push outside the include list")
}

// A tag push and a push by digest of the same new manifest take their locks in different orders (the tag item, then the package row,
// then the manifest item, against the manifest item, then the package row); a cycle between them is broken by the database, the
// transaction runner retries it, and every push ends 201.
func TestR_OCI_013_ATagPushAndADigestPushOfOneNewManifestBothEnd201(t *testing.T) {
	f := newOCIFixture(t)
	cfg := f.blob("app", []byte("{}"))
	for round := range 6 {
		m := ociManifest(cfg, f.blob("app", []byte(fmt.Sprint("layer ", round))))
		d := shaOf(m)
		var wg sync.WaitGroup
		statuses := make([]int, 4)
		for i, ref := range []string{fmt.Sprint("t", round), d, fmt.Sprint("u", round), d} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				statuses[i] = f.push("mover", "app", ref, m, ociCT).status
			}()
		}
		wg.Wait()
		for _, st := range statuses {
			if st != 201 {
				t.Fatalf("round %d: statuses %v, want every push to end 201", round, statuses)
			}
		}
		wantOCI(t, f.pull("reader", "app", d), 200, "")
	}
}
