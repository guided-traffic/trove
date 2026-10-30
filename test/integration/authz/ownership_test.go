//go:build integration

package authz

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/repository"
)

// ownWorld is the publishing side of the team example on two in-process replicas
// that share one database: npm-internal enforces ownership, npm-all writes into
// it, npm-loose does not enforce. team-web and team-mobile both deploy @web/* and
// @acme/* (overlapping patterns, the case ownership exists for), lead holds deploy
// and manage, the service account ci-bot deploys @acme/*, root holds the admin
// role (content:bypass).
type ownWorld struct {
	e      *env
	a, b   *replica
	oa, ob *authz.Ownership
	epoch  int64

	alice, dave, mallory, lead, root, carol, bot authz.Subject

	web, mobile person
}

func newOwnWorld(t *testing.T) *ownWorld {
	t.Helper()
	e := newEnv(t)
	w := &ownWorld{e: e, a: e.newReplica("a", replicaOpts{}), b: e.newReplica("b", replicaOpts{})}
	w.web, w.mobile = e.group("team-web", false), e.group("team-mobile", false)
	w.alice, w.dave = sub(e.user("alice"), w.web), sub(e.user("dave"), w.web)
	w.mallory = sub(e.user("mallory"), w.mobile)
	w.lead, w.root, w.carol = sub(e.user("lead")), sub(e.user("root")), sub(e.user("carol"))
	w.bot = sub(e.serviceAccount("ci-bot"))
	for _, r := range []struct{ key, class, cfg string }{
		{"npm-internal", "local", `{"local":{"packageOwnership":"enforce"}}`},
		{"npm-loose", "local", `{"local":{"packageOwnership":"off"}}`},
		{"npm-secret", "local", `{"local":{"packageOwnership":"enforce"}}`},
		{"npmjs", "remote", `{"remote":{"cacheFillAction":"read"}}`},
		{"npm-frozen", "remote", `{"remote":{"cacheFillAction":"deploy"}}`},
		{"npm-all", "virtual", `{"virtual":{"members":["npm-internal","npmjs"],"deployTarget":"npm-internal"}}`},
	} {
		e.repo(r.key, r.class, "npm", r.cfg)
	}
	ctx := t.Context()
	create := func(in authz.PolicyInput) {
		t.Helper()
		_, ep, err := w.a.svc.Create(ctx, w.root, in)
		if err != nil {
			t.Fatalf("create %s: %v", in.Name, err)
		}
		w.epoch = ep
	}
	pkgs := authz.PatternLists{Include: []string{"@web/*", "@acme/*"}}
	create(authz.PolicyInput{
		Name: "team-web-publish", Resources: authz.Resources{Repositories: []string{"npm-internal", "npm-loose"}, Packages: pkgs},
		Entries: []authz.Entry{{Principal: "group:team-web", Actions: []string{"read", "deploy"}}},
	})
	create(authz.PolicyInput{
		Name: "team-mobile-publish", Resources: authz.Resources{Repositories: []string{"npm-internal", "npm-loose"}, Packages: pkgs},
		Entries: []authz.Entry{{Principal: "group:team-mobile", Actions: []string{"read", "deploy"}}},
	})
	create(authz.PolicyInput{
		Name: "lead", Resources: authz.Resources{Repositories: []string{"npm-internal"}},
		Entries: []authz.Entry{{Principal: "user:lead", Actions: []string{"read", "deploy", "manage"}}},
	})
	create(authz.PolicyInput{
		Name: "gate", Resources: authz.Resources{Repositories: []string{"npm-all"}},
		Entries: []authz.Entry{{Principal: "group:team-web", Actions: []string{"read"}}, {Principal: "group:team-mobile", Actions: []string{"read"}}},
	})
	create(authz.PolicyInput{
		Name: "bot", Resources: authz.Resources{Repositories: []string{"npm-internal"}, Packages: authz.PatternLists{Include: []string{"@acme/*"}}},
		Entries: []authz.Entry{{Principal: "sa:ci-bot", Actions: []string{"read", "deploy"}}},
	})
	create(authz.PolicyInput{
		Name: "carol-frozen", Resources: authz.Resources{Repositories: []string{"npm-frozen"}},
		Entries: []authz.Entry{{Principal: "user:carol", Actions: []string{"read", "deploy"}}},
	})
	_, ep, err := w.a.svc.BindRole(ctx, w.root, authz.RoleAdmin, "user:root")
	if err != nil {
		t.Fatal(err)
	}
	w.epoch = ep
	waitFor(t, "both replicas applying the policies", 10*time.Second, func() bool {
		return w.a.engine.Epoch() >= w.epoch && w.b.engine.Epoch() >= w.epoch
	})
	if w.oa, err = authz.NewOwnership(authz.OwnershipOptions{Engine: w.a.engine, DB: w.a.db, Recorder: w.a.rec}); err != nil {
		t.Fatal(err)
	}
	if w.ob, err = authz.NewOwnership(authz.OwnershipOptions{Engine: w.b.engine, DB: w.b.db, Recorder: w.b.rec}); err != nil {
		t.Fatal(err)
	}
	return w
}

func (w *ownWorld) ownership(r *replica) *authz.Ownership {
	if r == w.b {
		return w.ob
	}
	return w.oa
}

