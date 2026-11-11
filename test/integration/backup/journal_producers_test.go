//go:build integration

package backup

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/bastionmain"
	"github.com/guided-traffic/trove/internal/cache"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/journal"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/qname"
	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// The producers of the revocation journal that a security review found without an entry (R-HA-054): a disabled or deleted
// Bastion registration (R-BAS-004, Q-BAS-145), the tokens a disable or deletion of a user or service account revokes
// (R-AUTH-016, R-AUTH-020, Q-AUTH-198), a disabled or deleted repository (R-AUTHZ-031, Q-AUTHZ-238, Q-AUTHZ-239) and a
// cleared auto-join flag (R-AUTH-019, Q-AUTH-198). Each test changes a source database after t through the services the API
// uses, ships its outbox with the journal's shipper, replays the journal into the copy made at t, and shows that the access
// stays removed; a replica that starts on the copy afterwards has empty caches.

var producerAdmin = principal.Caller{Actor: audit.Actor{Kind: "admin-cli", Name: "test"}}

// identity are the identity services of one side, as a replica builds them (internal/app/wire_identity.go), with empty
// caches.
type identity struct {
	manager  *principal.Manager
	tokens   *token.Service
	bastions *bastionmain.Registrations
}

func newIdentity(t *testing.T, s *twinSide) identity {
	t.Helper()
	reg := prometheus.NewRegistry()
	caches, err := cache.NewRegistry(s.changes, reg)
	if err != nil {
		t.Fatal(err)
	}
	metrics, err := principal.NewMetrics(reg)
	if err != nil {
		t.Fatal(err)
	}
	gate := principal.Gate{Bound: 2 * time.Second, Metrics: metrics}
	cfg := config.AuthTokens{
		CacheTTL: 15 * time.Second, RetentionAfterEnd: 2160 * time.Hour, ExpiryNotice: 168 * time.Hour,
		PAT: config.AuthTokenPAT{DefaultTTL: 24 * time.Hour, MaxTTL: 48 * time.Hour}, SAT: config.AuthTokenSAT{DefaultTTL: 24 * time.Hour, MaxTTL: 48 * time.Hour},
		BST: config.AuthTokenBST{DefaultTTL: 24 * time.Hour, MaxTTL: 48 * time.Hour}, MaxActive: config.AuthTokenMax{PAT: 50, SAT: 20, BST: 3},
	}
	principals := principal.NewStore(principal.StoreOptions{DB: s.db, Cache: caches, Gate: gate, SafetyTTL: cfg.CacheTTL})
	settings := token.NewSettingsStore(token.SettingsOptions{DB: s.db, Notifier: s.changes, Recorder: s.rec, Cache: caches, Gate: gate, Tokens: cfg})
	tokens := token.NewService(token.ServiceOptions{
		DB: s.db, Notifier: s.changes, Recorder: s.rec, Principals: principals, Settings: settings, Cache: caches, Gate: gate, Tokens: cfg, Metrics: metrics, Log: quietLog,
	})
	return identity{
		manager:  principal.NewManager(principal.ManagerOptions{DB: s.db, Notifier: s.changes, Recorder: s.rec, BreakGlassName: "breakglass"}),
		tokens:   tokens,
		bastions: bastionmain.NewRegistrations(bastionmain.RegistrationOptions{DB: s.db, Notifier: s.changes, Recorder: s.rec, Tokens: tokens, Max: 16}),
	}
}

// seedUser and seedAccount insert a principal, as the seed of testenv does.
func seedUser(s *twinSide, name string) string {
	id := s.text("INSERT INTO principal (id, kind, name) VALUES (gen_random_uuid(), 'user', $1) RETURNING id::text", name)
	s.exec("INSERT INTO app_user (principal_id, origin) VALUES ($1::uuid, 'admin')", id)
	return id
}

func seedAccount(s *twinSide, name string) string {
	id := s.text("INSERT INTO principal (id, kind, name) VALUES (gen_random_uuid(), 'service_account', $1) RETURNING id::text", name)
	s.exec("INSERT INTO service_account (principal_id) VALUES ($1::uuid)", id)
	return id
}

