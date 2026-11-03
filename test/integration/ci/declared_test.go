//go:build integration

package ci

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/app"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/test/integration/testenv"
	"github.com/guided-traffic/trove/test/oidcstub"
)

// declare runs `trove migrate up` in the test process with ci.issuers set to the entries, exactly as the
// command composes it (app.DeclaredState); a replica that runs picks the change up from the change log.
// The error is the one the command would exit with.
func (e *env) declare(entries ...config.CIIssuer) error {
	e.t.Helper()
	db := e.stack.DatabaseConfig()
	cfg := &config.Config{Database: db}
	cfg.CI.Issuers = entries
	cfg.CI.Token.MaxTTL = time.Hour
	cfg.CI.Mappings.CacheSafetyTTL, cfg.CI.SigningKeys.CacheSafetyTTL = 10*time.Minute, 10*time.Minute
	cfg.Server.BaseURL = "http://trove.test"
	cfg.Cluster = config.Cluster{EpochPollInterval: time.Second, EpochMaxStaleness: 20 * time.Second, ChangeRetention: time.Hour}
	cfg.Health.WatchdogTimeout = 30 * time.Second
	cfg.Audit = config.Audit{AsyncBuffer: 100, RetentionDays: 365}
	cfg.Auth.BreakGlass.Username = "admin"
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, err := postgres.Migrate(ctx, db, postgres.MigrateOptions{
		Instance: "it-migrate", Version: "0.1.0", Log: testenv.Quiet(), AuditRetentionDays: &cfg.Audit.RetentionDays,
		Migrate:  config.Migrate{AdvisoryLockWait: 20 * time.Second, DDLLockTimeout: 2 * time.Second, DDLLockRetries: 3},
		Declared: app.DeclaredState(cfg, "it-migrate", testenv.Quiet()),
	})
	return err
}

func declared(d *double, name string) config.CIIssuer {
	f := false
	ref := d.bundle
	return config.CIIssuer{Name: name, Kind: "gitlab", Issuer: d.stub.Issuer(), PublicIssuer: &f, CABundleRef: &ref}
}

func TestR_CI_001_DeclaredIssuersAreReadOnlyAndFollowTheConfiguration(t *testing.T) {
	d := startDouble(t, oidcstub.Config{})
	e := newEnv(t, options{valkey: true})
	a := e.web("web-a")
	if err := e.declare(declared(d, "gitlab-main")); err != nil {
		t.Fatal(err)
	}

	t.Run("R-CI-001 after trove migrate the issuer is read with source configuration", func(t *testing.T) {
		within(t, "the replica seeing the issuer", 3*time.Second, func() bool {
			_, ok, _ := a.App.CIIssuers().Resolve(t.Context(), d.stub.Issuer())
			return ok
		})
		r := e.do(a, "GET", issuersPath+"/gitlab-main", nil)
		wantStatus(t, r, 200, "")
		got := r.JSON(t)
		if got["source"] != "configuration" || got["removedFromConfiguration"] != false || got["enabled"] != true || got["version"] != float64(1) {
			t.Errorf("issuer = %v", got)
		}
		if e.events("ci.issuer.created") != 1 || e.text("SELECT actor_name FROM audit_event WHERE action = 'ci.issuer.created'") != "configuration" {
			t.Error("the event is not recorded with the actor system:configuration")
		}
		// No row exists yet: the first key set comes from the task or the first exchange.
		if n := e.number("SELECT count(*) FROM ci_issuer_keys"); n != 0 {
			t.Errorf("migrate fetched keys: %d rows", n)
		}
	})

	t.Run("R-CI-001 the API refuses to change or delete it", func(t *testing.T) {
		body := e.do(a, "GET", issuersPath+"/gitlab-main", nil).JSON(t)
		body["description"] = "edited"
		wantStatus(t, e.do(a, "PUT", issuersPath+"/gitlab-main", body, "If-Match", "*"), 409, "issuer_managed_by_configuration")
		body["enabled"] = false
		wantStatus(t, e.do(a, "PUT", issuersPath+"/gitlab-main", body, "If-Match", "*"), 409, "issuer_managed_by_configuration")
		wantStatus(t, e.do(a, "DELETE", issuersPath+"/gitlab-main", nil), 409, "issuer_managed_by_configuration")
		// Its keys can be fetched: that is no change of the issuer.
		wantStatus(t, e.do(a, "POST", issuersPath+"/gitlab-main/keys/refresh", nil), 200, "")
	})

	t.Run("R-CI-001 applying the same declaration again writes nothing; a change replaces by name", func(t *testing.T) {
		events, epoch := e.number("SELECT count(*) FROM audit_event WHERE action LIKE 'ci.issuer.%'"), e.epoch("ci_mappings")
		if err := e.declare(declared(d, "gitlab-main")); err != nil {
			t.Fatal(err)
		}
		if e.number("SELECT count(*) FROM audit_event WHERE action LIKE 'ci.issuer.%'") != events || e.epoch("ci_mappings") != epoch ||
			e.number("SELECT version FROM ci_issuers") != 1 {
			t.Error("an unchanged declaration wrote")
		}
		changed := declared(d, "gitlab-main")
		changed.Description = "the main instance"
		if err := e.declare(changed); err != nil {
			t.Fatal(err)
		}
		if e.number("SELECT version FROM ci_issuers") != 2 || e.events("ci.issuer.updated") != 1 {
			t.Errorf("version %d, updated events %d", e.number("SELECT version FROM ci_issuers"), e.events("ci.issuer.updated"))
		}
		within(t, "the replica reading the new description", 3*time.Second, func() bool {
			return e.do(a, "GET", issuersPath+"/gitlab-main", nil).JSON(t)["description"] == "the main instance"
		})
	})

	t.Run("R-CI-001 removed from ci.issuers the next migrate disables it, and only then may it be deleted", func(t *testing.T) {
		if err := e.declare(); err != nil {
			t.Fatal(err)
		}
		r := e.do(a, "GET", issuersPath+"/gitlab-main", nil)
		got := r.JSON(t)
		if got["enabled"] != false || got["removedFromConfiguration"] != true || got["source"] != "configuration" {
			t.Errorf("issuer = %v", got)
		}
		if e.events("ci.issuer.disabled") != 1 {
			t.Errorf("%d disabled events", e.events("ci.issuer.disabled"))
		}
		// Declared again, it is enabled again and loses the flag.
		if err := e.declare(declared(d, "gitlab-main")); err != nil {
			t.Fatal(err)
		}
		if g := e.do(a, "GET", issuersPath+"/gitlab-main", nil).JSON(t); g["enabled"] != true || g["removedFromConfiguration"] != false {
			t.Errorf("issuer = %v", g)
		}
		if err := e.declare(); err != nil {
			t.Fatal(err)
		}
		wantStatus(t, e.do(a, "DELETE", issuersPath+"/gitlab-main", nil), 204, "")
	})
}

