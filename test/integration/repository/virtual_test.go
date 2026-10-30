//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/repository"
)

// pkgNames is the name rule of the test format: names compare as written, the
// way npm's do.
type pkgNames struct{}

func (pkgNames) Normalize(name string) (string, bool) { return name, name != "" }

// pkgCatalog registers a format with package names, claims and priority members
// (what npm declares) and one that has nothing of it, so the tests of virtual
// repositories need no format to be built.
func pkgCatalog() *catalog.Registry {
	r := catalog.NewRegistry()
	r.Register(catalog.Entry{Facts: catalog.Facts{
		ID: "pkgfmt", Classes: []catalog.Class{catalog.Local, catalog.Remote, catalog.Virtual}, Layout: "pkgfmt",
		ProtocolBasePath: "/api/pkgfmt/{key}/", PatternSubject: catalog.SubjectName,
		PackageNames: true, PriorityMembers: true, PinsAndClaims: true,
	}, Names: pkgNames{}})
	r.Register(catalog.Entry{Facts: catalog.Facts{
		ID: "nopkg", Classes: []catalog.Class{catalog.Local}, Layout: "nopkg", ProtocolBasePath: "/files/{key}/",
		PatternSubject: catalog.SubjectPath,
	}})
	r.Register(catalog.Entry{Facts: catalog.Facts{
		ID: "otherfmt", Classes: []catalog.Class{catalog.Local, catalog.Remote, catalog.Virtual}, Layout: "otherfmt",
		ProtocolBasePath: "/api/otherfmt/{key}/", PatternSubject: catalog.SubjectName,
		PackageNames: true, PinsAndClaims: true,
	}, Names: pkgNames{}})
	return r
}

func doc(key, class, format, extra string) []byte {
	if extra != "" {
		extra = "," + extra
	}
	return []byte(fmt.Sprintf(`{"key":%q,"class":%q,"format":%q%s}`, key, class, format, extra))
}

func (r *replica) mustCreate(key, class, format, extra string) repository.Stored {
	r.t.Helper()
	if class == "remote" && !strings.Contains(extra, `"remote"`) {
		// A remote repository needs its upstream (R-REPO-024); these tests never contact it.
		remote := `"remote":{"url":"https://upstream.example.test/"}`
		if extra == "" {
			extra = remote
		} else {
			extra = remote + "," + extra
		}
	}
	st, err := r.svc.Create(context.Background(), caller, doc(key, class, format, extra))
	if err != nil {
		r.t.Fatalf("create %s: %v", key, err)
	}
	return st
}

func fieldPointers(t *testing.T, err error) map[string]string {
	t.Helper()
	e, ok := errs.As(err)
	if !ok {
		t.Fatalf("err = %v", err)
	}
	out := map[string]string{}
	for _, f := range e.Fields {
		out[f.Pointer] = f.Code
	}
	return out
}

func memberKeysOf(r repository.Repository) []string {
	var out []string
	for _, m := range r.Members() {
		out = append(out, m.Key)
	}
	return out
}

