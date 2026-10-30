package app

import (
	"context"
	"fmt"
	"net/http"

	v1 "github.com/guided-traffic/trove/internal/api/v1"
	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/listq"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/internal/secrets"
)

// The wiring of the API conventions (WP1.6): the start-up canary of the key ring
// (R-ARCH-021), the cluster-wide rate limits and the CORS and header stage that
// form the chain's stage 7 (R-API-016, R-API-017, R-API-018), and the services
// of the system operations (jobs, tasks, key-ring status).

// wireLimits is the limits area: the replica's cluster limiter set (R-ARCH-034)
// with the live-replica count its fallback divides by, and the two limiters of
// R-AUTH-038: the counter auth.credential_failures in the count-only form (burst
// = auth.rate_limit.failures_per_address in a one-minute window), which the
// authentication stage takes, and auth.flow_starts in the take form (burst =
// auth.rate_limit.starts_per_address per minute), which the login's start route
// and local login take. Only a replica with a client listener has it.
func (a *App) wireLimits(ctx context.Context, in infra) (limitsArea, error) {
	count := &replicaCount{a: a}
	if err := a.startComponent(ctx, count); err != nil {
		return limitsArea{}, err
	}
	ls, err := cluster.NewLimiterSet(cluster.LimiterOptions{Valkey: in.vk, Registry: a.tele.metrics.Registry(), LiveReplicas: count.Get})
	if err != nil {
		return limitsArea{}, err
	}
	n := a.cfg.Auth.RateLimit.FailuresPerAddress
	s := a.cfg.Auth.RateLimit.StartsPerAddress
	return limitsArea{
		set: ls, replicas: count,
		failures:   ls.NewLimiter("auth.credential_failures", cluster.ScopeCluster, float64(n)/60, n),
		flowStarts: ls.NewLimiter("auth.flow_starts", cluster.ScopeCluster, float64(s)/60, s),
	}, nil
}

// wireAPI is the api area: the CORS and header stage and the request limits that
// form the chain's stage 7 (R-API-016, R-API-017, R-API-018).
func (a *App) wireAPI(_ context.Context, lim limitsArea, az authzArea) (apiArea, error) {
	if err := requireInputs(AreaAPI, given("limiter set", lim.set), given("authorizer", az.engine)); err != nil {
		return apiArea{}, err
	}
	cfg := a.cfg.API
	access := newBridgeAccess(az.engine, a.cfg.System.BootstrapAnonymousRead)
	// A question, not an operation: a request of a caller without the capability
	// is not a refusal and records no authz.denied (R-AUTHZ-029). It runs in a
	// chain stage before the shared step, so it asks the decision point on an
	// index of its own; the operation's decisions use the step's (Q-API-158).
	isAdmin := func(r *http.Request) bool {
		ok, err := v1.Holds(r.Context(), access, string(authz.ContentBypass))
		return err == nil && ok
	}
	limit, err := v1.NewRateLimit(v1.RateLimitOptions{Config: cfg.RateLimits, Limiters: lim.set, IsAdmin: isAdmin, Registry: a.tele.metrics.Registry()})
	if err != nil {
		return apiArea{}, err
	}
	return apiArea{
		rateLimit: v1.EdgeStage(v1.CORS(cfg.CORS.AllowedOrigins), limit),
		preflight: v1.Preflight(cfg.CORS.AllowedOrigins),
	}, nil
}

// wireKeyRing is the keyring area: the start-up canary of R-ARCH-021. It refuses to start
// when sealed rows reference a key ID the ring lacks ("key ring does not match
// database: missing key <id>"), seeds the canary of every key ID of the ring
// that has none, and refuses to start when the ring cannot open the canary of a
// key ID it holds (the same ID with other key material).
func (a *App) wireKeyRing(ctx context.Context, in infra) (keyRingArea, error) {
	if err := a.verifyKeyRingCanaries(ctx, in.db, in.ring); err != nil {
		return keyRingArea{}, err
	}
	a.reseal.set(resealDeps{db: in.db, ring: in.ring})
	return keyRingArea{}, nil
}

func (a *App) verifyKeyRingCanaries(ctx context.Context, db *postgres.DB, ring *secrets.KeyRing) error {
	st, err := ring.StatusWithCounts(ctx, listq.SealedCounter{DB: db.Main()})
	if err != nil {
		return fmt.Errorf("key ring: %w", err)
	}
	var referenced []string
	for _, k := range st.Keys {
		if k.SealedValues != nil && *k.SealedValues > 0 {
			referenced = append(referenced, k.KeyID)
		}
	}
	if err := ring.RequireKeys("database", referenced); err != nil {
		return err
	}
	return db.InTx(ctx, func(tx meta.Tx) error {
		ptx, ok := postgres.Unwrap(tx)
		if !ok {
			return fmt.Errorf("key ring canary: not a PostgreSQL transaction")
		}
		qs := q.New(ptx)
		rows, err := qs.ListSecretKeyCanaries(ctx)
		if err != nil {
			return err
		}
		have := map[string]string{}
		for _, r := range rows {
			have[r.KeyID] = r.Ciphertext
		}
		for _, id := range ring.KeyIDs() {
			if sealed, ok := have[id]; ok {
				if err := ring.CheckCanary(id, sealed); err != nil {
					return fmt.Errorf("key ring does not match database: %w", err)
				}
				continue
			}
			sealed, err := ring.SealCanary(id)
			if err != nil {
				return err
			}
			if err := qs.InsertSecretKeyCanary(ctx, q.InsertSecretKeyCanaryParams{KeyID: id, Ciphertext: sealed}); err != nil {
				return err
			}
		}
		return nil
	})
}