// beginIndex takes the index a request decides on, at the epoch of the last write.
func (w *ownWorld) beginIndex(t *testing.T, r *replica, s authz.Subject, keys ...string) (*authz.Index, error) {
	s.MinEpoch = w.epoch
	return r.engine.Begin(t.Context(), s, keys...)
}

func (w *ownWorld) begin(t *testing.T, r *replica, s authz.Subject, keys ...string) *authz.Index {
	t.Helper()
	idx, err := w.beginIndex(t, r, s, keys...)
	if err != nil {
		t.Fatal(err)
	}
	return idx
}

// publish is what a writer does for a mutating operation on a package: decide
// the action on the request's index, then run the ownership check and the claim
// in its commit transaction (formats contract C-14). hold, when given, runs after
// the claim and before the commit; its error rolls the transaction back. It
// reports every failure as its result, so a test may run it in a goroutine.
func (w *ownWorld) publish(t *testing.T, r *replica, s authz.Subject, repo, name string, ow authz.OwnershipWrite, hold func() error) error {
	ctx := t.Context()
	idx, err := w.beginIndex(t, r, s, repo)
	if err != nil {
		return err
	}
	c := write(repo, name, "", authz.Deploy)
	if d := r.engine.DecideOn(ctx, idx, s, c); !d.Allowed() {
		return d.Err()
	}
	return r.db.InTx(ctx, func(tx meta.Tx) error {
		if _, err := w.ownership(r).EnforceTx(ctx, tx, idx, s, c, ow); err != nil {
			return err
		}
		if hold != nil {
			return hold()
		}
		return nil
	})
}

// jsonHas reports whether a JSON object holds the string want under key.
func jsonHas(raw []byte, key, want string) bool {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return false
	}
	v, ok := m[key].(string)
	return ok && v == want
}

var (
	firstVersion = authz.OwnershipWrite{Publish: true, FirstVersion: true}
	nextVersion  = authz.OwnershipWrite{Publish: true}
	distTag      = authz.OwnershipWrite{}
)

// statusOf is the HTTP status of an error; codeOf (lastadmin_test.go) its code.
func statusOf(err error) int {
	if ee, ok := errs.As(err); ok {
		return ee.Status()
	}
	return 0
}

// owners reads the owners of a name the way GET …/owners does.
func (w *ownWorld) owners(t *testing.T, repo, name string) authz.PackageOwners {
	t.Helper()
	got, err := w.oa.Owners(t.Context(), w.begin(t, w.a, w.root, repo), w.root, repo, name)
	if err != nil {
		t.Fatalf("owners of %s: %v", name, err)
	}
	return got
}

func ownerRefs(p authz.PackageOwners) []string {
	var out []string
	for _, o := range p.Owners {
		out = append(out, o.Owner)
	}
	return out
}

