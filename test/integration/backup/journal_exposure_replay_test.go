//go:build integration

package backup

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/journal"
	"github.com/guided-traffic/trove/internal/meta"

	_ "github.com/guided-traffic/trove/internal/formats/all" // the formats' name rules and ranges, as in the trove binary
)

// R-EP-020: the exposure entries of the revocation journal replayed into the database a restore brings back.

// The endpoints: customers carries the rule of R-EP-012's example; partners the rules whose include lists the criterion
// of R-EP-020 widens and replaces, so they do not expose @acme/sdk on customers.
const (
	endpointCustomers = "customers"
	endpointPartners  = "partners"
)

func exposureService(s *twinSide) *authz.ExposureService {
	return authz.NewExposureService(authz.ExposureServiceOptions{
		DB: s.db, Changes: s.changes, Recorder: s.rec,
		Ceiling: authz.NewExposureCeiling(map[string]string{endpointCustomers: authz.ExposureModeRules, endpointPartners: authz.ExposureModeRules}, catalog.Default()),
	})
}

var exposureAdmin = authz.Subject{Kind: "user", Name: "admin"}

func createRule(t *testing.T, s *twinSide, svc *authz.ExposureService, endpoint string, in authz.ExposureRuleInput) {
	t.Helper()
	err := s.db.InTx(context.Background(), func(tx meta.Tx) error {
		_, err := svc.Create(context.Background(), tx, exposureAdmin, endpoint, in, nil)
		return err
	})
	if err != nil {
		t.Fatalf("create %s: %v", in.Name, err)
	}
}

func replaceRule(t *testing.T, svc *authz.ExposureService, endpoint, name string, f func(*authz.ExposureRuleInput)) {
	t.Helper()
	r, err := svc.Get(context.Background(), endpoint, name)
	if err != nil {
		t.Fatal(err)
	}
	in := r.ExposureRuleInput
	f(&in)
	if _, _, err := svc.Replace(context.Background(), exposureAdmin, endpoint, name, errs.AnyRevision, in); err != nil {
		t.Fatalf("replace %s: %v", name, err)
	}
}

// acmeSDK is the rule acme-sdk of R-EP-012's example.
func acmeSDK(name string) authz.ExposureRuleInput {
	r := ">=2.0.0"
	return authz.ExposureRuleInput{
		Name: name, Repository: "web/npm-customers", Enabled: true,
		Packages: authz.PatternLists{Include: []string{"@acme/sdk", "@acme/sdk-*"}, Exclude: []string{"@acme/sdk-internal"}},
		Versions: authz.VersionLists{Exclude: []string{"2.0.1"}, Range: &r},
		Tags:     authz.PatternLists{Include: []string{"latest", "lts"}},
	}
}

func onlySDK(name string) authz.ExposureRuleInput {
	return authz.ExposureRuleInput{Name: name, Repository: "web/npm-customers", Enabled: true, Packages: authz.PatternLists{Include: []string{"@acme/sdk"}}}
}

func exposedVersions(t *testing.T, svc *authz.ExposureService, versions ...string) map[string]bool {
	t.Helper()
	res, err := svc.Check(context.Background(), endpointCustomers, authz.ExposureCheckInput{Repository: "web/npm-customers", Package: "@acme/sdk", Versions: versions})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, v := range res.Versions {
		out[v.Value] = v.Exposed
	}
	return out
}