// auditRecorder returns the audit recorder built at step 4, or a discarding one
// in a shell without infrastructure.
func (a *App) auditRecorder() audit.Recorder {
	if rec := part[infra](a.parts).rec; rec != nil {
		return rec
	}
	return audit.Nop{}
}

// apiServices builds the services of the system operations for the handlers of
// every endpoint of this replica.
func (a *App) apiServices() v1.Services {
	svc := v1.Services{
		Config: a.cfg.API, Maintenance: a.cfg.Maintenance, KeyRing: part[keysArea](a.parts).ring, Audit: a.auditRecorder(),
		TaskArgs: map[string]func() any{
			"gc":      func() any { return &blob.GCJobArgs{} },
			"gc-full": func() any { return &blob.GCJobArgs{} },
		},
	}
	db := a.database()
	if db == nil {
		return svc
	}
	svc.Store = db
	svc.Sealed = listq.SealedCounter{DB: db.Main()}
	svc.Rings = ringSource{a: a}
	if reg, err := a.jobRegistry(); err == nil {
		svc.Jobs = reg
	}
	if queue := a.Queue(); queue != nil {
		svc.Queue = queue
	}
	return svc
}

// ringSource reads the key rings of the replicas from the registry (R-OPS-003).
type ringSource struct{ a *App }

func (s ringSource) Rings(ctx context.Context) ([]v1.ReplicaRing, error) {
	var infos []cluster.ReplicaInfo
	err := s.a.database().Read(ctx, func(c q.DBTX) (err error) {
		infos, err = cluster.ListReplicas(ctx, c, s.a.cfg.System)
		return err
	})
	if err != nil {
		return nil, err
	}
	out := make([]v1.ReplicaRing, 0, len(infos))
	for _, i := range infos {
		role := ""
		if len(i.Roles) > 0 {
			role = i.Roles[0]
		}
		out = append(out, v1.ReplicaRing{Name: i.Name, Role: role, Lost: i.Lost, ActiveKeyID: i.ActiveSecretKeyID, KeyIDs: i.SecretKeyIDs})
	}
	return out, nil
}

// resealDeps are what the task secrets-reencrypt needs: the database and the ring
// whose active key the values are sealed under. The key-ring area sets them.
type resealDeps struct {
	db   *postgres.DB
	ring *secrets.KeyRing
}

// registerSecretsReencrypt registers the manual-only task secrets-reencrypt
// (R-ARCH-021, R-OPS-019): no schedule, and the trigger refuses it while the key
// rings of the live replicas are incompatible (internal/api/v1). The job reseals
// every value of the sealed-column registry that is not under the active key. The
// only sealed column today is the repository secrets: the job walks it in batches
// of one transaction each and fails when a value could not be opened (a key the
// ring lacks, a moved or damaged value); a column registered later without a walk
// of its own fails the job permanently and names it. The handler finds the
// database and the ring through a late value, because the registry is built before
// the key-ring area is wired.
func (a *App) registerSecretsReencrypt(r *jobs.Registry) {
	r.RegisterKind(v1.KindSecretsReencrypt, jobs.KindSpec{
		Queue: "maintenance", DedupMode: jobs.Exclusive, MaxAttempts: 3,
		Handler: a.reseal.handler("key ring", func(ctx context.Context, d resealDeps, run *jobs.Run) error {
			for _, col := range secrets.Columns() {
				if col.String() != repository.RepositorySecrets.String() {
					return jobs.Permanent(fmt.Errorf("re-encryption of %s is not implemented yet", col))
				}
			}
			res, err := repository.ResealSecrets(ctx, d.db, d.ring, 0)
			if err != nil {
				return err
			}
			if err := run.SetResult(map[string]int64{"columns": int64(len(secrets.Columns())), "resealed": res.Resealed, "current": res.Current, "failed": res.Failed}); err != nil {
				return err
			}
			if res.Failed > 0 {
				return jobs.Permanent(fmt.Errorf("%d sealed values could not be opened with the ring of this replica and were left as they are", res.Failed))
			}
			return nil
		}),
	})
	r.RegisterTask(v1.KindSecretsReencrypt, jobs.TaskSpec{ManualOnly: true})
}