// mint creates n tokens of the principal through the token service (a PAT for a user, a SAT for a service account) and
// returns their values.
func (id identity) mint(t *testing.T, owner string, typ token.Type, n int) []string {
	t.Helper()
	out := make([]string, 0, n)
	for range n {
		c, err := id.tokens.Create(context.Background(), token.CreateRequest{
			Caller: producerAdmin, Type: typ, OwnerID: owner, Description: "journal test", Via: "admin-cli", Admin: true,
		})
		if err != nil {
			t.Fatalf("create a %s of %s: %v", typ, owner, err)
		}
		out = append(out, c.Secret)
	}
	return out
}

// valid counts the token values the token service accepts.
func (id identity) valid(values []string) int {
	n := 0
	for _, raw := range values {
		if _, err := id.tokens.Validate(context.Background(), raw); err == nil {
			n++
		}
	}
	return n
}

// TestR_AUTH_016_TokensADisableRevokedStayRevokedAfterARestore: a user disabled with revokeTokens, a service account disabled
// with revokeTokens, and a user and a service account deleted after t record one token.revoked with its journal entry per token.
// The replay disables the four principals and revokes their tokens, so an administrator who enables the restored principals
// again does not bring the tokens back (Q-AUTH-198). A disable without revokeTokens revokes nothing and journals no token.
func TestR_AUTH_016_TokensADisableRevokedStayRevokedAfterARestore(t *testing.T) {
	tw := newTwin(t)
	src := tw.open(tw.src)
	ids := newIdentity(t, src)
	alice, bob, kept := seedUser(src, "alice"), seedUser(src, "bob"), seedUser(src, "kept")
	deploy, builder := seedAccount(src, "deploy"), seedAccount(src, "builder")
	values := map[string][]string{
		"alice": ids.mint(t, alice, token.TypePAT, 3), "bob": ids.mint(t, bob, token.TypePAT, 2), "kept": ids.mint(t, kept, token.TypePAT, 1),
		"deploy": ids.mint(t, deploy, token.TypeSAT, 2), "builder": ids.mint(t, builder, token.TypeSAT, 2),
	}
	src.close()
	tw.snapshot() // t

	src = tw.open(tw.src)
	ids = newIdentity(t, src)
	ctx := context.Background()
	if err := ids.manager.DisableUser(ctx, producerAdmin, "alice", principal.DisableOptions{RevokeTokens: true}); err != nil {
		t.Fatal(err)
	}
	if err := ids.manager.DeleteUser(ctx, producerAdmin, "bob"); err != nil {
		t.Fatal(err)
	}
	if err := ids.manager.DisableUser(ctx, producerAdmin, "kept", principal.DisableOptions{RevokeTokens: false}); err != nil {
		t.Fatal(err)
	}
	if err := ids.manager.DisableServiceAccount(ctx, producerAdmin, "deploy", true); err != nil {
		t.Fatal(err)
	}
	if err := ids.manager.DeleteServiceAccount(ctx, producerAdmin, "builder"); err != nil {
		t.Fatal(err)
	}
	t.Run("R-AUTH-029 each revoked token has its token.revoked and journal row", func(t *testing.T) {
		if n := src.number("SELECT count(*) FROM revocation_outbox WHERE event = 'token.revoked'"); n != 3+2+2+2 {
			t.Errorf("%d token.revoked journal rows, want 9 (the tokens of alice, bob, deploy and builder; none of kept)", n)
		}
		if n := src.number("SELECT count(*) FROM audit_event a JOIN revocation_outbox o ON o.id = a.id WHERE a.action = 'token.revoked' AND a.reason = 'principal_disabled'"); n != 9 {
			t.Errorf("%d token.revoked events with their journal row, want 9", n)
		}
	})
	store := newMemStore()
	if n := src.ship(store); n != 9+5 {
		t.Fatalf("%d journal objects shipped, want 14 (nine tokens, two user and two service-account events, the disable of kept)", n)
	}
	src.close()

	rst := tw.open(tw.restored)
	before := newIdentity(t, rst)
	if n := before.valid(slices.Concat(values["alice"], values["bob"], values["deploy"], values["builder"])); n != 9 {
		t.Fatalf("setup: %d of the 9 tokens validate in the database of t", n)
	}
	sum, _ := rst.replay(store, tw.at, rst.changes)
	if sum.Counts[journal.Applied] != 14 {
		t.Errorf("replay: %v, want 14 entries applied", sum.Counts)
	}
	for _, p := range []string{alice, bob, kept, deploy, builder} {
		if st := rst.text("SELECT status FROM principal WHERE id = $1::uuid", p); st != "disabled" {
			t.Errorf("principal %s is %s after the replay", p, st)
		}
	}

	t.Run("R-AUTH-016 enabling the restored principals again brings no token back", func(t *testing.T) {
		after := newIdentity(t, rst)
		for _, name := range []string{"alice", "bob", "kept"} {
			if err := after.manager.EnableUser(ctx, producerAdmin, name); err != nil {
				t.Fatal(err)
			}
		}
		for _, name := range []string{"deploy", "builder"} {
			if err := after.manager.EnableServiceAccount(ctx, producerAdmin, name); err != nil {
				t.Fatal(err)
			}
		}
		fresh := newIdentity(t, rst) // a replica that starts afterwards
		for _, name := range []string{"alice", "bob", "deploy", "builder"} {
			if n := fresh.valid(values[name]); n != 0 {
				t.Errorf("%d tokens of %s validate after the enable", n, name)
			}
		}
		if n := fresh.valid(values["kept"]); n != 1 {
			t.Errorf("the token of kept, disabled without revokeTokens, validates %d times after the enable, want 1: disabling revoked nothing", n)
		}
	})
	t.Run("R-BAK-024 a second replay applies nothing again", func(t *testing.T) {
		again, _ := rst.replay(store, tw.at, rst.changes)
		if again.Counts[journal.Applied] != 5 || again.Counts[journal.AlreadyInEffect] != 9 {
			// The five principals were enabled by hand above, so their disables apply again; the tokens stay revoked.
			t.Errorf("second replay: %v, want the 9 tokens already in effect and the 5 principals disabled again", again.Counts)
		}
	})
}