func TestR_EP_020_ExposureEntriesReplayIntoTheRestoredDatabase(t *testing.T) {
	tw := newTwin(t)
	src := tw.open(tw.src)
	src.exec("INSERT INTO project (id, key, display_name) VALUES (gen_random_uuid(), 'web', 'web')")
	src.exec("INSERT INTO repositories (key, class, format, config, project_id) SELECT 'npm-customers', 'local', 'npm', '{}'::jsonb, id FROM project WHERE key = 'web'")
	svc := exposureService(src)
	createRule(t, src, svc, endpointCustomers, acmeSDK("acme-sdk"))
	createRule(t, src, svc, endpointCustomers, acmeSDK("beta"))
	createRule(t, src, svc, endpointPartners, onlySDK("gamma"))
	createRule(t, src, svc, endpointPartners, onlySDK("delta"))
	createRule(t, src, svc, endpointCustomers, acmeSDK("epsilon"))
	src.close()
	tw.snapshot() // t

	// After t, on the old installation; the shipper writes its journal.
	src = tw.open(tw.src)
	svc = exposureService(src)
	replaceRule(t, svc, endpointCustomers, "acme-sdk", func(in *authz.ExposureRuleInput) { in.Versions.Exclude = append(in.Versions.Exclude, "2.0.0") })
	if _, err := svc.Delete(context.Background(), exposureAdmin, endpointCustomers, "beta", errs.AnyRevision); err != nil {
		t.Fatal(err)
	}
	replaceRule(t, svc, endpointPartners, "gamma", func(in *authz.ExposureRuleInput) { in.Packages.Include = []string{"@acme/sdk", "@acme/*"} })
	replaceRule(t, svc, endpointPartners, "delta", func(in *authz.ExposureRuleInput) { in.Packages.Include = []string{"@acme/sdk-*"} })
	replaceRule(t, svc, endpointCustomers, "epsilon", func(in *authz.ExposureRuleInput) { later := ">=2.1.0"; in.Versions.Range = &later })
	store := newMemStore()
	if n := src.ship(store); n != 5 {
		t.Fatalf("%d journal objects shipped, want 5 (four updates and a deletion)", n)
	}
	src.close()

	// The restored database: the state at t, then the replay.
	rst := tw.open(tw.restored)
	rsvc := exposureService(rst)
	if got := exposedVersions(t, rsvc, "2.0.0", "2.1.0"); !got["2.0.0"] || !got["2.1.0"] {
		t.Fatalf("at t, acme-sdk exposes 2.0.0 and 2.1.0: %v", got)
	}
	authzEpoch := rst.epoch(cluster.AreaAuthz)
	outboxAtT := rst.number("SELECT count(*) FROM revocation_outbox")
	eventsAtT := rst.number("SELECT count(*) FROM audit_event")
	sum, printed := rst.replay(store, tw.at, rst.changes)
	got := outcomes(sum)

	t.Run("R-EP-020 a narrowing and a deletion after the snapshot are applied", func(t *testing.T) {
		if o := got["endpoint.exposure.updated customers/acme-sdk"]; !slices.Equal(o, []journal.Outcome{journal.Applied}) {
			t.Errorf("acme-sdk: %v, want applied", o)
		}
		if o := got["endpoint.exposure.deleted customers/beta"]; !slices.Equal(o, []journal.Outcome{journal.Applied}) {
			t.Errorf("beta: %v, want applied", o)
		}
		if v := exposedVersions(t, rsvc, "2.0.0", "2.1.0"); v["2.0.0"] || !v["2.1.0"] {
			t.Errorf("after the replay acme-sdk hides 2.0.0 and exposes 2.1.0: %v", v)
		}
		if n := rst.number("SELECT count(*) FROM endpoint_exposure_rule WHERE name = 'beta'"); n != 0 {
			t.Errorf("beta still exists")
		}
	})
	t.Run("R-EP-020 a widened include list is skipped, a replaced one suspended and read back disabled", func(t *testing.T) {
		if o := got["endpoint.exposure.updated partners/gamma"]; !slices.Equal(o, []journal.Outcome{journal.Skipped}) {
			t.Errorf("gamma: %v, want skipped", o)
		}
		if inc := rst.text("SELECT packages->>'include' FROM endpoint_exposure_rule WHERE name = 'gamma'"); inc != `["@acme/sdk"]` {
			t.Errorf("gamma's include = %s: access granted after t is not restored", inc)
		}
		if o := got["endpoint.exposure.updated partners/delta"]; !slices.Equal(o, []journal.Outcome{journal.Suspended}) {
			t.Errorf("delta: %v, want suspended", o)
		}
		r, err := rsvc.Get(context.Background(), endpointPartners, "delta")
		if err != nil || r.Enabled || !slices.Equal(r.Packages.Include, []string{"@acme/sdk"}) {
			t.Errorf("delta reads back %+v, %v: disabled, its patterns as restored", r.ExposureRuleInput, err)
		}
		var d struct {
			After struct {
				Packages authz.PatternLists `json:"packages"`
			} `json:"after"`
		}
		for _, res := range sum.Results {
			if res.Subject == "partners/delta" && (json.Unmarshal(res.Detail, &d) != nil || !slices.Equal(d.After.Packages.Include, []string{"@acme/sdk-*"})) {
				t.Errorf("the suspended rule carries the journaled after-state: %s", res.Detail)
			}
		}
		if !strings.Contains(printed, `"@acme/sdk-*"`) {
			t.Errorf("the replay prints the after-state of the suspended rule:\n%s", printed)
		}
	})
	t.Run("R-EP-020 a moved range is no narrowing the comparator proves: the rule is suspended", func(t *testing.T) {
		if o := got["endpoint.exposure.updated customers/epsilon"]; !slices.Equal(o, []journal.Outcome{journal.Suspended}) {
			t.Errorf("epsilon: %v, want suspended", o)
		}
		if on := rst.text("SELECT enabled::text FROM endpoint_exposure_rule WHERE name = 'epsilon'"); on != "false" {
			t.Error("epsilon must be disabled")
		}
	})
	t.Run("R-EP-020 the replayed writes move the authz epoch; they write no audit event and no journal row of their own", func(t *testing.T) {
		if e := rst.epoch(cluster.AreaAuthz); e <= authzEpoch {
			t.Errorf("authz epoch %d, was %d", e, authzEpoch)
		}
		if n := rst.number("SELECT count(*) FROM audit_event") - eventsAtT; n != 0 {
			t.Errorf("%d audit events written, want none: the replay lines are the record (Q-AUTHZ-232)", n)
		}
		if n := rst.number("SELECT count(*) FROM revocation_outbox") - outboxAtT; n != 0 {
			t.Errorf("%d journal rows written, want none: a replayed change never re-enters the journal", n)
		}
	})
	t.Run("R-BAK-024 a second replay applies nothing again", func(t *testing.T) {
		again, _ := rst.replay(store, tw.at, rst.changes)
		want := map[string]journal.Outcome{
			"endpoint.exposure.updated customers/acme-sdk": journal.AlreadyInEffect,
			"endpoint.exposure.deleted customers/beta":     journal.AlreadyInEffect,
			"endpoint.exposure.updated partners/gamma":     journal.Skipped,
			"endpoint.exposure.updated partners/delta":     journal.Suspended,
			"endpoint.exposure.updated customers/epsilon":  journal.Suspended,
		}
		got := outcomes(again)
		for k, w := range want {
			if !slices.Equal(got[k], []journal.Outcome{w}) {
				t.Errorf("%s: %v, want %s", k, got[k], w)
			}
		}
	})
	t.Run("R-HA-054 hand-written entries: another rule's ID, a foreign repository, no subject, an unreadable state", func(t *testing.T) {
		acme, err := rsvc.Get(context.Background(), endpointCustomers, "acme-sdk")
		if err != nil {
			t.Fatal(err)
		}
		forged := newMemStore()
		put := func(event string, entry map[string]any) string {
			b, _ := json.Marshal(entry)
			return handWritten(t, forged, event, b)
		}
		wide := map[string]any{
			"enabled": true, "packages": map[string]any{"include": []string{}, "exclude": []string{}},
			"paths": map[string]any{"include": []string{}, "exclude": []string{}}, "versions": map[string]any{"include": []string{}, "exclude": []string{}, "range": nil},
			"prereleases": true, "distTags": map[string]any{"include": []string{}, "exclude": []string{}},
		}
		want := map[string]journal.Outcome{
			put(authz.EventExposureDeleted, map[string]any{"ruleId": "0192f0c4-7a1e-7c3b-9d2e-0000000000ff", "endpoint": endpointCustomers, "name": "acme-sdk", "repositoryId": acme.RepositoryID}):     journal.SubjectUnknown,
			put(authz.EventExposureUpdated, map[string]any{"ruleId": acme.ID, "endpoint": endpointCustomers, "name": "acme-sdk", "repositoryId": acme.RepositoryID + 1, "before": wide, "after": wide}): journal.SubjectUnknown,
			put(authz.EventExposureUpdated, map[string]any{"name": "acme-sdk"}): journal.Skipped,
			put(authz.EventExposureUpdated, map[string]any{"ruleId": acme.ID, "endpoint": endpointCustomers, "name": "acme-sdk", "repositoryId": acme.RepositoryID, "before": wide, "after": map[string]any{"enabled": true, "grants": "all"}}): journal.Suspended,
		}
		res, _ := rst.replay(forged, time.Now().Add(-time.Minute), rst.changes)
		for _, r := range res.Results {
			if r.Outcome != want[r.ID] {
				t.Errorf("forged %s: %s, want %s (%s)", r.ID, r.Outcome, want[r.ID], r.Detail)
			}
		}
		after, err := rsvc.Get(context.Background(), endpointCustomers, "acme-sdk")
		if err != nil || after.Enabled || after.Prereleases || len(after.Packages.Include) != 2 {
			t.Errorf("acme-sdk after the forged entries: %+v, %v: disabled by the suspension, never widened", after.ExposureRuleInput, err)
		}
	})
	t.Run("R-EP-020 a restore without a change log replays as well; only the epoch stays", func(t *testing.T) {
		g, err := rsvc.Get(context.Background(), endpointPartners, "gamma")
		if err != nil {
			t.Fatal(err)
		}
		bare := newMemStore()
		e, _ := json.Marshal(map[string]any{"ruleId": g.ID, "endpoint": endpointPartners, "name": "gamma", "repositoryId": g.RepositoryID})
		handWritten(t, bare, authz.EventExposureDeleted, e)
		epoch := rst.epoch(cluster.AreaAuthz)
		res, _ := rst.replay(bare, time.Now().Add(-time.Minute), nil)
		if len(res.Results) != 1 || res.Results[0].Outcome != journal.Applied {
			t.Fatalf("results %+v, want gamma's deletion applied", res.Results)
		}
		if n := rst.number("SELECT count(*) FROM endpoint_exposure_rule WHERE name = 'gamma'"); n != 0 {
			t.Error("gamma still exists")
		}
		if e := rst.epoch(cluster.AreaAuthz); e != epoch {
			t.Errorf("the epoch moved without a notifier: %d, was %d", e, epoch)
		}
	})
	if t.Failed() {
		t.Logf("replay:\n%s", printed)
	}
}