func TestR_CI_001_ADeclaredIssuerThatClashesWithAnAPIIssuerStopsTheMigration(t *testing.T) {
	d, other := startDouble(t, oidcstub.Config{}), startDouble(t, oidcstub.Config{})
	e := newEnv(t, options{valkey: true})
	a := e.web("web-a")
	wantStatus(t, e.do(a, "POST", issuersPath, d.body("gitlab-api")), 201, "")
	before := e.number("SELECT count(*) FROM ci_issuers")

	// The same name.
	clash := declared(other, "gitlab-api")
	err := e.declare(declared(other, "fresh"), clash)
	if err == nil || !strings.Contains(err.Error(), "gitlab-api") || !strings.Contains(err.Error(), "API") {
		t.Errorf("name clash: %v", err)
	}
	// The same issuer URL.
	err = e.declare(declared(other, "fresh"), declared(d, "gitlab-config"))
	if err == nil || !strings.Contains(err.Error(), "gitlab-config") || !strings.Contains(err.Error(), "gitlab-api") {
		t.Errorf("URL clash: %v", err)
	}
	if e.number("SELECT count(*) FROM ci_issuers") != before {
		t.Error("nothing is applied when one entry clashes")
	}
	// An entry the configuration check would refuse is refused here too, naming the member.
	bad := declared(other, "fresh")
	bad.AllowedAlgorithms = []string{"HS256"}
	if err := e.declare(bad); err == nil || !strings.Contains(err.Error(), "allowedAlgorithms") {
		t.Errorf("bad entry: %v", err)
	}
}

// R-CI-001, Q-CI-48: a declared issuer keeps its issuer URL and its kind under its name, as the API keeps them
// (immutable): a changed URL for an existing name stops `trove migrate` with an error that names both URLs and tells
// the operator to declare the new instance under a new name; nothing is applied, so the pins, the package owners
// ci:<issuer>:project:<id> and the mappings stay with the instance they were made for.
func TestR_CI_001_ADeclaredIssuerKeepsItsIssuerURL(t *testing.T) {
	d, other := startDouble(t, oidcstub.Config{}), startDouble(t, oidcstub.Config{})
	e := newEnv(t, options{valkey: true})
	if err := e.declare(declared(d, "gitlab-main")); err != nil {
		t.Fatal(err)
	}
	id := e.text("SELECT id::text FROM ci_issuers WHERE name = 'gitlab-main'")
	moved := declared(other, "gitlab-main")
	err := e.declare(moved)
	if err == nil || !strings.Contains(err.Error(), d.stub.Issuer()) || !strings.Contains(err.Error(), other.stub.Issuer()) || !strings.Contains(err.Error(), "new name") {
		t.Fatalf("a changed issuer URL: %v", err)
	}
	if got := e.text("SELECT issuer FROM ci_issuers WHERE id = $1::uuid", id); got != d.stub.Issuer() {
		t.Errorf("the issuer URL became %s", got)
	}
	t.Run("R-CI-001 the new instance under a new name is applied and the old name is disabled", func(t *testing.T) {
		if err := e.declare(declared(other, "gitlab-next")); err != nil {
			t.Fatal(err)
		}
		if e.text("SELECT enabled::text || removed_from_configuration::text FROM ci_issuers WHERE id = $1::uuid", id) != "falsetrue" {
			t.Error("the removed name is not disabled and flagged")
		}
		if e.text("SELECT issuer FROM ci_issuers WHERE name = 'gitlab-next'") != other.stub.Issuer() {
			t.Error("the new name does not hold the new instance")
		}
	})
}
