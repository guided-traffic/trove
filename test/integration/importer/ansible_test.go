//go:build integration

package importer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/config"
	imp "github.com/guided-traffic/trove/internal/importer"
	"github.com/guided-traffic/trove/internal/outbound"
	"github.com/guided-traffic/trove/test/ansiblestub"
)

// ansibleSource is the counting stub Galaxy behind TLS with the factory that trusts it and reaches the target replica by its name.
type ansibleSource struct {
	stub    *ansiblestub.Server
	root    string
	factory *outbound.Factory
}

func (e *env) startAnsible() *ansibleSource {
	e.t.Helper()
	cert, err := ansiblestub.NewSelfSigned(ansiblestub.LoopbackHosts...)
	if err != nil {
		e.t.Fatal(err)
	}
	stub, err := ansiblestub.New(ansiblestub.Config{})
	if err != nil {
		e.t.Fatal(err)
	}
	run, err := stub.Start("127.0.0.1:0", cert.ServerConfig())
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { _ = run.Close() })
	ca := filepath.Join(e.t.TempDir(), "ansible-ca.pem")
	if err := os.WriteFile(ca, cert.CAPEM, 0o600); err != nil {
		e.t.Fatal(err)
	}
	dial := run.DialOverride()
	dial[targetHost+":80"] = e.rep.Addr("default")
	f := outbound.New(config.Outbound{
		TLS:           config.OutboundTLS{CAFiles: []string{ca}, MinVersion: "1.2"},
		AddressPolicy: config.OutboundAddressPolicy{AllowCIDRs: []string{"127.0.0.1/32"}},
	}, outbound.WithDialOverride(dial))
	if err := f.Err(); err != nil {
		e.t.Fatal(err)
	}
	return &ansibleSource{stub: stub, root: run.APIRoot(), factory: f}
}

func ansibleTarget(repo string) string {
	return "http://" + targetHost + "/api/ansible/it/" + repo + "/"
}

func (e *env) ansibleOptions(a *ansibleSource, token string, names ...string) imp.Options {
	e.t.Helper()
	f, src, dst, err := imp.Build(a.factory, imp.Params{Format: "ansible", From: a.root, To: ansibleTarget(rAnsible), Token: token, ExtraHosts: []string{"*.localhost"}})
	if err != nil {
		e.t.Fatal(err)
	}
	src.Retry, dst.Retry = fast, fast
	return imp.Options{Format: f, Source: src, Target: dst, Names: names, Concurrency: 2, Dir: e.t.TempDir()}
}

func (e *env) ansibleVersions(name string) int64 {
	e.t.Helper()
	return e.st.Number(`SELECT count(*) FROM ansible_collection_versions v JOIN repositories r ON r.id = v.repo_id WHERE r.key = $1 AND v.name = $2`, rAnsible, name)
}

func TestR_ANS_027_ACollectionOfTheStubIsCopiedThroughThePublishRoadAndASecondRunSkipsEveryVersion(t *testing.T) {
	e := newEnv(t)
	a := e.startAnsible()
	corpus, err := a.stub.Corpus()
	if err != nil {
		t.Fatal(err)
	}
	var tools ansiblestub.CollectionInfo
	for _, c := range corpus.Collections {
		if c.Namespace == "acme" && c.Name == "tools" {
			tools = c
		}
	}
	if len(tools.Versions) == 0 {
		t.Fatal("the stub holds no acme.tools")
	}
	rep, err := imp.Run(context.Background(), e.ansibleOptions(a, e.writer, "acme.tools"))
	if err != nil || !rep.OK() {
		t.Fatalf("run: %v %+v", err, rep.Packages)
	}
	if got := e.ansibleVersions("acme.tools"); got != int64(len(tools.Versions)) {
		t.Errorf("%d versions stored, the stub holds %d", got, len(tools.Versions))
	}
	// The road is the publish: one event per version by the importing user, and the stored bytes are the source's.
	if got := e.st.Number(`SELECT count(*) FROM audit_event WHERE action = 'package.published' AND actor_id = $1::uuid`, e.writerID); got != int64(len(tools.Versions)) {
		t.Errorf("events by the importing user: %d, want %d", got, len(tools.Versions))
	}
	for _, v := range tools.Versions {
		got := e.st.Text(`SELECT encode(v.sha256, 'hex') FROM ansible_collection_versions v JOIN repositories r ON r.id = v.repo_id WHERE r.key = $1 AND v.name = 'acme.tools' AND v.version = $2`, rAnsible, v.Version)
		if got != v.SHA256 {
			t.Errorf("%s: stored sha256 %s, the stub announced %s", v.Version, got, v.SHA256)
		}
	}
	before := e.st.Number(`SELECT count(*) FROM audit_event WHERE action = 'package.published'`)
	rep, err = imp.Run(context.Background(), e.ansibleOptions(a, e.writer, "acme.tools"))
	if err != nil || !rep.OK() {
		t.Fatalf("second run: %v %+v", err, rep.Packages)
	}
	for _, v := range pkgOf(t, rep, "acme.tools").Versions {
		if v.Outcome != imp.OutcomeSkipped {
			t.Errorf("second run %s: %+v", v.Version, v)
		}
	}
	if after := e.st.Number(`SELECT count(*) FROM audit_event WHERE action = 'package.published'`); after != before {
		t.Errorf("the second run wrote %d events", after-before)
	}
	// The roles of the source are named, once, and not imported.
	var roles bool
	for _, n := range pkgOf(t, rep, "acme.tools").NotKept {
		roles = roles || strings.Contains(n, "roles of the source")
	}
	if !roles {
		t.Errorf("notKept %v names no roles", pkgOf(t, rep, "acme.tools").NotKept)
	}
}