func TestVirtualMembers(t *testing.T) {
	e := newEnv(t)
	poll := 300 * time.Millisecond
	// Neither replica has a Valkey client: the change log of PostgreSQL carries
	// every change (R-REPO-049), which is the case "also with Valkey stopped".
	a := e.newReplica("virt-a", replicaOpts{poll: poll, catalog: pkgCatalog(), maxMembers: 3})
	b := e.newReplica("virt-b", replicaOpts{poll: poll, catalog: pkgCatalog(), maxMembers: 3})
	ctx := context.Background()

	a.mustCreate("pkg-local", "local", "pkgfmt", "")
	a.mustCreate("pkg-remote", "remote", "pkgfmt", "")
	a.mustCreate("pkg-team", "local", "pkgfmt", "")
	a.mustCreate("other-local", "local", "otherfmt", "")

	t.Run("R-REPO-008 R-REPO-017 a member of another format and a missing key are two invalid_member entries", func(t *testing.T) {
		_, err := a.svc.Create(ctx, caller, doc("bad-all", "virtual", "pkgfmt", `"virtual":{"members":["other-local","ghost"]}`))
		ee := wantCode(t, err, 422, errs.CodeValidation)
		if len(ee.Fields) != 2 {
			t.Fatalf("fields = %+v", ee.Fields)
		}
		f := fieldPointers(t, err)
		if f["/virtual/members/0"] != errs.CodeInvalidMember || f["/virtual/members/1"] != errs.CodeInvalidMember {
			t.Fatalf("fields = %v", f)
		}
		if n := e.scalar(`SELECT count(*) FROM repositories WHERE key = 'bad-all'`); n != 0 {
			t.Fatalf("a refused create left %d rows", n)
		}
	})

	t.Run("R-REPO-017 R-REPO-021 a virtual repository is created with members and a deploy target, stored as rows", func(t *testing.T) {
		st := a.mustCreate("pkg-all", "virtual", "pkgfmt",
			`"virtual":{"members":["pkg-local","pkg-remote"],"deployTarget":"pkg-local","mergedMetadataTtlSeconds":600}`)
		v := st.Document.Virtual
		if v == nil || !slices.Equal(v.Members, []string{"pkg-local", "pkg-remote"}) || *v.DeployTarget != "pkg-local" || *v.MergedMetadataTTLSeconds != 600 {
			t.Fatalf("document = %+v", v)
		}
		if n := e.scalar(`SELECT count(*) FROM repositories WHERE key = 'pkg-all' AND (config::text LIKE '%pkg-local%' OR config::text LIKE '%members%')`); n != 0 {
			t.Fatal("repositories.config holds references")
		}
		if n := e.scalar(`SELECT count(*) FROM virtual_members m JOIN repositories v ON v.id = m.virtual_id WHERE v.key = 'pkg-all'`); n != 2 {
			t.Fatalf("%d member rows", n)
		}
		if n := e.scalar(`SELECT count(*) FROM virtual_deploy_targets`); n != 1 {
			t.Fatalf("%d deploy target rows", n)
		}
		got, err := a.svc.Get(ctx, "pkg-all")
		if err != nil || !slices.Equal(got.Document.Virtual.Members, v.Members) {
			t.Fatalf("Get = %+v, %v", got.Document.Virtual, err)
		}
		if got.Repository.Version != 1 {
			t.Fatalf("version = %d", got.Repository.Version)
		}
	})

	t.Run("R-REPO-017 the snapshot carries the members in order and the deploy target", func(t *testing.T) {
		waitFor(t, "B to know the virtual repository", 3*poll, func() bool {
			r, err := b.reader.ByKey(ctx, "pkg-all")
			return err == nil && slices.Equal(memberKeysOf(r), []string{"pkg-local", "pkg-remote"})
		})
		r, _ := b.reader.ByKey(ctx, "pkg-all")
		tgt, ok := r.DeployTarget()
		if !ok || tgt.Key != "pkg-local" {
			t.Fatalf("deploy target = %v, %v", tgt.Key, ok)
		}
		vs, _ := r.Virtual()
		if *vs.MergedMetadataTTLSeconds != 600 {
			t.Fatalf("settings = %+v", vs)
		}
	})

	t.Run("R-REPO-017 a change of the member order is visible on both replicas within 2 seconds, without Valkey", func(t *testing.T) {
		start := time.Now()
		if _, err := a.svc.Patch(ctx, caller, "pkg-all", repository.MatchAny(),
			mergePatch(`{"virtual":{"members":["pkg-remote","pkg-local"]}}`)); err != nil {
			t.Fatal(err)
		}
		for _, rep := range []*replica{a, b} {
			waitFor(t, rep.name+" to show the new order", 2*time.Second, func() bool {
				r, err := rep.reader.ByKey(ctx, "pkg-all")
				return err == nil && slices.Equal(memberKeysOf(r), []string{"pkg-remote", "pkg-local"})
			})
		}
		if d := time.Since(start); d > 2*time.Second {
			t.Fatalf("the change took %s", d)
		}
		// The routing reads the same snapshot: the order is the one a request is resolved in.
		r, _ := b.reader.ByKey(ctx, "pkg-all")
		route, err := b.routing.Route(ctx, r, repository.RouteRequest{Name: "left-pad"})
		if err != nil {
			t.Fatal(err)
		}
		if got := keysOf(route.Members); !slices.Equal(got, []string{"pkg-remote", "pkg-local"}) {
			t.Fatalf("routed members = %v", got)
		}
	})

	t.Run("R-REPO-011 a patch that changes only the members moves the version and records an update", func(t *testing.T) {
		before, _ := a.svc.Get(ctx, "pkg-all")
		st, err := a.svc.Patch(ctx, caller, "pkg-all", repository.MatchVersion(before.Repository.Version),
			mergePatch(`{"virtual":{"members":["pkg-local","pkg-remote"]}}`))
		if err != nil {
			t.Fatal(err)
		}
		if st.Repository.Version != before.Repository.Version+1 {
			t.Fatalf("version %d -> %d", before.Repository.Version, st.Repository.Version)
		}
		ev := a.rec.last()
		d := map[string][]string{}
		_ = json.Unmarshal(ev.Details, &d)
		if ev.Type != repository.EventUpdated || !slices.Equal(d["changes"], []string{"virtual"}) {
			t.Fatalf("event = %s %s", ev.Type, ev.Details)
		}
		// The same document again changes nothing.
		again, err := a.svc.Patch(ctx, caller, "pkg-all", repository.MatchAny(), mergePatch(`{"virtual":{"members":["pkg-local","pkg-remote"]}}`))
		if err != nil || again.Repository.Version != st.Repository.Version {
			t.Fatalf("version %d, %v", again.Repository.Version, err)
		}
	})

	t.Run("R-REPO-021 R-REPO-008 a member list without the deploy target is invalid_deploy_target", func(t *testing.T) {
		_, err := a.svc.Patch(ctx, caller, "pkg-all", repository.MatchAny(), mergePatch(`{"virtual":{"members":["pkg-remote"]}}`))
		wantCode(t, err, 422, errs.CodeValidation)
		if f := fieldPointers(t, err); f["/virtual/deployTarget"] != "invalid_deploy_target" {
			t.Fatalf("fields = %v", f)
		}
	})
	t.Run("R-REPO-021 a remote repository cannot be the deploy target", func(t *testing.T) {
		_, err := a.svc.Patch(ctx, caller, "pkg-all", repository.MatchAny(), mergePatch(`{"virtual":{"deployTarget":"pkg-remote"}}`))
		if f := fieldPointers(t, err); f["/virtual/deployTarget"] != "invalid_deploy_target" {
			t.Fatalf("fields = %v", f)
		}
	})
	t.Run("R-REPO-021 a deploy target is removed with null and the repository becomes read-only", func(t *testing.T) {
		a.mustCreate("pkg-ro", "virtual", "pkgfmt", `"virtual":{"members":["pkg-local"],"deployTarget":"pkg-local"}`)
		st, err := a.svc.Patch(ctx, caller, "pkg-ro", repository.MatchAny(), mergePatch(`{"virtual":{"deployTarget":null}}`))
		if err != nil || st.Document.Virtual.DeployTarget != nil {
			t.Fatalf("document = %+v, %v", st.Document.Virtual, err)
		}
		if n := e.scalar(`SELECT count(*) FROM virtual_deploy_targets t JOIN repositories v ON v.id = t.virtual_id WHERE v.key = 'pkg-ro'`); n != 0 {
			t.Fatalf("%d deploy target rows", n)
		}
	})
	t.Run("R-REPO-017 a virtual repository takes no virtual member before nesting exists", func(t *testing.T) {
		_, err := a.svc.Create(ctx, caller, doc("pkg-nest", "virtual", "pkgfmt", `"virtual":{"members":["pkg-all"]}`))
		if f := fieldPointers(t, err); f["/virtual/members/0"] != errs.CodeInvalidMember {
			t.Fatalf("fields = %v", f)
		}
	})
	t.Run("R-REPO-017 repositories.max_virtual_members bounds the member list", func(t *testing.T) {
		_, err := a.svc.Create(ctx, caller, doc("pkg-big", "virtual", "pkgfmt", `"virtual":{"members":["pkg-local","pkg-remote","pkg-team","pkg-ro"]}`))
		if f := fieldPointers(t, err); f["/virtual/members"] != "out_of_range" {
			t.Fatalf("fields = %v", f)
		}
	})

	t.Run("R-REPO-013 a repository that a virtual repository lists cannot be deleted without detach", func(t *testing.T) {
		_, err := a.svc.Delete(ctx, caller, "pkg-local", false)
		ee := wantCode(t, err, 409, errs.CodeRepositoryReferenced)
		if !strings.Contains(ee.Detail, "pkg-all") || !strings.Contains(ee.Detail, "pkg-ro") {
			t.Fatalf("detail = %q names neither virtual repository", ee.Detail)
		}
		if n := e.scalar(`SELECT count(*) FROM repositories WHERE key = 'pkg-local' AND lifecycle = 'active'`); n != 1 {
			t.Fatal("the refused delete marked the repository")
		}
	})
	t.Run("R-REPO-013 R-REPO-021 detach removes the references, moves the versions and records updates in one transaction", func(t *testing.T) {
		w := e.newReplica("virt-w", replicaOpts{role: "worker", poll: poll, catalog: pkgCatalog(), maxMembers: 3})
		before, _ := a.svc.Get(ctx, "pkg-all")
		res, err := a.svc.Delete(ctx, caller, "pkg-local", true)
		if err != nil || res.JobID == "" {
			t.Fatalf("delete = %+v, %v", res, err)
		}
		got, _ := a.svc.Get(ctx, "pkg-all")
		if !slices.Equal(got.Document.Virtual.Members, []string{"pkg-remote"}) || got.Document.Virtual.DeployTarget != nil {
			t.Fatalf("virtual = %+v", got.Document.Virtual)
		}
		if got.Repository.Version != before.Repository.Version+1 {
			t.Fatalf("version %d -> %d", before.Repository.Version, got.Repository.Version)
		}
		if n := e.scalar(`SELECT count(*) FROM virtual_members m JOIN repositories r ON r.id = m.member_id WHERE r.key = 'pkg-local'`); n != 0 {
			t.Fatalf("%d member rows remain", n)
		}
		waitFor(t, "B to drop the member", 3*poll, func() bool {
			r, err := b.reader.ByKey(ctx, "pkg-all")
			return err == nil && slices.Equal(memberKeysOf(r), []string{"pkg-remote"})
		})
		if j := e.waitJob(w.queue, res.JobID); j.State != "succeeded" {
			t.Fatalf("job = %s", j.State)
		}
		if n := e.scalar(`SELECT count(*) FROM repositories WHERE key = 'pkg-local'`); n != 0 {
			t.Fatal("the row is still there")
		}
	})
	t.Run("R-REPO-017 a repository being deleted is not accepted as a member", func(t *testing.T) {
		a.mustCreate("pkg-dying", "local", "pkgfmt", "")
		e.exec(`UPDATE repositories SET lifecycle = 'deleting' WHERE key = 'pkg-dying'`)
		_, err := a.svc.Patch(ctx, caller, "pkg-all", repository.MatchAny(), mergePatch(`{"virtual":{"members":["pkg-remote","pkg-dying"]}}`))
		if f := fieldPointers(t, err); f["/virtual/members/1"] != errs.CodeInvalidMember {
			t.Fatalf("fields = %v", f)
		}
	})
	t.Run("R-REPO-013 the row of a virtual repository goes with its member rows", func(t *testing.T) {
		res, err := a.svc.Delete(ctx, caller, "pkg-ro", false)
		if err != nil || !res.Done {
			t.Fatalf("delete = %+v, %v", res, err)
		}
		if n := e.scalar(`SELECT count(*) FROM virtual_members m LEFT JOIN repositories v ON v.id = m.virtual_id WHERE v.id IS NULL`); n != 0 {
			t.Fatalf("%d orphan member rows", n)
		}
	})
}