// stmtTrace counts the statements of a pool, without BEGIN, COMMIT and ROLLBACK.
type stmtTrace struct {
	mu sync.Mutex
	n  int
}

func (s *stmtTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	switch strings.ToLower(strings.TrimSpace(d.SQL)) {
	case "begin", "commit", "rollback", "begin read write", "begin isolation level read committed":
	default:
		s.mu.Lock()
		s.n++
		s.mu.Unlock()
	}
	return ctx
}

func (*stmtTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (s *stmtTrace) take() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := s.n
	s.n = 0
	return n
}

// TestR_AUTH_016_ADisableCostsTheSameStatementsForOneTokenAndForThousands: the token.revoked events and journal rows of a
// revoke-all are written in batches (audit.RecordAllTx), so disabling a user with 1,200 tokens takes at most the four further
// statements of two further batches of 500 audit rows and journal rows, not one round trip per token (Q-AUTH-198).
func TestR_AUTH_016_ADisableCostsTheSameStatementsForOneTokenAndForThousands(t *testing.T) {
	tw := newTwin(t)
	src := tw.open(tw.src)
	small, large := seedUser(src, "small"), seedUser(src, "large")
	src.exec(`INSERT INTO api_token (id, type, secret_sha256, hint, principal_id, description, scope, created_via, expires_at)
		SELECT gen_random_uuid(), 'pat', sha256(convert_to(p.id::text || g::text, 'UTF8')), 'trv_pat_x', p.id, 'bulk', '{}'::jsonb, 'admin-cli', now() + interval '1 day'
		  FROM principal p, generate_series(1, CASE WHEN p.name = 'large' THEN 1200 ELSE 1 END) g
		 WHERE p.kind = 'user' AND p.name IN ('small', 'large')`)
	src.close()

	tr := &stmtTrace{}
	cfg := tw.base
	cfg.Name = tw.src
	db, err := postgres.Open(context.Background(), cfg, "it-journal-traced", "web", testenv.Quiet(), postgres.WithQueryTracer(tr))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	rec, err := audit.New(audit.Options{DB: db, Replica: "it-journal", Config: config.Audit{AsyncBuffer: 100}, Log: testenv.Quiet(), AppLog: testenv.Quiet()})
	if err != nil {
		t.Fatal(err)
	}
	changes, err := cluster.NewChanges(cluster.ChangesOptions{
		DB: db.ControlPool(), Config: config.Cluster{EpochPollInterval: time.Second, EpochMaxStaleness: 20 * time.Second},
		WatchdogTimeout: 30 * time.Second, Log: testenv.Quiet(), Metrics: prometheus.NewRegistry(),
	})
	if err != nil {
		t.Fatal(err)
	}
	manager := principal.NewManager(principal.ManagerOptions{DB: db, Notifier: changes, Recorder: rec, BreakGlassName: "breakglass"})
	ctx := context.Background()
	tr.take()
	if err := manager.DisableUser(ctx, producerAdmin, "small", principal.DisableOptions{RevokeTokens: true}); err != nil {
		t.Fatal(err)
	}
	one := tr.take()
	if err := manager.DisableUser(ctx, producerAdmin, "large", principal.DisableOptions{RevokeTokens: true}); err != nil {
		t.Fatal(err)
	}
	many := tr.take()
	t.Logf("statements of a disable with 1 token: %d, with 1,200 tokens: %d", one, many)
	if many-one > 4 {
		t.Errorf("disabling with 1 token took %d statements, with 1,200 tokens %d: more than the 4 of two further batches", one, many)
	}
	check := tw.open(tw.src)
	for p, want := range map[string]int64{small: 1, large: 1200} {
		if n := check.number("SELECT count(*) FROM revocation_outbox o JOIN audit_event a ON a.id = o.id WHERE o.event = 'token.revoked' AND a.details->>'reason' = 'principal_disabled' AND o.entry->>'tokenId' IN (SELECT id::text FROM api_token WHERE principal_id = $1::uuid)", p); n != want {
			t.Errorf("principal %s: %d token.revoked journal rows, want %d", p, n, want)
		}
	}
}