func TestR_ANS_027_ASearchCopiesEveryCollectionItFindsAndTheDryRunWritesNothing(t *testing.T) {
	e := newEnv(t)
	a := e.startAnsible()
	search := "other."
	o := e.ansibleOptions(a, e.writer)
	o.Search = &search
	o.DryRun = true
	rep, err := imp.Run(context.Background(), o)
	if err != nil || !rep.OK() {
		t.Fatalf("dry run: %v %+v", err, rep.Packages)
	}
	if len(rep.Packages) == 0 || e.ansibleVersions("other.stuff") != 0 {
		t.Fatalf("packages %+v, stored %d", rep.Packages, e.ansibleVersions("other.stuff"))
	}
	for _, v := range pkgOf(t, rep, "other.stuff").Versions {
		if v.Outcome != imp.OutcomeWouldAdd {
			t.Errorf("%s: %+v", v.Version, v)
		}
	}
	o = e.ansibleOptions(a, e.writer)
	o.Search = &search
	if rep, err = imp.Run(context.Background(), o); err != nil || !rep.OK() {
		t.Fatalf("run: %v %+v", err, rep.Packages)
	}
	if e.ansibleVersions("other.stuff") == 0 {
		t.Error("the search copied nothing")
	}
}

func TestR_ANS_027_ASourceArchiveWhoseBytesAreNotTheAnnouncedDigestIsNeverSentToTheTarget(t *testing.T) {
	e := newEnv(t)
	a := e.startAnsible()
	if err := a.stub.SetMode(ansiblestub.Mode{WrongDigest: []string{"acme.tools"}}); err != nil {
		t.Fatal(err)
	}
	rep, err := imp.Run(context.Background(), e.ansibleOptions(a, e.writer, "acme.tools"))
	if err != nil {
		t.Fatal(err)
	}
	if rep.OK() {
		t.Fatal("a digest mismatch left the report OK")
	}
	for _, v := range pkgOf(t, rep, "acme.tools").Versions {
		if v.Outcome != imp.OutcomeFailed || v.Code != imp.CodeDigestMismatch {
			t.Errorf("%s: %+v", v.Version, v)
		}
	}
	if got := e.ansibleVersions("acme.tools"); got != 0 {
		t.Errorf("%d versions stored of an archive that did not match its digest", got)
	}
}

func TestR_ANS_027_ATokenWithoutThePublishRightIsRefusedAndThePreflightRefusesAnotherFormatsRepository(t *testing.T) {
	e := newEnv(t)
	a := e.startAnsible()
	rep, err := imp.Run(context.Background(), e.ansibleOptions(a, e.reader, "acme.tools"))
	if err != nil {
		t.Fatalf("a refused package does not stop the run: %v", err)
	}
	for _, v := range pkgOf(t, rep, "acme.tools").Versions {
		if v.Outcome != imp.OutcomeFailed || v.Code != imp.CodeForbidden {
			t.Errorf("%s: %+v", v.Version, v)
		}
	}
	if got := e.ansibleVersions("acme.tools"); got != 0 {
		t.Errorf("%d versions stored by a reader", got)
	}
	// A repository of another format is not a target: the discovery document is not a Galaxy one.
	f, _, dst, err := imp.Build(a.factory, imp.Params{Format: "ansible", From: a.root, To: npmTarget(rNPM), Token: e.writer})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Preflight(context.Background(), dst); err == nil {
		t.Error("an npm repository passed the preflight of the Galaxy import")
	}
}