// TestR_AUTHZ_018_TwoReplicasClaimOneNewName: two first publishes of one name by
// principals that do not co-own it, on two replicas with their own connections.
// Replica a has inserted its claim and not committed; replica b's check saw the
// name unclaimed and its insert waits for a's. When a commits, b's insert does
// nothing, b reads the claim and is refused: exactly one succeeds, without a
// retry, an advisory lock or Valkey (rule 5).
func TestR_AUTHZ_018_TwoReplicasClaimOneNewName(t *testing.T) {
	w := newOwnWorld(t)
	ctx := t.Context()
	claimed, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	var errA, errB error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		errA = w.publish(t, w.a, w.alice, "npm-internal", "@acme/frontend", firstVersion, func() error {
			once.Do(func() { close(claimed) })
			<-release
			return nil
		})
	}()
	<-claimed
	waiting := func() int64 {
		return w.e.number("SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND wait_event_type = 'Lock'")
	}
	idx := w.begin(t, w.b, w.mallory, "npm-internal")
	c := write("npm-internal", "@acme/frontend", "", authz.Deploy)
	wg.Add(1)
	go func() {
		defer wg.Done()
		errB = w.b.db.InTx(ctx, func(tx meta.Tx) error {
			chk, err := w.ob.CheckTx(ctx, tx, idx, w.mallory, c, firstVersion)
			if err != nil {
				return err
			}
			if !chk.Claim || chk.State != authz.StateUnclaimed {
				return fmt.Errorf("replica b saw the uncommitted claim of replica a: %+v", chk)
			}
			return w.ob.ClaimTx(ctx, tx, chk)
		})
	}()
	waitFor(t, "replica b's claim waiting for replica a's", 10*time.Second, func() bool { return waiting() >= 1 })
	close(release)
	wg.Wait()
	if errA != nil {
		t.Fatalf("replica a: %v", errA)
	}
	if codeOf(errB) != errs.CodePackageOwnedByOtherOwner || statusOf(errB) != 403 {
		t.Fatalf("replica b: %v", errB)
	}
	if got := ownerRefs(w.owners(t, "npm-internal", "@acme/frontend")); !slices.Equal(got, []string{"group:team-web"}) {
		t.Fatalf("owners %v", got)
	}
	if n := w.e.number("SELECT count(*) FROM package_owner WHERE package_name = '@acme/frontend'"); n != 1 {
		t.Fatalf("%d owner rows", n)
	}
	denied := w.b.rec.ofType(authz.EventDenied)
	if len(denied) != 1 || denied[0].Reason != authz.ReasonOwnership {
		t.Fatalf("denials on b: %+v", denied)
	}
	if len(w.b.rec.ofType(authz.EventOwnershipClaimed)) != 0 || len(w.a.rec.ofType(authz.EventOwnershipClaimed)) != 1 {
		t.Fatal("the claim event belongs to the publish that claimed")
	}

	t.Run("R-AUTHZ-018 a claim that rolls back leaves the name to the waiting publish", func(t *testing.T) {
		claimed, release := make(chan struct{}), make(chan struct{})
		var once sync.Once
		var errA, errB error
		var wg sync.WaitGroup
		wg.Add(2)
		before := waiting()
		go func() {
			defer wg.Done()
			errA = w.publish(t, w.a, w.alice, "npm-internal", "@acme/rollback", firstVersion, func() error {
				once.Do(func() { close(claimed) })
				<-release
				return errRollback
			})
		}()
		<-claimed
		go func() {
			defer wg.Done()
			errB = w.publish(t, w.b, w.mallory, "npm-internal", "@acme/rollback", firstVersion, nil)
		}()
		waitFor(t, "replica b waiting", 10*time.Second, func() bool { return waiting() > before })
		close(release)
		wg.Wait()
		if !errors.Is(errA, errRollback) || errB != nil {
			t.Fatalf("a: %v, b: %v", errA, errB)
		}
		if got := ownerRefs(w.owners(t, "npm-internal", "@acme/rollback")); !slices.Equal(got, []string{"group:team-mobile"}) {
			t.Fatalf("owners %v", got)
		}
	})

	t.Run("R-AUTHZ-018 started at the same moment on two replicas, exactly one of each pair succeeds", func(t *testing.T) {
		for i := range 20 {
			name := fmt.Sprintf("@web/race-%02d", i)
			start := make(chan struct{})
			var results [2]error
			var wg sync.WaitGroup
			for j, p := range []struct {
				r *replica
				s authz.Subject
			}{{w.a, w.alice}, {w.b, w.mallory}} {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					results[j] = w.publish(t, p.r, p.s, "npm-internal", name, firstVersion, nil)
				}()
			}
			close(start)
			wg.Wait()
			if (results[0] == nil) == (results[1] == nil) {
				t.Fatalf("%s: a %v, b %v", name, results[0], results[1])
			}
			for _, err := range results {
				if err != nil && codeOf(err) != errs.CodePackageOwnedByOtherOwner {
					t.Fatalf("%s: %v", name, err)
				}
			}
			if n := w.e.number("SELECT count(*) FROM package_owner WHERE package_name = $1", name); n != 1 {
				t.Fatalf("%s: %d owners", name, n)
			}
		}
	})
}

// errRollback makes a held transaction roll back.
var errRollback = errors.New("rolled back by the test")

// TestR_AUTHZ_018_GroupOwner: a publish allowed by a group grant makes the group
// the owner, so every member publishes later versions; a principal with deploy
// on the same name through another grant is refused.
func TestR_AUTHZ_018_GroupOwner(t *testing.T) {
	w := newOwnWorld(t)
	if err := w.publish(t, w.a, w.alice, "npm-internal", "@web/new-lib", firstVersion, nil); err != nil {
		t.Fatal(err)
	}
	got := w.owners(t, "npm-internal", "@web/new-lib")
	if got.State != authz.StateClaimed || got.Domain != "npm-internal" || got.Format != "npm" || len(got.Owners) != 1 {
		t.Fatalf("owners %+v", got)
	}
	if o := got.Owners[0]; o.Owner != "group:team-web" || o.Display != "team-web" || o.Reason != authz.OwnerFirstPublish || o.AddedBy != "" || o.AddedAt.IsZero() {
		t.Fatalf("owner %+v", o)
	}
	claimed := w.a.rec.ofType(authz.EventOwnershipClaimed)
	if len(claimed) != 1 || claimed[0].Repository != "npm-internal" || claimed[0].Path != "@web/new-lib" {
		t.Fatalf("claim events %+v", claimed)
	}
	claim := w.e.text("SELECT claim_event::text FROM package_ownership WHERE package_name = '@web/new-lib'")
	if !jsonHas(claimed[0].Details, "claim", claim) {
		t.Fatalf("the claim event %s does not carry the claim %s", claimed[0].Details, claim)
	}
	if err := w.publish(t, w.b, w.dave, "npm-internal", "@web/new-lib", nextVersion, nil); err != nil {
		t.Fatalf("R-AUTHZ-018 another member of the owner group: %v", err)
	}
	if err := w.publish(t, w.b, w.dave, "npm-internal", "@web/new-lib", distTag, nil); err != nil {
		t.Fatalf("R-AUTHZ-018 a dist-tag by a member of the owner group: %v", err)
	}
	for _, ow := range []authz.OwnershipWrite{nextVersion, distTag} {
		if err := w.publish(t, w.b, w.mallory, "npm-internal", "@web/new-lib", ow, nil); codeOf(err) != errs.CodePackageOwnedByOtherOwner {
			t.Fatalf("R-AUTHZ-018 a deploy holder outside the owners: %v", err)
		}
	}
	if err := w.publish(t, w.a, w.lead, "npm-internal", "@web/new-lib", nextVersion, nil); err != nil {
		t.Fatalf("R-AUTHZ-018 manage passes: %v", err)
	}
	if err := w.publish(t, w.a, w.root, "npm-internal", "@web/new-lib", nextVersion, nil); err != nil {
		t.Fatalf("R-AUTHZ-018 content:bypass passes: %v", err)
	}
	if got := ownerRefs(w.owners(t, "npm-internal", "@web/new-lib")); !slices.Equal(got, []string{"group:team-web"}) {
		t.Fatalf("R-AUTHZ-018 manage and content:bypass take no ownership: %v", got)
	}

	t.Run("R-AUTHZ-009 a publish through the virtual repository claims in its deploy target", func(t *testing.T) {
		if err := w.publish(t, w.a, w.mallory, "npm-all", "@acme/through", firstVersion, nil); err != nil {
			t.Fatal(err)
		}
		domain := w.e.text("SELECT domain FROM package_ownership WHERE package_name = '@acme/through'")
		if want := "repo:" + w.e.text("SELECT id::text FROM repositories WHERE key = 'npm-internal'"); domain != want {
			t.Fatalf("domain %s, want %s", domain, want)
		}
		if err := w.publish(t, w.a, w.alice, "npm-internal", "@acme/through", nextVersion, nil); codeOf(err) != errs.CodePackageOwnedByOtherOwner {
			t.Fatalf("the claim made through npm-all binds npm-internal: %v", err)
		}
	})
	t.Run("R-AUTHZ-018 a repository with packageOwnership off records no claim", func(t *testing.T) {
		if err := w.publish(t, w.a, w.alice, "npm-loose", "@web/loose", firstVersion, nil); err != nil {
			t.Fatal(err)
		}
		if err := w.publish(t, w.b, w.mallory, "npm-loose", "@web/loose", nextVersion, nil); err != nil {
			t.Fatal(err)
		}
		if n := w.e.number("SELECT count(*) FROM package_ownership WHERE package_name = '@web/loose'"); n != 0 {
			t.Fatalf("%d claim rows", n)
		}
	})
}