// TestR_BAS_004_ADisabledOrDeletedRegistrationStaysDisabledAfterARestore: disabling a registration revokes nothing, so
// without its entry a restore to a point before it would accept the registration's tokens again; bastion.disabled and
// bastion.deleted are journaled and the replay disables the registration (Q-BAS-145). An enable after t is not replayed.
func TestR_BAS_004_ADisabledOrDeletedRegistrationStaysDisabledAfterARestore(t *testing.T) {
	tw := newTwin(t)
	src := tw.open(tw.src)
	ids := newIdentity(t, src)
	ctx := context.Background()
	values := map[string]string{}
	for _, name := range []string{"partners", "dmz", "lab"} {
		if _, err := ids.bastions.Create(ctx, producerAdmin, bastionmain.NewRegistration{Name: name, Source: "admin-cli"}); err != nil {
			t.Fatal(err)
		}
		c, err := ids.bastions.CreateToken(ctx, producerAdmin, name, bastionmain.TokenRequest{Via: "admin-cli"})
		if err != nil {
			t.Fatal(err)
		}
		values[name] = c.Secret
	}
	off := false
	if _, err := ids.bastions.Update(ctx, producerAdmin, "lab", bastionmain.Patch{Enabled: &off}, errs.AnyRevision); err != nil {
		t.Fatal(err)
	}
	if n := src.ship(newMemStore()); n != 1 {
		t.Fatalf("setup: %d journal objects of lab's disable before t, want 1", n)
	}
	src.close()
	tw.snapshot() // t: lab is disabled

	src = tw.open(tw.src)
	ids = newIdentity(t, src)
	desc := "the partners' Bastion"
	if _, err := ids.bastions.Update(ctx, producerAdmin, "partners", bastionmain.Patch{Description: &desc, Enabled: &off}, errs.AnyRevision); err != nil {
		t.Fatal(err)
	}
	if err := ids.bastions.Delete(ctx, producerAdmin, "dmz"); err != nil {
		t.Fatal(err)
	}
	on := true
	if _, err := ids.bastions.Update(ctx, producerAdmin, "lab", bastionmain.Patch{Enabled: &on}, errs.AnyRevision); err != nil {
		t.Fatal(err)
	}
	t.Run("R-BAS-004 the disable and the deletion carry a journal row, the description and the enable none", func(t *testing.T) {
		got := map[string]int64{}
		for _, ev := range []string{audit.EventBastionUpdated, audit.EventBastionDisabled, audit.EventBastionEnabled, audit.EventBastionDeleted, audit.EventTokenRevoked} {
			got[ev] = src.number("SELECT count(*) FROM revocation_outbox WHERE event = $1", ev)
		}
		want := map[string]int64{audit.EventBastionUpdated: 0, audit.EventBastionDisabled: 1, audit.EventBastionEnabled: 0, audit.EventBastionDeleted: 1, audit.EventTokenRevoked: 1}
		for ev, n := range want {
			if got[ev] != n {
				t.Errorf("%s: %d journal rows, want %d", ev, got[ev], n)
			}
		}
		if n := src.number("SELECT count(*) FROM audit_event WHERE action IN ('bastion.updated', 'bastion.disabled', 'bastion.enabled')"); n != 4 {
			t.Errorf("%d registration events after t, want 4 (partners: updated and disabled; lab: enabled; plus lab's disable before t)", n)
		}
	})
	store := newMemStore()
	if n := src.ship(store); n != 3 {
		t.Fatalf("%d journal objects shipped, want 3 (the disable, the deletion and its token)", n)
	}
	src.close()

	rst := tw.open(tw.restored)
	if n := newIdentity(t, rst).bastionsAccept(values["partners"], values["dmz"]); n != 2 {
		t.Fatalf("setup: %d of the two Bastion tokens are accepted in the database of t", n)
	}
	sum, _ := rst.replay(store, tw.at, rst.changes)
	got := outcomes(sum)
	if o := got["bastion.disabled partners"]; !slices.Equal(o, []journal.Outcome{journal.Applied}) {
		t.Errorf("bastion.disabled partners: %v, want applied", o)
	}
	if o := got["bastion.deleted dmz"]; !slices.Equal(o, []journal.Outcome{journal.Applied}) {
		t.Errorf("bastion.deleted dmz: %v, want applied (disabled, not deleted)", o)
	}
	for name, want := range map[string]string{"partners": "disabled", "dmz": "disabled", "lab": "disabled"} {
		if st := rst.text("SELECT status FROM principal WHERE kind = 'bastion' AND name = $1", name); st != want {
			t.Errorf("registration %s is %s after the replay, want %s", name, st, want)
		}
	}
	if n := newIdentity(t, rst).bastionsAccept(values["partners"], values["dmz"], values["lab"]); n != 0 {
		t.Errorf("%d Bastion tokens are accepted after the replay, want none", n)
	}
	if d := rst.text("SELECT description FROM bastion b JOIN principal p ON p.id = b.principal_id WHERE p.name = 'partners'"); d != "" {
		t.Errorf("the description %q was replayed: replay changes access, not descriptions", d)
	}
}

