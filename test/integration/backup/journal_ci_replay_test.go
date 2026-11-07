//go:build integration

package backup

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/ci"
	"github.com/guided-traffic/trove/internal/ci/claims"
	"github.com/guided-traffic/trove/internal/ci/reason"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/journal"
)

// R-CI-020: the issuer and mapping entries of the revocation journal replayed into the database a restore brings back,
// with the fall-through suspension of Q-CI-11.

var ciAdmin = authz.Subject{Kind: "user", Name: "admin"}

// ciServices are the issuer and mapping services of one side, as a replica builds them (internal/app/wire_ci.go).
type ciServices struct {
	issuers  *ci.Service
	mappings *ci.MappingService
}

func newCIServices(t *testing.T, s *twinSide) ciServices {
	t.Helper()
	cfg := config.CI{
		Token:       config.CIToken{DefaultTTL: 15 * time.Minute, MaxTTL: time.Hour, MinTTL: 30 * time.Second},
		Mappings:    config.CIMappings{MaxPerIssuer: 100, MaxPerProject: 10, CacheSafetyTTL: 10 * time.Minute},
		SigningKeys: config.CISigningKeys{CacheSafetyTTL: 10 * time.Minute, FetchTimeout: 5 * time.Second},
		Replay:      config.CIReplay{WarnAfterExchanges: 10, RetentionAfterExpiry: 15 * time.Minute},
	}
	revoker, err := ci.NewRevoker(ci.RevokerOptions{DB: s.db, Changes: s.changes, Recorder: s.rec})
	if err != nil {
		t.Fatal(err)
	}
	mappings, err := ci.NewMappings(ci.MappingsOptions{DB: s.db, Changes: s.changes, Recorder: s.rec, Revoker: revoker, Config: cfg, BaseURL: "https://trove.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	issuers, err := ci.New(ci.Options{DB: s.db, Changes: s.changes, Recorder: s.rec, Config: cfg, BaseURL: "https://trove.example.com", Revoker: revoker, Mappings: mappings})
	if err != nil {
		t.Fatal(err)
	}
	return ciServices{issuers: issuers, mappings: mappings}
}

func node(t *testing.T, s string) claims.Node {
	t.Helper()
	n, err := claims.ParseCondition([]byte(s))
	if err != nil {
		t.Fatalf("condition %s: %v", s, err)
	}
	return n
}

func (c ciServices) issuer(t *testing.T, name string) ci.Issuer {
	t.Helper()
	is, err := c.issuers.Create(context.Background(), ciAdmin, ci.IssuerInput{
		Name: name, Kind: ci.KindGitLab, Issuer: "https://" + name + ".example.com",
		AllowedAudiences: []string{"https://trove.example.com", "npm:trove.example.com"},
	}, false)
	if err != nil {
		t.Fatalf("issuer %s: %v", name, err)
	}
	return is
}

func (c ciServices) mapping(t *testing.T, issuer string, in ci.MappingInput) ci.Mapping {
	t.Helper()
	res, err := c.mappings.Create(context.Background(), ciAdmin, issuer, in)
	if err != nil {
		t.Fatalf("mapping %s/%s: %v", issuer, in.Name, err)
	}
	return res.Mapping
}

func (c ciServices) replaceMapping(t *testing.T, issuer, name string, f func(*ci.MappingInput)) {
	t.Helper()
	m, err := c.mappings.Get(context.Background(), issuer, name)
	if err != nil {
		t.Fatal(err)
	}
	enabled, opts := m.Enabled, m.Options
	in := ci.MappingInput{
		Name: m.Name, Description: m.Description, Priority: m.Priority, Enabled: &enabled, SubjectKinds: m.SubjectKinds,
		Match: m.Match, Grants: m.Grants, MaxTTLSeconds: m.MaxTTLSeconds, Options: &opts,
	}
	f(&in)
	if _, err := c.mappings.Replace(context.Background(), ciAdmin, issuer, name, errs.AnyRevision, in); err != nil {
		t.Fatalf("replace %s/%s: %v", issuer, name, err)
	}
}

func readGrant(repo string) claims.Spec {
	return claims.Spec{Inline: []claims.InlineGrant{{Repositories: []string{repo}, Packages: &claims.PatternSet{Include: []string{"@acme/*"}}, Actions: []claims.Action{claims.Read}}}}
}

func publishGrant() claims.Spec {
	return claims.Spec{
		Policies: []string{"ci-read-internal"},
		Inline: []claims.InlineGrant{{
			Repositories: []string{"web/npm-internal"}, Packages: &claims.PatternSet{Include: []string{"@acme/*"}},
			Actions: []claims.Action{claims.Read, claims.Deploy}, Ownership: claims.OwnershipBindProject,
		}},
	}
}

func off() *bool { f := false; return &f }

// mappingRow is what the restored database holds of a mapping.
func (s *twinSide) mappingRow(issuer, name string) (exists, enabled bool, principalStatus string) {
	s.t.Helper()
	n := s.number(`SELECT count(*) FROM ci_mappings m JOIN ci_issuers i ON i.id = m.issuer_id WHERE i.name = $1 AND m.name = $2`, issuer, name)
	if n == 0 {
		return false, false, ""
	}
	on := s.text(`SELECT m.enabled::text FROM ci_mappings m JOIN ci_issuers i ON i.id = m.issuer_id WHERE i.name = $1 AND m.name = $2`, issuer, name)
	st := s.text(`SELECT p.status FROM ci_mappings m JOIN ci_issuers i ON i.id = m.issuer_id JOIN principal p ON p.id = m.principal_id WHERE i.name = $1 AND m.name = $2`, issuer, name)
	return true, on == "true", st
}

func (s *twinSide) issuerEnabled(name string) bool {
	return s.text(`SELECT enabled::text FROM ci_issuers WHERE name = $1`, name) == "true"
}

func TestR_CI_020_IssuerAndMappingEntriesReplayIntoTheRestoredDatabase(t *testing.T) {
	tw := newTwin(t)
	src := tw.open(tw.src)
	src.exec("INSERT INTO project (id, key, display_name) VALUES (gen_random_uuid(), 'web', 'web')")
	src.exec("INSERT INTO repositories (key, class, format, config, project_id) SELECT 'npm-internal', 'local', 'npm', '{}'::jsonb, id FROM project WHERE key = 'web'")
	src.exec("INSERT INTO authz_policy (id, name) VALUES (gen_random_uuid(), 'ci-read-internal')")
	svc := newCIServices(t, src)
	ctx := context.Background()

	// Issuer A: the tier of R-CI-009's example, a disabled mapping and a project's tier beside it.
	svc.issuer(t, "gitlab-a")
	svc.mapping(t, "gitlab-a", ci.MappingInput{
		Name: "acme-mr-readonly", Priority: 10, Grants: readGrant("web/npm-internal"),
		Match: node(t, `{"all": [{"claim": "namespace_id", "equals": "4711"}, {"claim": "pipeline_source", "equals": "merge_request_event"}]}`),
	})
	svc.mapping(t, "gitlab-a", ci.MappingInput{
		Name: "acme-release-publish", Priority: 100, Grants: publishGrant(),
		Match: node(t, `{"all": [{"claim": "namespace_id", "equals": "4711"}, {"claim": "ref_protected", "equals": true}]}`),
	})
	svc.mapping(t, "gitlab-a", ci.MappingInput{
		Name: "acme-off", Priority: 500, Enabled: off(), Grants: readGrant("web/npm-internal"),
		Match: node(t, `{"all": [{"claim": "namespace_id", "equals": "4711"}]}`),
	})
	svc.mapping(t, "gitlab-a", ci.MappingInput{
		Name: "acme-read", Priority: 900, Grants: readGrant("web/npm-internal"),
		Match: node(t, `{"all": [{"claim": "namespace_id", "equals": "4711"}]}`),
	})
	if _, err := svc.mappings.CreateIn(ctx, ciAdmin, "web", ci.MappingInput{
		Issuer: "gitlab-a", Name: "web-read", Priority: 10, Grants: readGrant("npm-internal"),
		Match: node(t, `{"all": [{"claim": "namespace_id", "equals": "4711"}]}`),
	}); err != nil {
		t.Fatalf("project mapping: %v", err)
	}
	// Issuer B: a publish mapping that is narrowed and then changed in its condition, and a later mapping of the tier.
	svc.issuer(t, "gitlab-b")
	svc.mapping(t, "gitlab-b", ci.MappingInput{
		Name: "publish", Priority: 100, Grants: publishGrant(),
		Match: node(t, `{"all": [{"claim": "namespace_id", "equals": "4711"}, {"claim": "ref_protected", "equals": true}, {"claim": "ref_type", "equals": "tag"}]}`),
	})
	svc.mapping(t, "gitlab-b", ci.MappingInput{
		Name: "later", Priority: 200, Grants: readGrant("web/npm-internal"),
		Match: node(t, `{"all": [{"claim": "namespace_id", "equals": "4711"}]}`),
	})
	// Issuer C is narrowed; issuer D is disabled with a CI token outstanding; issuer E is deleted with its mappings.
	svc.issuer(t, "gitlab-c")
	isD := svc.issuer(t, "gitlab-d")
	mD := svc.mapping(t, "gitlab-d", ci.MappingInput{Name: "d-read", Priority: 10, Grants: readGrant("web/npm-internal"), Match: node(t, `{"all": [{"claim": "namespace_id", "equals": "4711"}]}`)})
	token := src.text(`INSERT INTO api_token (id, type, secret_sha256, hint, principal_id, description, scope, created_via, expires_at)
		VALUES (gen_random_uuid(), 'cit', decode(md5(random()::text) || md5(random()::text), 'hex'), 'trv_cit_…abcd', $1::uuid, 'CI exchange', '{"inherit":true}', 'ci-exchange', now() + interval '1 hour')
		RETURNING id::text`, mD.PrincipalID)
	src.exec(`INSERT INTO ci_token_issuances (token_id, issuer_id, mapping_ids, mapping_names, subject_kind, project_id, job_id, claims, grants, granted_scope, issued_at, expires_at)
		VALUES ($1::uuid, $2::uuid, ARRAY[$3::uuid], ARRAY['d-read'], 'id_token', '4242', '9001', '{}', '{}', 'read', now(), now() + interval '1 hour')`, token, isD.ID, mD.ID)
	svc.issuer(t, "gitlab-e")
	svc.mapping(t, "gitlab-e", ci.MappingInput{Name: "e-first", Priority: 10, Grants: readGrant("web/npm-internal"), Match: node(t, `{"all": [{"claim": "namespace_id", "equals": "1"}]}`)})
	svc.mapping(t, "gitlab-e", ci.MappingInput{Name: "e-second", Priority: 20, Grants: readGrant("web/npm-internal"), Match: node(t, `{"all": [{"claim": "namespace_id", "equals": "2"}]}`)})
	src.close()
	tw.snapshot() // t

	// After t, on the old installation.
	src = tw.open(tw.src)
	svc = newCIServices(t, src)
	if err := svc.mappings.Delete(ctx, ciAdmin, "gitlab-a", "acme-mr-readonly", errs.AnyRevision); err != nil {
		t.Fatal(err)
	}
	svc.replaceMapping(t, "gitlab-b", "publish", func(in *ci.MappingInput) {
		in.Grants = claims.Spec{Inline: []claims.InlineGrant{{
			Repositories: []string{"web/npm-internal"}, Packages: &claims.PatternSet{Include: []string{"@acme/*"}},
			Actions: []claims.Action{claims.Read}, Ownership: claims.OwnershipBindProject,
		}}}
	})
	svc.replaceMapping(t, "gitlab-b", "publish", func(in *ci.MappingInput) { in.Match.All = in.Match.All[:2] })
	c, err := svc.issuers.Get(ctx, "gitlab-c")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.issuers.Replace(ctx, ciAdmin, "gitlab-c", errs.AnyRevision, ci.IssuerInput{
		Name: "gitlab-c", Kind: ci.KindGitLab, Issuer: c.Issuer, AllowedAudiences: []string{"https://trove.example.com"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.issuers.Replace(ctx, ciAdmin, "gitlab-d", errs.AnyRevision, ci.IssuerInput{
		Name: "gitlab-d", Kind: ci.KindGitLab, Issuer: isD.Issuer, AllowedAudiences: isD.AllowedAudiences, Enabled: off(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.issuers.Delete(ctx, ciAdmin, "gitlab-e", true, errs.AnyRevision); err != nil {
		t.Fatal(err)
	}
	store := newMemStore()
	src.ship(store)
	src.close()

	// The restored database: the state at t, then the replay.
	rst := tw.open(tw.restored)
	rsvc := newCIServices(t, rst)
	mrPipeline := claims.Claims{"namespace_id": "4711", "project_id": "4242", "pipeline_source": "merge_request_event", "ref_protected": true}
	selectA := func() ci.Selection {
		ms, err := rsvc.mappings.List(ctx, "gitlab-a")
		if err != nil {
			t.Fatal(err)
		}
		var system []ci.Mapping
		for _, m := range ms {
			if m.Tier == ci.TierSystem {
				system = append(system, m)
			}
		}
		return ci.Select(ci.MappingSet{Mappings: system}, ci.SubjectIDToken, mrPipeline)
	}
	if sel := selectA(); sel.Failure != "" || sel.Mappings()[0].Name != "acme-mr-readonly" {
		t.Fatalf("at t a merge request pipeline matches acme-mr-readonly: %+v", sel)
	}
	epochs := map[string]int64{cluster.AreaAuthz: rst.epoch(cluster.AreaAuthz), cluster.AreaCIMappings: rst.epoch(cluster.AreaCIMappings), cluster.AreaAuth: rst.epoch(cluster.AreaAuth)}
	eventsAtT := rst.number("SELECT count(*) FROM audit_event")
	outboxAtT := rst.number("SELECT count(*) FROM revocation_outbox")
	sum, printed := rst.replay(store, tw.at, rst.changes)
	got := outcomes(sum)

	t.Run("R-CI-020 a deleted shadowing mapping is disabled, and the later mappings of its tier are suspended", func(t *testing.T) {
		if o := got["ci.mapping.deleted gitlab-a/acme-mr-readonly"]; !slices.Equal(o, []journal.Outcome{journal.Applied}) {
			t.Errorf("acme-mr-readonly: %v, want applied", o)
		}
		for _, name := range []string{"acme-release-publish", "acme-read"} {
			if o := got["ci.mapping.deleted gitlab-a/"+name]; !slices.Equal(o, []journal.Outcome{journal.Suspended}) {
				t.Errorf("%s: %v, want suspended", name, o)
			}
		}
		for _, name := range []string{"acme-mr-readonly", "acme-release-publish", "acme-read", "acme-off"} {
			if exists, on, st := rst.mappingRow("gitlab-a", name); !exists || on || st != "disabled" {
				t.Errorf("%s: exists %v, enabled %v, principal %q: disabled, never deleted", name, exists, on, st)
			}
		}
		if _, ok := got["ci.mapping.deleted gitlab-a/acme-off"]; ok {
			t.Error("a mapping that was disabled already lets nothing fall through and is not reported")
		}
		if exists, on, _ := rst.mappingRow("gitlab-a", "web-read"); !exists || !on {
			t.Error("tiers combine by union: the project's tier is not touched")
		}
		if sel := selectA(); sel.Failure != reason.NoMatchingMapping {
			t.Errorf("a merge request pipeline from a protected source branch: %+v, want no_matching_mapping, not a deploy grant", sel)
		}
	})
	t.Run("R-CI-020 deploy removed from an inline grant is applied; a leaf removed from all suspends the mapping", func(t *testing.T) {
		if o := got["ci.mapping.updated gitlab-b/publish"]; !slices.Equal(o, []journal.Outcome{journal.Applied, journal.Suspended}) {
			t.Errorf("publish: %v, want applied then suspended", o)
		}
		if o := got["ci.mapping.updated gitlab-b/later"]; !slices.Equal(o, []journal.Outcome{journal.Suspended}) {
			t.Errorf("later: %v, want suspended by the fall-through", o)
		}
		m, err := rsvc.mappings.Get(ctx, "gitlab-b", "publish")
		if err != nil {
			t.Fatal(err)
		}
		if m.Enabled || len(m.Grants.Policies) != 0 || !slices.Equal(m.Grants.Inline[0].Actions, []claims.Action{claims.Read}) || len(m.Match.All) != 3 {
			t.Errorf("publish = enabled %v, policies %v, actions %v, %d leaves: deploy and the policy gone, the condition as restored, disabled",
				m.Enabled, m.Grants.Policies, m.Grants.Inline[0].Actions, len(m.Match.All))
		}
		if n := rst.number(`SELECT count(*) FROM authz_policy_entry e JOIN authz_policy p ON p.id = e.policy_id WHERE p.name = 'ci-read-internal' AND e.principal_id = $1::uuid`, m.PrincipalID); n != 0 {
			t.Error("the removed policy still lists the mapping's principal")
		}
		if exists, on, _ := rst.mappingRow("gitlab-b", "later"); !exists || on {
			t.Error("later must be disabled")
		}
	})
	t.Run("R-CI-020 an issuer narrowing is applied, a disable revokes the issuer's tokens, a deleted issuer is disabled with its mappings", func(t *testing.T) {
		if o := got["ci.issuer.updated gitlab-c"]; !slices.Equal(o, []journal.Outcome{journal.Applied}) {
			t.Errorf("gitlab-c: %v, want applied", o)
		}
		if a := rst.text(`SELECT array_to_string(allowed_audiences, ',') FROM ci_issuers WHERE name = 'gitlab-c'`); a != "https://trove.example.com" {
			t.Errorf("gitlab-c audiences %s", a)
		}
		if o := got["ci.issuer.disabled gitlab-d"]; !slices.Equal(o, []journal.Outcome{journal.Applied}) || rst.issuerEnabled("gitlab-d") {
			t.Errorf("gitlab-d: %v, enabled %v: disabled", o, rst.issuerEnabled("gitlab-d"))
		}
		if r := rst.text(`SELECT COALESCE(revocation_reason, '') FROM api_token WHERE id = $1::uuid`, token); r == "" {
			t.Error("the CI token of the disabled issuer is not revoked")
		}
		if o := got["ci.issuer.deleted gitlab-e"]; !slices.Equal(o, []journal.Outcome{journal.Applied}) || rst.issuerEnabled("gitlab-e") {
			t.Errorf("gitlab-e: %v: disabled, never deleted", o)
		}
		if o := got["ci.mapping.deleted gitlab-e/e-first"]; !slices.Equal(o, []journal.Outcome{journal.Applied}) {
			t.Errorf("e-first: %v", o)
		}
		if o := got["ci.mapping.deleted gitlab-e/e-second"]; !slices.Equal(o, []journal.Outcome{journal.Suspended, journal.AlreadyInEffect}) {
			t.Errorf("e-second: %v, want suspended by e-first's deletion, then its own entry in effect", o)
		}
		for _, name := range []string{"e-first", "e-second"} {
			if exists, on, _ := rst.mappingRow("gitlab-e", name); !exists || on {
				t.Errorf("%s must exist and be disabled", name)
			}
		}
	})
	t.Run("R-CI-020 the replay prints every suspended mapping as a line of its own", func(t *testing.T) {
		for _, want := range []string{"gitlab-a/acme-mr-readonly", "gitlab-a/acme-release-publish", "gitlab-a/acme-read"} {
			if !strings.Contains(printed, "ci.mapping.deleted "+want+" ") {
				t.Errorf("no line for %s:\n%s", want, printed)
			}
		}
		if sum.Counts[journal.Suspended] < 5 {
			t.Errorf("%d suspended, want at least the five mappings and issuers suspended above", sum.Counts[journal.Suspended])
		}
	})
	t.Run("R-CI-020 the replayed writes move the change areas; they write no audit event and no journal row of their own", func(t *testing.T) {
		for area, was := range epochs {
			if e := rst.epoch(area); e <= was {
				t.Errorf("area %s: epoch %d, was %d", area, e, was)
			}
		}
		if n := rst.number("SELECT count(*) FROM audit_event") - eventsAtT; n != 0 {
			t.Errorf("%d audit events written, want none (Q-AUTHZ-232)", n)
		}
		if n := rst.number("SELECT count(*) FROM revocation_outbox") - outboxAtT; n != 0 {
			t.Errorf("%d journal rows written, want none: a replayed change never re-enters the journal", n)
		}
	})
	t.Run("R-BAK-024 a second replay applies nothing again", func(t *testing.T) {
		again, _ := rst.replay(store, tw.at, rst.changes)
		for _, r := range again.Results {
			if strings.HasPrefix(r.Event, "ci.") && r.Outcome == journal.Applied {
				t.Errorf("second pass: %s", r.Line())
			}
		}
	})
	t.Run("R-CI-020 a hand-written ci.issuer.updated that adds an audience is skipped, a forged before-state suspends the issuer", func(t *testing.T) {
		cur, err := rsvc.issuers.Get(ctx, "gitlab-c")
		if err != nil {
			t.Fatal(err)
		}
		// The issuer's record as the shipped entry of its narrowing carries it, with the audiences of the hand-written state.
		var shipped json.RawMessage
		_ = store.List(ctx, journal.ListPrefix(twinPrefix), func(key string) error {
			raw, err := store.Get(ctx, key)
			if err != nil {
				return err
			}
			o, err := journal.Decode(raw)
			var e struct {
				Name  string          `json:"name"`
				After json.RawMessage `json:"after"`
			}
			if err == nil && o.Event == audit.EventCIIssuerUpdated && o.Into(&e) && e.Name == "gitlab-c" {
				shipped = e.After
			}
			return nil
		})
		rec := func(auds ...string) map[string]any {
			var m map[string]any
			if err := json.Unmarshal(shipped, &m); err != nil {
				t.Fatalf("the shipped record of gitlab-c: %v", err)
			}
			list := make([]any, len(auds))
			for i, a := range auds {
				list[i] = a
			}
			m["allowedAudiences"] = list
			return m
		}
		forged := newMemStore()
		put := func(before, after map[string]any) string {
			b, _ := json.Marshal(map[string]any{"issuerId": cur.ID, "name": "gitlab-c", "before": before, "after": after})
			return handWritten(t, forged, audit.EventCIIssuerUpdated, b)
		}
		now := cur.AllowedAudiences[0]
		added := put(rec(now), rec(now, "https://new.example.com"))
		forgedBefore := put(rec(now, "https://new.example.com", "https://x.example.com"), rec(now, "https://new.example.com"))
		res, _ := rst.replay(forged, time.Now().Add(-time.Minute), rst.changes)
		want := map[string]journal.Outcome{added: journal.Skipped, forgedBefore: journal.Suspended}
		for _, r := range res.Results {
			if r.Outcome != want[r.ID] {
				t.Errorf("%s: %s, want %s (%s)", r.ID, r.Outcome, want[r.ID], r.Detail)
			}
		}
		after, err := rsvc.issuers.Get(ctx, "gitlab-c")
		if err != nil || after.Enabled || !slices.Equal(after.AllowedAudiences, []string{now}) {
			t.Errorf("gitlab-c = enabled %v, audiences %v, %v: suspended, and no new audience", after.Enabled, after.AllowedAudiences, err)
		}
	})
	t.Run("R-CI-020 a forged after-state that removes a bound or turns the clock skew negative suspends; a genuine narrowing applies", func(t *testing.T) {
		is := rsvc.issuer(t, "gitlab-f")
		ttl := int64(600)
		m := rsvc.mapping(t, "gitlab-f", ci.MappingInput{
			Name: "f-read", Priority: 10, Grants: readGrant("web/npm-internal"), MaxTTLSeconds: &ttl,
			Match: node(t, `{"all": [{"claim": "namespace_id", "equals": "4711"}]}`),
		})
		store := newMemStore()
		mapping := func(before, after map[string]any) string {
			b, _ := json.Marshal(map[string]any{
				"mappingId": m.ID, "issuerId": m.IssuerID, "issuer": m.Issuer, "name": m.Name, "tier": m.Tier,
				"priority": m.Priority, "before": before, "after": after,
			})
			return handWritten(t, store, audit.EventCIMappingUpdated, b)
		}
		issuer := func(before, after map[string]any) string {
			b, _ := json.Marshal(map[string]any{"issuerId": is.ID, "name": is.Name, "before": before, "after": after})
			return handWritten(t, store, audit.EventCIIssuerUpdated, b)
		}
		bound := func(v int64) map[string]any { r := mappingRecordOf(m); r["maxTtlSeconds"] = v; return r }
		skew := func(v int) map[string]any { r := issuerRecordOf(is); r["clockSkewSeconds"] = v; return r }
		want := map[string]journal.Outcome{
			mapping(bound(600), bound(300)): journal.Applied,
			issuer(skew(60), skew(30)):      journal.Applied,
		}
		res, _ := rst.replay(store, time.Now().Add(-time.Minute), rst.changes)
		forged := newMemStore()
		store = forged
		want2 := map[string]journal.Outcome{
			mapping(bound(300), bound(0)): journal.Suspended,
			issuer(skew(30), skew(-30)):   journal.Suspended,
		}
		res2, _ := rst.replay(forged, time.Now().Add(-time.Minute), rst.changes)
		for _, r := range append(res.Results, res2.Results...) {
			w, ok := want[r.ID]
			if !ok {
				w = want2[r.ID]
			}
			if r.Outcome != w {
				t.Errorf("%s %s: %s, want %s (%s)", r.Event, r.Subject, r.Outcome, w, r.Detail)
			}
		}
		if got := rst.text(`SELECT COALESCE(extract(epoch FROM max_ttl)::bigint::text, 'unbounded') || ' ' || enabled::text FROM ci_mappings WHERE id = $1::uuid`, m.ID); got != "300 false" {
			t.Errorf("f-read = %s, want the bound of 300 s kept and the mapping disabled", got)
		}
		if got := rst.text(`SELECT extract(epoch FROM clock_skew)::bigint::text || ' ' || enabled::text FROM ci_issuers WHERE id = $1::uuid`, is.ID); got != "30 false" {
			t.Errorf("gitlab-f = %s, want the skew of 30 s kept and the issuer disabled", got)
		}
	})
	if t.Failed() {
		t.Logf("replay:\n%s", printed)
	}
}

// mappingRecordOf is the record of a mapping as the producer writes it into a journal entry (recordOfMapping).
func mappingRecordOf(m ci.Mapping) map[string]any {
	r := map[string]any{
		"id": m.ID, "name": m.Name, "issuer": m.Issuer, "tier": m.Tier, "description": m.Description, "priority": m.Priority,
		"enabled": m.Enabled, "subjectKinds": m.SubjectKinds, "match": m.Match, "grants": m.Grants, "maxTtlSeconds": m.MaxTTLSeconds,
		"options": m.Options, "version": m.Version,
	}
	if m.Project != "" {
		r["project"] = m.Project
	}
	return r
}

// issuerRecordOf is the record of an issuer as the producer writes it into a journal entry (recordOf).
func issuerRecordOf(is ci.Issuer) map[string]any {
	return map[string]any{
		"id": is.ID, "name": is.Name, "kind": is.Kind, "issuer": is.Issuer, "description": is.Description, "enabled": is.Enabled,
		"discoveryUrl": is.DiscoveryURL, "jwksUri": is.JWKSURI, "allowedAudiences": is.AllowedAudiences, "exclusiveAudience": is.ExclusiveAudience,
		"allowedAlgorithms": is.AllowedAlgorithms, "allowedTyp": is.AllowedTyp, "allowMissingTyp": is.AllowMissingTyp,
		"clockSkewSeconds": is.ClockSkewSeconds, "requiredClaims": is.RequiredClaims, "publicIssuer": is.PublicIssuer, "pinClaims": is.PinClaims,
		"singleUseSubjectTokens": is.SingleUseSubjectTokens, "maxTtlSeconds": is.MaxTTLSeconds, "caBundleRef": is.CABundleRef,
		"useOutboundProxy": is.UseOutboundProxy, "jobTokenFallback": is.JobTokenFallback, "source": is.Source,
		"removedFromConfiguration": is.RemovedFromConfiguration, "version": is.Version,
	}
}