// importVersion stores a package with one version in a repository the way an
// import before ownership existed would have.
func (w *ownWorld) importVersion(repo, name, version string) {
	pkg := w.e.text("INSERT INTO packages (repo_id, name, display_name) SELECT id, $2, $2 FROM repositories WHERE key = $1 RETURNING id::text", repo, name)
	w.e.exec("INSERT INTO package_versions (package_id, version, display_version) VALUES ($1::bigint, $2, $2)", pkg, version)
}

// TestR_AUTHZ_018_UnclaimedNameWithVersionsNeedsManage is rule 3: nobody takes over
// an existing package merely by holding deploy; a publish by a holder of manage
// claims it by rule 2.
func TestR_AUTHZ_018_UnclaimedNameWithVersionsNeedsManage(t *testing.T) {
	w := newOwnWorld(t)
	w.importVersion("npm-internal", "@acme/util", "1.0.0")
	if err := w.publish(t, w.a, w.alice, "npm-internal", "@acme/util", nextVersion, nil); codeOf(err) != errs.CodePackageOwnedByOtherOwner {
		t.Fatalf("a deploy holder took over an imported package: %v", err)
	}
	if err := w.publish(t, w.a, w.lead, "npm-internal", "@acme/util", distTag, nil); err != nil {
		t.Fatalf("a dist-tag by the manage holder: %v", err)
	}
	if n := w.e.number("SELECT count(*) FROM package_ownership WHERE package_name = '@acme/util'"); n != 0 {
		t.Fatal("a change that is no publish claimed the name")
	}
	if err := w.publish(t, w.b, w.lead, "npm-internal", "@acme/util", nextVersion, nil); err != nil {
		t.Fatalf("the manage holder's publish: %v", err)
	}
	if got := ownerRefs(w.owners(t, "npm-internal", "@acme/util")); !slices.Equal(got, []string{"user:lead"}) {
		t.Fatalf("owners %v", got)
	}
	if err := w.publish(t, w.a, w.alice, "npm-internal", "@acme/util", nextVersion, nil); codeOf(err) != errs.CodePackageOwnedByOtherOwner {
		t.Fatalf("after the claim: %v", err)
	}
}

