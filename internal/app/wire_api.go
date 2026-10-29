package app

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	v1 "github.com/guided-traffic/trove/internal/api/v1"
	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/listq"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/secrets"
)

// The wiring of the API conventions (WP1.6): the start-up canary of the key ring
// (R-ARCH-021), the cluster-wide rate limits and the CORS and header stage that
// form the chain's stage 7 (R-API-016, R-API-017, R-API-018), and the services
// of the system operations (jobs, tasks, key-ring status).

// liveReplicas is the number n of live replicas of this replica's role that the
// rate-limit fallback divides the budget by (R-ARCH-034). It is read from the
// replica registry every heartbeat interval and is 1 until the first read.
type liveReplicas struct{ n atomic.Int32 }

func (l *liveReplicas) Get() int { return max(1, int(l.n.Load())) }

// startLiveReplicaCount refreshes n in the background until ctx ends. A failed
// read keeps the last value: the fallback must not depend on PostgreSQL either.
func (a *App) startLiveReplicaCount(ctx context.Context) *liveReplicas {
	l := &liveReplicas{}
	db := a.database()
	if db == nil {
		return l
	}
	stale := a.cfg.System.HeartbeatStaleAfter.Seconds()
	refresh := func() {
		rctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		_ = db.Read(rctx, func(c q.DBTX) error {
			n, err := q.New(c).CountLiveReplicas(rctx, q.CountLiveReplicasParams{StaleSecs: stale, Role: a.id.Role})
			if err == nil {
				l.n.Store(n)
			}
			return err
		})
	}
	refresh()
	a.bg.Add(1)
	go func() {
		defer a.bg.Done()
		t := time.NewTicker(a.cfg.System.HeartbeatInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				refresh()
			}
		}
	}()
	return l
}

// limiterSet returns the replica's cluster limiter set (R-ARCH-034), built on the
// first call: the identity stage registers its failure throttle on it before the
// API adds the request limiters.
func (a *App) limiterSet(ctx context.Context) (*cluster.LimiterSet, error) {
	if a.m1.limiters != nil {
		return a.m1.limiters, nil
	}
	live := a.startLiveReplicaCount(ctx)
	ls, err := cluster.NewLimiterSet(cluster.LimiterOptions{Valkey: a.valkey(), Registry: a.tele.metrics.Registry(), LiveReplicas: live.Get})
	if err != nil {
		return nil, err
	}
	a.m1.limiters = ls
	return ls, nil
}

// wireFailureLimiter registers the cluster-wide limiters of R-AUTH-038: the
// counter auth.credential_failures in the count-only form (burst =
// auth.rate_limit.failures_per_address in a one-minute window), which the
// authentication stage takes, and auth.flow_starts in the take form (burst =
// auth.rate_limit.starts_per_address per minute), which the login's start route
// and local login take.
func (a *App) wireFailureLimiter(ctx context.Context) error {
	if !a.sub.ClientListener {
		return nil
	}
	ls, err := a.limiterSet(ctx)
	if err != nil {
		return err
	}
	n := a.cfg.Auth.RateLimit.FailuresPerAddress
	a.m1.failureLimiter = ls.NewLimiter("auth.credential_failures", cluster.ScopeCluster, float64(n)/60, n)
	s := a.cfg.Auth.RateLimit.StartsPerAddress
	a.m1.flowStarts = ls.NewLimiter("auth.flow_starts", cluster.ScopeCluster, float64(s)/60, s)
	return nil
}

// wireAPI runs at the api marker of step 8.
func (a *App) wireAPI(ctx context.Context, infra bool) error {
	if infra && a.sub.MainPool {
		if err := a.verifyKeyRingCanaries(ctx); err != nil {
			return err
		}
	}
	if !a.sub.ClientListener {
		return nil
	}
	cfg := a.cfg.API
	reg := a.tele.metrics.Registry()
	ls, err := a.limiterSet(ctx)
	if err != nil {
		return err
	}
	var isAdmin func(*http.Request) bool
	if a.m1.authorizer != nil {
		access := a.access()
		isAdmin = func(r *http.Request) bool { return access.Require(r.Context(), "content:bypass") == nil }
	}
	limit, err := v1.NewRateLimit(v1.RateLimitOptions{Config: cfg.RateLimits, Limiters: ls, IsAdmin: isAdmin, Registry: reg})
	if err != nil {
		return err
	}
	a.m1.rateLimit = v1.EdgeStage(v1.CORS(cfg.CORS.AllowedOrigins), limit)
	return nil
}

// verifyKeyRingCanaries is the start-up canary of R-ARCH-021. It refuses to start
// when sealed rows reference a key ID the ring lacks ("key ring does not match
// database: missing key <id>"), seeds the canary of every key ID of the ring
// that has none, and refuses to start when the ring cannot open the canary of a
// key ID it holds (the same ID with other key material).
func (a *App) verifyKeyRingCanaries(ctx context.Context) error {
	db := a.database()
	if db == nil || a.ring == nil {
		return nil
	}
	st, err := a.ring.StatusWithCounts(ctx, listq.SealedCounter{DB: db.Main()})
	if err != nil {
		return fmt.Errorf("key ring: %w", err)
	}
	var referenced []string
	for _, k := range st.Keys {
		if k.SealedValues != nil && *k.SealedValues > 0 {
			referenced = append(referenced, k.KeyID)
		}
	}
	if err := a.ring.RequireKeys("database", referenced); err != nil {
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
		for _, id := range a.ring.KeyIDs() {
			if sealed, ok := have[id]; ok {
				if err := a.ring.CheckCanary(id, sealed); err != nil {
					return fmt.Errorf("key ring does not match database: %w", err)
				}
				continue
			}
			sealed, err := a.ring.SealCanary(id)
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
	if a.m1.recorder != nil {
		return a.m1.recorder
	}
	return audit.Nop{}
}

// apiServices builds the services of the system operations for the handlers of
// every endpoint of this replica.
func (a *App) apiServices() v1.Services {
	svc := v1.Services{
		Config: a.cfg.API, Maintenance: a.cfg.Maintenance, KeyRing: a.ring, Audit: a.auditRecorder(),
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

// registerSecretsReencrypt registers the manual-only task secrets-reencrypt
// (R-ARCH-021, R-OPS-019): no schedule, and the trigger refuses it while the key
// rings of the live replicas are incompatible (internal/api/v1). The job reseals
// every value of the sealed-column registry that is not under the active key.
// The registry is empty until the first feature that stores a secret registers
// its column, and an empty registry has nothing to reseal; a run that finds a
// registered column fails permanently and says so, because the walk over a
// column is written with the first column (docs/developer/http-api.md).
func registerSecretsReencrypt(r *jobs.Registry) {
	r.RegisterKind(v1.KindSecretsReencrypt, jobs.KindSpec{
		Queue: "maintenance", DedupMode: jobs.Exclusive, MaxAttempts: 3,
		Handler: func(_ context.Context, run *jobs.Run) error {
			cols := secrets.Columns()
			if len(cols) > 0 {
				return jobs.Permanent(fmt.Errorf("re-encryption of %s is not implemented yet", cols[0]))
			}
			return run.SetResult(map[string]int64{"columns": 0, "resealed": 0})
		},
	})
	r.RegisterTask(v1.KindSecretsReencrypt, jobs.TaskSpec{ManualOnly: true})
}