func keysOf(repos []repository.Repository) []string {
	var out []string
	for _, r := range repos {
		out = append(out, r.Key)
	}
	return out
}

func TestVirtualPinsAndWarnings(t *testing.T) {
	e := newEnv(t)
	poll := 300 * time.Millisecond
	a := e.newReplica("pin-a", replicaOpts{poll: poll, catalog: pkgCatalog()})
	b := e.newReplica("pin-b", replicaOpts{poll: poll, catalog: pkgCatalog()})
	ctx := context.Background()
	a.mustCreate("loc-open", "local", "pkgfmt", `"local":{"autoClaim":false}`)
	a.mustCreate("loc-auto", "local", "pkgfmt", "")
	a.mustCreate("rem-pub", "remote", "pkgfmt", "")
	a.mustCreate("rem-partner", "remote", "pkgfmt", "")

	warnings := func(st repository.Stored) []string {
		var out []string
		for _, w := range st.Warnings {
			out = append(out, w.Code)
		}
		return out
	}

	t.Run("R-REPO-023 R-REPO-008 dependency_confusion_unprotected: local and remote members, no pin rule, nothing claims", func(t *testing.T) {
		st := a.mustCreate("v-open", "virtual", "pkgfmt", `"virtual":{"members":["loc-open","rem-pub"]}`)
		if !slices.Equal(warnings(st), []string{"dependency_confusion_unprotected"}) {
			t.Fatalf("warnings = %v", warnings(st))
		}
	})
	t.Run("R-REPO-023 an automatically claiming local member protects the repository", func(t *testing.T) {
		st := a.mustCreate("v-auto", "virtual", "pkgfmt", `"virtual":{"members":["loc-auto","rem-pub"]}`)
		if len(warnings(st)) != 0 {
			t.Fatalf("warnings = %v", warnings(st))
		}
	})
	t.Run("R-REPO-023 a pin rule, or a claim of the local member, protects the repository", func(t *testing.T) {
		st := a.mustCreate("v-pinned", "virtual", "pkgfmt",
			`"virtual":{"members":["loc-open","rem-pub"],"pinning":{"rules":[{"packages":["@acme/*"],"members":["loc-open"]}]}}`)
		if len(warnings(st)) != 0 {
			t.Fatalf("pin rule: warnings = %v", warnings(st))
		}
		e.exec(`INSERT INTO namespace_claims (repo_id, pattern, source, scope) SELECT id, 'left-pad', 'manual', 'members' FROM repositories WHERE key = 'loc-open'`)
		st = a.mustCreate("v-claimed", "virtual", "pkgfmt", `"virtual":{"members":["loc-open","rem-pub"]}`)
		if len(warnings(st)) != 0 {
			t.Fatalf("claim: warnings = %v", warnings(st))
		}
	})
	t.Run("R-REPO-005 R-REPO-008 pattern_unscoped_only is returned by the service", func(t *testing.T) {
		st := a.mustCreate("rem-star", "remote", "pkgfmt", `"excludePatterns":["*"]`)
		if !slices.Equal(warnings(st), []string{"pattern_unscoped_only"}) {
			t.Fatalf("warnings = %v", warnings(st))
		}
	})

	t.Run("R-REPO-023 pin rules are rows, read back in the member order, and reach the other replica", func(t *testing.T) {
		st := a.mustCreate("v-partner", "virtual", "pkgfmt", `"virtual":{"members":["loc-auto","rem-pub","rem-partner"],"pinning":{"rules":[
			{"packages":["@partner/*"],"members":["rem-partner"]},
			{"packages":["@partner/internal-*"],"members":["loc-auto"]}]}}`)
		if got := st.Document.Virtual.Pinning.Rules; len(got) != 2 || got[0].Packages[0] != "@partner/*" || got[1].Members[0] != "loc-auto" {
			t.Fatalf("rules = %+v", got)
		}
		if n := e.scalar(`SELECT count(*) FROM virtual_pin_members p JOIN repositories v ON v.id = p.virtual_id WHERE v.key = 'v-partner'`); n != 2 {
			t.Fatalf("%d pin member rows", n)
		}
		var r repository.Repository
		waitFor(t, "B to know the pin rules", 3*poll, func() bool {
			var err error
			r, err = b.reader.ByKey(ctx, "v-partner")
			vs, ok := r.Virtual()
			return err == nil && ok && len(vs.Pinning.Rules) == 2
		})
		route, err := b.routing.Route(ctx, r, repository.RouteRequest{Name: "@partner/sdk"})
		if err != nil {
			t.Fatal(err)
		}
		if got := keysOf(route.Members); !slices.Equal(got, []string{"rem-partner"}) || route.Decision.Kind != repository.DecidedByPin {
			t.Fatalf("route = %+v", route)
		}
		// Editing the rules reaches B as well.
		if _, err := a.svc.Patch(ctx, caller, "v-partner", repository.MatchAny(), mergePatch(`{"virtual":{"pinning":{"rules":[]}}}`)); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "B to drop the pin rules", 3*poll, func() bool {
			r, err := b.reader.ByKey(ctx, "v-partner")
			vs, _ := r.Virtual()
			return err == nil && len(vs.Pinning.Rules) == 0
		})
		if n := e.scalar(`SELECT count(*) FROM virtual_pin_rules`); n != 1 { // v-pinned keeps its rule
			t.Fatalf("%d pin rules remain", n)
		}
	})
	t.Run("R-REPO-023 a pin rule member that is deleted is detached from the rule", func(t *testing.T) {
		a.mustCreate("rem-gone", "remote", "pkgfmt", "")
		a.mustCreate("v-gone", "virtual", "pkgfmt", `"virtual":{"members":["loc-auto","rem-gone"],"pinning":{"rules":[{"packages":["x"],"members":["rem-gone"]}]}}`)
		if _, err := a.svc.Delete(ctx, caller, "rem-gone", false); err == nil {
			t.Fatal("deleted a repository that a pin rule names")
		}
		w := e.newReplica("pin-w", replicaOpts{role: "worker", poll: poll, catalog: pkgCatalog()})
		res, err := a.svc.Delete(ctx, caller, "rem-gone", true)
		if err != nil {
			t.Fatal(err)
		}
		st, _ := a.svc.Get(ctx, "v-gone")
		rules := st.Document.Virtual.Pinning.Rules
		if len(rules) != 1 || len(rules[0].Members) != 0 || !slices.Equal(st.Document.Virtual.Members, []string{"loc-auto"}) {
			t.Fatalf("virtual = %+v", st.Document.Virtual)
		}
		e.waitJob(w.queue, res.JobID)
	})
}