// TestR_AUTHZ_018_PrecheckIsReadOnly: the early form of the check refuses the caller the commit would
// refuse, claims nothing, holds no lock, and passes the caller the commit would allow.
func TestR_AUTHZ_018_PrecheckIsReadOnly(t *testing.T) {
	w := newOwnWorld(t)
	ctx := t.Context()
	precheck := func(r *replica, s authz.Subject, name string, ow authz.OwnershipWrite) error {
		idx := w.begin(t, r, s, "npm-internal")
		return w.ownership(r).PrecheckWrite(ctx, idx, s, write("npm-internal", name, "", authz.Deploy), ow)
	}
	claims := func() int64 { return w.e.number("SELECT count(*) FROM package_ownership") }

	t.Run("R-AUTHZ-018 a first version passes and claims nothing", func(t *testing.T) {
		before := claims()
		if err := precheck(w.a, w.alice, "@acme/early", authz.OwnershipWrite{FirstVersion: true}); err != nil {
			t.Fatal(err)
		}
		if claims() != before {
			t.Fatal("the precheck claimed the name")
		}
	})
	t.Run("R-AUTHZ-018 a name with versions and no owner is refused to a deploy holder and passes the manage holder", func(t *testing.T) {
		w.importVersion("npm-internal", "@acme/imported", "1.0.0")
		if err := precheck(w.a, w.alice, "@acme/imported", authz.OwnershipWrite{}); codeOf(err) != errs.CodePackageOwnedByOtherOwner || statusOf(err) != 403 {
			t.Fatalf("a deploy holder: %v", err)
		}
		if err := precheck(w.b, w.lead, "@acme/imported", authz.OwnershipWrite{}); err != nil {
			t.Fatalf("the manage holder: %v", err)
		}
	})
	t.Run("R-AUTHZ-018 a name another owner holds is refused, and the refusal is audited like the commit's", func(t *testing.T) {
		if err := w.publish(t, w.a, w.alice, "npm-internal", "@acme/held", firstVersion, nil); err != nil {
			t.Fatal(err)
		}
		before := len(w.b.rec.ofType(authz.EventDenied))
		if err := precheck(w.b, w.mallory, "@acme/held", authz.OwnershipWrite{}); codeOf(err) != errs.CodePackageOwnedByOtherOwner {
			t.Fatalf("%v", err)
		}
		if got := w.b.rec.ofType(authz.EventDenied); len(got) != before+1 || got[len(got)-1].Reason != authz.ReasonOwnership {
			t.Fatalf("denials %+v", got)
		}
		if err := precheck(w.a, w.alice, "@acme/held", authz.OwnershipWrite{}); err != nil {
			t.Fatalf("the owner: %v", err)
		}
	})
}