// bastionsAccept counts the Bastion token values the token service accepts.
func (id identity) bastionsAccept(values ...string) int {
	n := 0
	for _, raw := range values {
		if _, err := id.tokens.AuthenticateBastion(context.Background(), raw, token.Request{}); err == nil {
			n++
		}
	}
	return n
}

// repositories is the repository service of one side, with a job queue that only takes the deletion job.
func repositories(t *testing.T, s *twinSide) *repository.Service {
	t.Helper()
	reg := jobs.NewRegistry()
	reg.RegisterKind(repository.DeleteJobKind, repository.DeleteJobSpec(func(context.Context, *jobs.Run) error { return nil }))
	svc, err := repository.NewService(repository.Options{
		Runner: s.db, Catalog: catalog.Default(), Recorder: s.rec, Notifier: s.changes,
		Jobs: jobs.NewQueue(jobs.QueueOptions{DB: s.db.Main(), Registry: reg, Log: quietLog}), Log: quietLog,
	})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func setMember(member string, value any) repository.Patcher {
	return func(cur []byte) ([]byte, error) {
		var doc map[string]any
		if err := json.Unmarshal(cur, &doc); err != nil {
			return nil, err
		}
		doc[member] = value
		return json.Marshal(doc)
	}
}

// TestR_AUTHZ_031_ADisabledOrDeletedRepositoryStaysDisabledAfterARestore: disabling a repository records
// repository.disabled and deleting one records repository.deleted, both journaled; the replay disables each in the restored
// database, and a forged entry for an enabled-again repository can only disable it. Enabling a repository after t is not
// journaled and not replayed (Q-AUTHZ-238, Q-AUTHZ-239).
func TestR_AUTHZ_031_ADisabledOrDeletedRepositoryStaysDisabledAfterARestore(t *testing.T) {
	tw := newTwin(t)
	src := tw.open(tw.src)
	src.exec("INSERT INTO project (id, key, display_name) VALUES (gen_random_uuid(), 'web', 'web')")
	for _, key := range []string{"npm-off", "npm-gone", "npm-on"} {
		cfg := "{}"
		if key == "npm-on" {
			cfg = `{"disabled": true}`
		}
		src.exec("INSERT INTO repositories (key, class, format, config, project_id) SELECT $1, 'local', 'npm', $2::text::jsonb, id FROM project WHERE key = 'web'", key, cfg)
	}
	src.close()
	tw.snapshot() // t: npm-on is disabled

	src = tw.open(tw.src)
	svc := repositories(t, src)
	ctx := context.Background()
	c := repository.Caller{Actor: audit.Actor{Kind: "admin-cli", Name: "test"}}
	if _, err := svc.Patch(ctx, c, qname.Repo{Project: "web", Key: "npm-off"}, repository.Precondition{Any: true}, setMember("disabled", true)); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Patch(ctx, c, qname.Repo{Project: "web", Key: "npm-on"}, repository.Precondition{Any: true}, setMember("disabled", false)); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Patch(ctx, c, qname.Repo{Project: "web", Key: "npm-off"}, repository.Precondition{Any: true}, setMember("description", "kept off")); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Delete(ctx, c, qname.Repo{Project: "web", Key: "npm-gone"}, false); err != nil {
		t.Fatal(err)
	}
	t.Run("R-AUTHZ-031 only the disable and the deletion are journaled", func(t *testing.T) {
		rows := map[string]int64{}
		for _, ev := range []string{"repository.updated", "repository.disabled", "repository.deleted"} {
			rows[ev] = src.number("SELECT count(*) FROM revocation_outbox WHERE event = $1", ev)
		}
		if rows["repository.updated"] != 0 || rows["repository.disabled"] != 1 || rows["repository.deleted"] != 1 {
			t.Errorf("journal rows: %v, want one repository.disabled and one repository.deleted", rows)
		}
		if n := src.number("SELECT count(*) FROM audit_event WHERE action = 'repository.updated'"); n != 3 {
			t.Errorf("%d repository.updated events, want 3: the disable keeps its repository.updated", n)
		}
	})
	store := newMemStore()
	if n := src.ship(store); n != 2 {
		t.Fatalf("%d journal objects shipped, want 2", n)
	}
	src.close()

	rst := tw.open(tw.restored)
	disabled := func(key string) string {
		return rst.text("SELECT COALESCE(config->>'disabled', 'false') FROM repositories WHERE key = $1", key)
	}
	if disabled("npm-off") != "false" || disabled("npm-gone") != "false" {
		t.Fatal("setup: npm-off and npm-gone are enabled at t")
	}
	sum, _ := rst.replay(store, tw.at, rst.changes)
	got := outcomes(sum)
	if o := got["repository.disabled web/npm-off"]; !slices.Equal(o, []journal.Outcome{journal.Applied}) {
		t.Errorf("repository.disabled web/npm-off: %v, want applied", o)
	}
	if o := got["repository.deleted web/npm-gone"]; !slices.Equal(o, []journal.Outcome{journal.Applied}) {
		t.Errorf("repository.deleted web/npm-gone: %v, want applied", o)
	}
	for key, want := range map[string]string{"npm-off": "true", "npm-gone": "true", "npm-on": "true"} {
		if got := disabled(key); got != want {
			t.Errorf("%s: disabled = %s after the replay, want %s", key, got, want)
		}
	}
	if d := rst.text("SELECT COALESCE(config->>'description', '') FROM repositories WHERE key = 'npm-off'"); d != "" {
		t.Errorf("the description %q was replayed", d)
	}
	if lc := rst.text("SELECT lifecycle FROM repositories WHERE key = 'npm-gone'"); lc != "active" {
		t.Errorf("npm-gone is %s: replay disables, it never deletes", lc)
	}
	reader := repositories(t, rst)
	st, err := reader.Get(ctx, qname.Repo{Project: "web", Key: "npm-gone"})
	if err != nil || !st.Repository.Disabled {
		t.Errorf("the repository service reads npm-gone as %+v, %v: disabled", st.Repository, err)
	}
	t.Run("R-BAK-024 a second replay applies nothing again", func(t *testing.T) {
		again, _ := rst.replay(store, tw.at, rst.changes)
		if again.Counts[journal.AlreadyInEffect] != 2 || again.Counts[journal.Applied] != 0 {
			t.Errorf("second replay: %v, want both already in effect", again.Counts)
		}
	})
}

// TestR_AUTH_019_AClearedAutoJoinStaysClearedAfterARestore: clearing a group's auto-join flag records group.updated with the
// flag before and after; the replay clears the flag in the restored database, so a user created afterwards does not join the
// group. A flag set after t is skipped and stays off (Q-AUTH-198).
func TestR_AUTH_019_AClearedAutoJoinStaysClearedAfterARestore(t *testing.T) {
	tw := newTwin(t)
	src := tw.open(tw.src)
	ids := newIdentity(t, src)
	ctx := context.Background()
	for name, auto := range map[string]bool{"everyone": true, "staff": false} {
		if _, err := ids.manager.CreateGroup(ctx, producerAdmin, principal.NewGroup{Name: name, AutoJoin: auto}); err != nil {
			t.Fatal(err)
		}
	}
	src.close()
	tw.snapshot() // t

	src = tw.open(tw.src)
	ids = newIdentity(t, src)
	off, on, desc := false, true, "all signed-in users"
	if _, err := ids.manager.UpdateGroup(ctx, producerAdmin, "everyone", principal.GroupPatch{AutoJoin: &off, Expected: errs.AnyRevision}); err != nil {
		t.Fatal(err)
	}
	if _, err := ids.manager.UpdateGroup(ctx, producerAdmin, "everyone", principal.GroupPatch{Description: &desc, Expected: errs.AnyRevision}); err != nil {
		t.Fatal(err)
	}
	if _, err := ids.manager.UpdateGroup(ctx, producerAdmin, "staff", principal.GroupPatch{AutoJoin: &on, Expected: errs.AnyRevision}); err != nil {
		t.Fatal(err)
	}
	store := newMemStore()
	if n := src.ship(store); n != 3 {
		t.Fatalf("%d journal objects shipped, want 3: every group update carries the flag", n)
	}
	src.close()

	rst := tw.open(tw.restored)
	sum, _ := rst.replay(store, tw.at, rst.changes)
	got := outcomes(sum)
	if o := got["group.updated everyone"]; !slices.Equal(o, []journal.Outcome{journal.Applied, journal.AlreadyInEffect}) {
		t.Errorf("group.updated everyone: %v, want applied, then already in effect for the description", o)
	}
	if o := got["group.updated staff"]; !slices.Equal(o, []journal.Outcome{journal.Skipped}) {
		t.Errorf("group.updated staff: %v, want skipped: a flag set after t is not restored", o)
	}
	for name, want := range map[string]string{"everyone": "false", "staff": "false"} {
		if v := rst.text("SELECT g.auto_join::text FROM user_group g JOIN principal p ON p.id = g.principal_id WHERE p.name = $1", name); v != want {
			t.Errorf("%s: auto_join = %s after the replay, want %s", name, v, want)
		}
	}
	if _, err := newIdentity(t, rst).manager.CreateUser(ctx, producerAdmin, principal.NewUser{Name: "newcomer", Email: "newcomer@example.com"}); err != nil {
		t.Fatal(err)
	}
	if n := rst.number("SELECT count(*) FROM group_membership m JOIN principal u ON u.id = m.member_id WHERE u.name = 'newcomer'"); n != 0 {
		t.Errorf("a user created after the restore joined %d groups, want none", n)
	}
}
