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

// The seams for the OCI tests of this area that need the item store, the storage hooks and the package lock under their own control
// (a removal holding the package lock, the delete job's guard, a standing session): the real dispatcher, the real item store over
// PostgreSQL and S3, the real storage hooks and package writer, and the data port over the same database, with the format mounted at
// /api/oci/<project>/<key>/ through a wrapper that changes the one fact of the descriptor that decides where it is mounted. The
// protocol criteria of the format run over /v2/ in test/integration/oci, against replicas of the whole application; what stays here
// is what a replica cannot stage (R-OCI-013, R-OCI-018, R-OCI-019 races and guards). Ownership and the namespace auto claim are stood
// in for by seams that allow, because their rules are tested with the core (R-AUTHZ-018, R-REPO-023).

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
	e     *env
	r     *replica
	host  *formathost.Host
	ports *formathost.Bindings
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
	f := &ociFixture{e: e, ports: b}
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
	e.user("hidetags")
	e.user("itemfolders")
	e.user("digestonly")
	e.grant(f.r, "oci-write", []string{"oci-local"}, authz.PatternLists{}, map[string][]string{
		"user:writer": {"read", "deploy"}, "user:mover": {"read", "deploy", "overwrite"}, "user:reader": {"read"},
	})
	e.grant(f.r, "oci-scoped", []string{"oci-local"}, authz.PatternLists{Exclude: []string{"secret/**"}}, map[string][]string{"user:scoped": {"read", "deploy"}})
	e.grant(f.r, "oci-team", []string{"oci-local"}, authz.PatternLists{Include: []string{"team/**"}}, map[string][]string{"user:teamonly": {"read", "deploy"}})
	e.grant(f.r, "oci-hidetags", []string{"oci-local"}, authz.PatternLists{Exclude: []string{"team/app/_tags/internal-*"}}, map[string][]string{"user:hidetags": {"read", "deploy"}})
	e.grant(f.r, "oci-itemfolders", []string{"oci-local"}, authz.PatternLists{Include: []string{"team/app/_tags/**", "team/app/_manifests/**", "team/app/_blobs/**"}}, map[string][]string{"user:itemfolders": {"read", "deploy"}})
	e.grant(f.r, "oci-digestonly", []string{"oci-local"}, authz.PatternLists{Include: []string{"**/_manifests/**", "**/_blobs/**"}}, map[string][]string{"user:digestonly": {"read"}})
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