// TestR_AUTHZ_019_OwnerOperations: pre-assignment, adding and removing owners,
// last_owner and release, the CI restriction, and the answers of refused callers.
func TestR_AUTHZ_019_OwnerOperations(t *testing.T) {
	w := newOwnWorld(t)
	ctx := t.Context()
	add := func(r *replica, s authz.Subject, repo, name, ref string) (authz.Owner, error) {
		return w.ownership(r).AddOwner(ctx, w.begin(t, r, s, repo), s, repo, name, ref)
	}
	remove := func(r *replica, s authz.Subject, repo, name, ref string, release bool) error {
		return w.ownership(r).RemoveOwner(ctx, w.begin(t, r, s, repo), s, repo, name, ref, release)
	}

	t.Run("R-AUTHZ-019 a holder of manage pre-assigns a name nobody has published", func(t *testing.T) {
		o, err := add(w.a, w.lead, "npm-internal", "@acme/not-yet", "group:team-web")
		if err != nil || o.Owner != "group:team-web" || o.Reason != authz.OwnerPreAssigned || o.AddedBy != "user:lead" {
			t.Fatalf("owner %+v, %v", o, err)
		}
		if got := w.owners(t, "npm-internal", "@acme/not-yet"); got.State != authz.StateClaimed || len(got.Owners) != 1 || got.Owners[0].Reason != authz.OwnerPreAssigned {
			t.Fatalf("owners %+v", got)
		}
		if err := w.publish(t, w.b, w.bot, "npm-internal", "@acme/not-yet", firstVersion, nil); codeOf(err) != errs.CodePackageOwnedByOtherOwner {
			t.Fatalf("another deploy holder after the pre-assignment: %v", err)
		}
		if err := w.publish(t, w.b, w.dave, "npm-internal", "@acme/not-yet", firstVersion, nil); err != nil {
			t.Fatalf("a member of the pre-assigned group: %v", err)
		}
		if got := w.a.rec.ofType(authz.EventOwnershipAdded); len(got) != 1 {
			t.Fatalf("added events %+v", got)
		}
	})
	t.Run("R-AUTHZ-019 pre-assigning needs manage", func(t *testing.T) {
		if _, err := add(w.a, w.alice, "npm-internal", "@acme/grab", "group:team-web"); statusOf(err) != 403 {
			t.Fatalf("err %v", err)
		}
		if n := w.e.number("SELECT count(*) FROM package_ownership WHERE package_name = '@acme/grab'"); n != 0 {
			t.Fatal("a refused pre-assignment claimed the name")
		}
	})
	t.Run("R-AUTHZ-019 an owner adds an owner; a second add is 409, an unknown owner 422", func(t *testing.T) {
		if err := w.publish(t, w.a, w.alice, "npm-internal", "@web/shared", firstVersion, nil); err != nil {
			t.Fatal(err)
		}
		o, err := add(w.a, w.alice, "npm-internal", "@web/shared", "group:team-mobile")
		if err != nil || o.Reason != authz.OwnerAdded {
			t.Fatalf("owner %+v, %v", o, err)
		}
		if err := w.publish(t, w.b, w.mallory, "npm-internal", "@web/shared", nextVersion, nil); err != nil {
			t.Fatalf("the added owner publishes: %v", err)
		}
		if _, err := add(w.b, w.dave, "npm-internal", "@web/shared", "group:team-mobile"); codeOf(err) != errs.CodeAlreadyExists || statusOf(err) != 409 {
			t.Fatalf("second add %v", err)
		}
		for _, ref := range []string{"user:nobody", "anonymous", "authenticated", "ci:gitlab-internal:project:4712", "nonsense"} {
			_, err := add(w.b, w.dave, "npm-internal", "@web/shared", ref)
			if ee, ok := errs.As(err); !ok || ee.Status() != 422 || len(ee.Fields) != 1 || ee.Fields[0].Code != errs.CodeUnknownPrincipal {
				t.Fatalf("%s: %v", ref, err)
			}
		}
	})
	t.Run("R-AUTHZ-019 removing the only owner is 409 last_owner, release=true releases the name", func(t *testing.T) {
		if _, err := add(w.a, w.lead, "npm-internal", "@acme/solo", "user:lead"); err != nil {
			t.Fatal(err)
		}
		if err := remove(w.a, w.lead, "npm-internal", "@acme/solo", "user:lead", false); codeOf(err) != errs.CodeLastOwner || statusOf(err) != 409 {
			t.Fatalf("err %v", err)
		}
		if got := ownerRefs(w.owners(t, "npm-internal", "@acme/solo")); !slices.Equal(got, []string{"user:lead"}) {
			t.Fatalf("owners after the refusal %v", got)
		}
		if err := remove(w.a, w.lead, "npm-internal", "@acme/solo", "user:lead", true); err != nil {
			t.Fatal(err)
		}
		if got := w.owners(t, "npm-internal", "@acme/solo"); got.State != authz.StateReleased || len(got.Owners) != 0 {
			t.Fatalf("owners %+v", got)
		}
		released := w.a.rec.ofType(authz.EventOwnershipReleased)
		if len(released) != 1 || !jsonHas(released[0].Journal, "package", "@acme/solo") || !jsonHas(released[0].Journal, "principalId", w.lead.ID) {
			t.Fatalf("released events %+v", released)
		}
		if err := w.publish(t, w.b, w.mallory, "npm-internal", "@acme/solo", firstVersion, nil); err != nil {
			t.Fatalf("a released name without versions is free again: %v", err)
		}
		if got := ownerRefs(w.owners(t, "npm-internal", "@acme/solo")); !slices.Equal(got, []string{"group:team-mobile"}) {
			t.Fatalf("owners %v", got)
		}
	})
	t.Run("R-AUTHZ-019 release needs manage, also from an owner", func(t *testing.T) {
		if err := remove(w.b, w.mallory, "npm-internal", "@acme/solo", "group:team-mobile", true); statusOf(err) != 403 {
			t.Fatalf("err %v", err)
		}
	})
	t.Run("R-AUTHZ-019 an owner hands a package over: add the new owner, then remove itself", func(t *testing.T) {
		if _, err := add(w.b, w.mallory, "npm-internal", "@acme/solo", "sa:ci-bot"); err != nil {
			t.Fatal(err)
		}
		if err := remove(w.b, w.mallory, "npm-internal", "@acme/solo", "group:team-mobile", false); err != nil {
			t.Fatal(err)
		}
		removed := w.b.rec.ofType(authz.EventOwnershipRemoved)
		if len(removed) != 1 || !jsonHas(removed[0].Journal, "principalId", w.mobile.ID) {
			t.Fatalf("removed events %+v", removed)
		}
		if err := remove(w.b, w.mallory, "npm-internal", "@acme/solo", "sa:ci-bot", false); statusOf(err) != 403 {
			t.Fatalf("a former owner removed the new one: %v", err)
		}
	})
	t.Run("R-AUTHZ-019 two concurrent removals of the last two owners leave one owner", func(t *testing.T) {
		if _, err := add(w.a, w.lead, "npm-internal", "@acme/pair", "group:team-web"); err != nil {
			t.Fatal(err)
		}
		if _, err := add(w.a, w.lead, "npm-internal", "@acme/pair", "group:team-mobile"); err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		var results [2]error
		var wg sync.WaitGroup
		for i, p := range []struct {
			r   *replica
			ref string
		}{{w.a, "group:team-web"}, {w.b, "group:team-mobile"}} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				results[i] = remove(p.r, w.lead, "npm-internal", "@acme/pair", p.ref, false)
			}()
		}
		close(start)
		wg.Wait()
		ok, last := 0, 0
		for _, err := range results {
			switch {
			case err == nil:
				ok++
			case codeOf(err) == errs.CodeLastOwner:
				last++
			default:
				t.Fatalf("err %v", err)
			}
		}
		if ok != 1 || last != 1 || len(w.owners(t, "npm-internal", "@acme/pair").Owners) != 1 {
			t.Fatalf("ok %d, last_owner %d", ok, last)
		}
	})
	t.Run("R-AUTHZ-019 a CI token never changes owners, also when its identity owns the name", func(t *testing.T) {
		mapping := w.e.ciMapping("gitlab-internal/acme-publish")
		ci := authz.Subject{Kind: principal.KindCI, ID: mapping.ID, Name: "ci", Mappings: []string{"ci:" + mapping.Name}}
		if _, err := add(w.a, w.lead, "npm-internal", "@acme/ci-owned", "ci:"+mapping.Name); err != nil {
			t.Fatal(err)
		}
		before := ownerRefs(w.owners(t, "npm-internal", "@acme/ci-owned"))
		if _, err := add(w.a, ci, "npm-internal", "@acme/ci-owned", "group:team-mobile"); codeOf(err) != errs.CodeCITokenRestricted || statusOf(err) != 403 {
			t.Fatalf("add: %v", err)
		}
		if err := remove(w.a, ci, "npm-internal", "@acme/ci-owned", "ci:"+mapping.Name, true); codeOf(err) != errs.CodeCITokenRestricted {
			t.Fatalf("remove: %v", err)
		}
		if after := ownerRefs(w.owners(t, "npm-internal", "@acme/ci-owned")); !slices.Equal(after, before) {
			t.Fatalf("owners changed: %v -> %v", before, after)
		}
	})
	t.Run("R-AUTHZ-016 a refused owner change is 403 whether or not the name or repository exists", func(t *testing.T) {
		for _, target := range []struct{ repo, name string }{{"npm-internal", "@web/shared"}, {"npm-internal", "@nope/nothing"}, {"ghost", "@web/shared"}, {"npmjs", "left-pad"}} {
			if _, err := add(w.a, w.carol, target.repo, target.name, "user:carol"); statusOf(err) != 403 || codeOf(err) != errs.CodeForbidden {
				t.Fatalf("%s/%s: %v", target.repo, target.name, err)
			}
		}
		if _, err := add(w.a, w.root, "npmjs", "left-pad", "user:lead"); statusOf(err) != 404 {
			t.Fatalf("where ownership does not exist a holder of manage gets 404: %v", err)
		}
	})
	t.Run("R-AUTHZ-016 reading the owners needs read; a refusal is the 404 of a missing name", func(t *testing.T) {
		_, refused := w.oa.Owners(ctx, w.begin(t, w.a, w.carol, "npm-internal"), w.carol, "npm-internal", "@web/shared")
		if statusOf(refused) != 404 || codeOf(refused) != errs.CodeNotFound {
			t.Fatalf("refused %v", refused)
		}
		if got := w.owners(t, "npm-internal", "@never/published"); got.State != authz.StateUnclaimed || len(got.Owners) != 0 {
			t.Fatalf("an unclaimed name reads back %+v", got)
		}
		if _, err := w.oa.Owners(ctx, w.begin(t, w.a, w.alice, "npm-loose"), w.alice, "npm-loose", "@web/x"); err != nil {
			t.Fatalf("a repository that does not enforce still has an ownership view: %v", err)
		}
	})
	t.Run("R-AUTHZ-019 the names an identity owns, for its members and for policies:read", func(t *testing.T) {
		list := func(s authz.Subject, ref string, after authz.OwnedCursor, limit int) ([]authz.OwnedPackage, *authz.OwnedCursor, error) {
			return w.oa.OwnedBy(ctx, w.begin(t, w.a, s), s, ref, "", after, limit)
		}
		if _, err := add(w.a, w.root, "npm-secret", "@web/hidden", "group:team-web"); err != nil {
			t.Fatal(err)
		}
		var names []string
		var cur authz.OwnedCursor
		for pages := 0; pages < 20; pages++ {
			page, next, err := list(w.dave, "group:team-web", cur, 1)
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range page {
				if p.Repository != "npm-internal" || p.Format != "npm" {
					t.Fatalf("item %+v", p)
				}
				names = append(names, p.Package)
			}
			if next == nil {
				break
			}
			cur = *next
		}
		for _, want := range []string{"@acme/not-yet", "@web/shared"} {
			if !slices.Contains(names, want) {
				t.Fatalf("%s missing from %v", want, names)
			}
		}
		if slices.Contains(names, "@web/hidden") {
			t.Fatalf("R-AUTHZ-016 a member sees a name of a repository it may not read: %v", names)
		}
		if _, _, err := list(w.mallory, "group:team-web", authz.OwnedCursor{}, 10); statusOf(err) != 403 {
			t.Fatalf("a caller outside the group without policies:read: %v", err)
		}
		page, _, err := list(w.root, "group:team-web", authz.OwnedCursor{}, 10)
		if err != nil || !slices.ContainsFunc(page, func(p authz.OwnedPackage) bool { return p.Package == "@web/hidden" && p.Repository == "npm-secret" }) {
			t.Fatalf("policies:read: %+v, %v", page, err)
		}
	})
}

// deletionWorker runs the repository deletion job of R-REPO-013 on a worker of its
// own, with the hooks of the authorization area as the composition root wires them
// and the ownership hook next to them, and returns the service that enqueues it.
func (w *ownWorld) deletionWorker(t *testing.T) *repository.Service {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	db, err := postgres.Open(ctx, w.e.cfg, "own-worker", "worker", quiet)
	if err != nil {
		t.Fatal(err)
	}
	reg := jobs.NewRegistry()
	var svc *repository.Service
	reg.RegisterKind(repository.DeleteJobKind, repository.DeleteJobSpec(func(ctx context.Context, run *jobs.Run) error {
		return svc.RunDeleteJob(ctx, run)
	}))
	reg.Freeze()
	svc, err = repository.NewService(repository.Options{
		Runner: db, Catalog: integrationFormats(), Recorder: w.a.rec, Notifier: w.a.changes,
		Jobs: jobs.NewQueue(jobs.QueueOptions{DB: db.Main(), Registry: reg, Log: quiet}), Log: quiet,
		Hooks: repository.MultiHooks(authz.NewPolicyHook(w.a.changes), authz.NewOwnershipHook()),
	})
	if err != nil {
		t.Fatal(err)
	}
	worker, err := jobs.NewWorker(jobs.WorkerOptions{
		Role: "worker", Instance: "own-worker", UID: w.e.text("SELECT gen_random_uuid()::text"),
		Main: db.Main(), Control: db.ControlPool(), Registry: reg, WatchdogTimeout: 30 * time.Second,
		Log: quiet, Metrics: prometheus.NewRegistry(),
		Config: config.Jobs{
			Workers:      config.JobsWorkers{Default: 1, Maintenance: 1, Index: 1, Delivery: 1},
			PollInterval: 100 * time.Millisecond, LeaseDuration: 6 * time.Second, HeartbeatInterval: 500 * time.Millisecond,
			ScheduleTick: time.Second, Retention: time.Hour,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		worker.Drain(ctx)
		worker.Quiesce(ctx)
		_ = worker.Stop(ctx)
		db.Close()
	})
	return svc
}

// TestR_AUTHZ_018_DeletedRepositoryTakesItsOwnershipRows: the claims of a
// repository's domain carry no foreign key; the hook of the authorization area
// removes them in the transaction of the deletion job that removes the repository
// row (Q-AUTHZ-156), and the claims of another repository stay.
func TestR_AUTHZ_018_DeletedRepositoryTakesItsOwnershipRows(t *testing.T) {
	w := newOwnWorld(t)
	w.e.repo("npm-doomed", "local", "npm", `{"local":{"packageOwnership":"enforce"}}`)
	doomed := "repo:" + w.e.text("SELECT id::text FROM repositories WHERE key = 'npm-doomed'")
	w.e.exec("INSERT INTO package_ownership (domain, format, package_name, state) VALUES ($1, 'npm', '@web/a', 'claimed')", doomed)
	w.e.exec("INSERT INTO package_owner (id, domain, format, package_name, owner_kind, principal_id, display, reason) VALUES (gen_random_uuid(), $1, 'npm', '@web/a', 'principal', $2::uuid, 'team-web', 'first-publish')", doomed, w.web.ID)
	if err := w.publish(t, w.a, w.alice, "npm-internal", "@web/kept", firstVersion, nil); err != nil {
		t.Fatal(err)
	}
	repos := w.deletionWorker(t)
	if _, err := repos.Delete(t.Context(), repository.Caller{}, "npm-doomed", false); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the deletion job removing the repository row", 15*time.Second, func() bool {
		return w.e.number("SELECT count(*) FROM repositories WHERE key = 'npm-doomed'") == 0
	})
	if n := w.e.number("SELECT count(*) FROM package_ownership WHERE domain = $1", doomed); n != 0 {
		t.Fatalf("%d claims of the deleted repository", n)
	}
	if n := w.e.number("SELECT count(*) FROM package_owner WHERE domain = $1", doomed); n != 0 {
		t.Fatalf("%d owners of the deleted repository", n)
	}
	if n := w.e.number("SELECT count(*) FROM package_ownership WHERE package_name = '@web/kept'"); n != 1 {
		t.Fatal("the claim of another repository went with it")
	}
}

// TestR_AUTHZ_008_PopulateCheckOnPostgreSQL: Carol holds read and deploy on the
// frozen mirror; with her session cookie she does not populate it, with her token
// she does. The policy that gives anonymous read on a remote that fills on read
// is saved with the warning anonymous_populates_remote, whether anonymous access
// is on or off; on the frozen mirror it is saved without.
func TestR_AUTHZ_008_PopulateCheckOnPostgreSQL(t *testing.T) {
	w := newOwnWorld(t)
	ctx := t.Context()
	c := read("npm-frozen", "is-odd", "")
	viaSession, viaToken := w.carol, w.carol
	viaSession.SessionID, viaSession.AuthMethod = "s-1", "session"
	viaToken.TokenID, viaToken.AuthMethod = "t-1", "bearer"
	if d := w.a.engine.PopulateOn(ctx, w.begin(t, w.a, viaSession, "npm-frozen"), viaSession, c); d.Reason != authz.ReasonPopulate || codeOf(d.Err()) != errs.CodePopulateNotPermitted {
		t.Fatalf("session: %+v", d)
	}
	if d := w.a.engine.PopulateOn(ctx, w.begin(t, w.a, viaToken, "npm-frozen"), viaToken, c); !d.Allowed() {
		t.Fatalf("token: %+v", d)
	}
	if denied := w.a.rec.ofType(authz.EventDenied); len(denied) != 1 || denied[0].Reason != authz.ReasonPopulate {
		t.Fatalf("denials %+v", denied)
	}
	for i, on := range []bool{false, true} {
		w.e.anonymous.Store(on)
		for _, tc := range []struct {
			repo string
			warn bool
		}{{"npmjs", true}, {"npm-frozen", false}} {
			p, _, err := w.a.svc.Create(ctx, w.root, authz.PolicyInput{
				Name: fmt.Sprintf("anon-%s-%d", tc.repo, i), Resources: authz.Resources{Repositories: []string{tc.repo}},
				Entries: []authz.Entry{{Principal: "anonymous", Actions: []string{"read"}}},
			})
			if err != nil {
				t.Fatal(err)
			}
			if got := slices.Contains(p.Warnings, authz.WarnAnonymousPopulatesRemote); got != tc.warn {
				t.Fatalf("anonymous access %v, %s: warnings %v", on, tc.repo, p.Warnings)
			}
		}
	}
	w.e.anonymous.Store(false)
}
