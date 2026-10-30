package virtual

import (
	"context"
	"crypto/sha256"
	"errors"
	"hash"
	"log/slog"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/cache"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/repository"
)

// DefaultSafetyTTL bounds the age of a merged entry whatever its own period says
// (R-ARCH-018): a missed invalidation can not outlive it.
const DefaultSafetyTTL = 2 * time.Hour

// Signals is the post-commit signal path of the cluster package; *cluster.Signals implements it.
type Signals interface {
	Subscribe(ctx context.Context, area string, h cluster.SignalHandler) (cancel func(), err error)
}

// Options configure [New].
type Options struct {
	Router  Router
	Decider Decider
	Locals  Locals
	// Remotes is the remote engine. Nil means the replica has none: a remote member is then
	// treated as one that cannot be asked, which a request answers with 502 and the explanation
	// with `offline`.
	Remotes Remotes
	// Caches registers the merged-metadata cache (CacheMerged); nil keeps none.
	Caches *cache.Registry
	// CacheBytes is proxy.merged_metadata_cache_bytes; 0 turns the cache off.
	CacheBytes int64
	// SafetyTTL bounds the age of a merged entry; zero is [DefaultSafetyTTL].
	SafetyTTL time.Duration
	// Signals delivers metadata_changed; nil never evicts early (the entries still validate
	// themselves).
	Signals Signals
	Metrics prometheus.Registerer
	// >>> WP2.9 exposure
	// FillRefused counts an upstream contact the endpoint's cache-fill policy
	// prevented (trove_endpoint_cache_fill_refused_total, R-EP-019): the remote
	// engine owns the family and counts the contacts it refuses itself; the virtual
	// engine counts the remote members it does not ask for an artifact. Nil counts
	// nothing.
	FillRefused func(endpoint string)
	// <<< WP2.9 exposure
	// Now overrides the clock, for tests.
	Now func() time.Time
	Log *slog.Logger
}

// Resolver is the virtual engine of one replica. It holds no request state beyond the merged
// cache (R-ARCH-032).
type Resolver struct {
	o      Options
	merged *cache.Cache[mergedKey, *mergedEntry]
	m      *metrics
	cancel func()
}

// New creates the engine and registers the merged-metadata cache.
func New(o Options) (*Resolver, error) {
	switch {
	case o.Router == nil:
		return nil, errors.New("virtual: no router")
	case o.Decider == nil:
		return nil, errors.New("virtual: no decision point")
	case o.Locals == nil:
		return nil, errors.New("virtual: no reader of local members")
	}
	if o.SafetyTTL <= 0 {
		o.SafetyTTL = DefaultSafetyTTL
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	m, err := newMetrics(o.Metrics)
	if err != nil {
		return nil, err
	}
	r := &Resolver{o: o, m: m}
	if o.Caches != nil && o.CacheBytes > 0 {
		r.merged = registerMerged(o.Caches, o)
	}
	return r, nil
}

// Start subscribes to metadata_changed. The engine is correct without it.
func (r *Resolver) Start(ctx context.Context) error {
	if r.o.Signals == nil || r.merged == nil {
		return nil
	}
	cancel, err := r.o.Signals.Subscribe(ctx, cluster.AreaMetadataChanged, r.onMetadataChanged)
	if err != nil {
		return err
	}
	r.cancel = cancel
	return nil
}

// Stop ends the subscription.
func (r *Resolver) Stop() {
	if r.cancel != nil {
		r.cancel()
		r.cancel = nil
	}
}

// coordinate is the coordinate of the request as the decision point asks it of a member.
func coordinate(v repository.Repository, d Descriptor) authz.Coordinate {
	return authz.Coordinate{
		Repository: v.Key, Class: v.Class, Format: v.Format, Kind: d.Kind,
		Path: d.Path, Package: d.Package, Version: d.Version, Addressed: v.Key,
	}
}

// Plan is R-REPO-019 steps 1 to 5 for one caller: the route, which depends on the stored
// configuration only, then the per-member read and populate check (R-AUTHZ-009 rules 2 and 3).
// It does not repeat the gate of step 1 on the virtual repository itself (rule 1) nor the rule for
// anonymous callers (rule 6): the coarse check of the request decided both before the format ran.
func (r *Resolver) Plan(ctx context.Context, c Caller, v repository.Repository, d Descriptor) (*Plan, error) {
	return r.plan(ctx, c, v, d, false)
}

// plan builds the plan; with all it decides every member of the repository, for the explanation.
func (r *Resolver) plan(ctx context.Context, c Caller, v repository.Repository, d Descriptor, all bool) (*Plan, error) {
	if v.Class != repository.Virtual {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "Resolution was asked for a repository that is not virtual.")
	}
	route, err := r.o.Router.Route(ctx, v, repository.RouteRequest{Name: d.Name(), Path: d.Path})
	if err != nil {
		return nil, err
	}
	p := &Plan{Virtual: v, Descriptor: d, Route: route, Admitted: route.Admitted && !v.Disabled, usable: map[string]usable{}}
	candidates := route.Members
	if all {
		candidates = v.Members()
	} else if !p.Admitted {
		return p, nil
	}
	coord := coordinate(v, d)
	for _, m := range candidates {
		read, fill := r.o.Decider.MemberUsableOn(ctx, c.Index, c.Subject, m.Key, coord)
		p.usable[m.Key] = usable{read: read, fill: fill}
		if !read {
			p.Unreadable++
		}
	}
	if !p.Admitted {
		return p, nil
	}
	for _, m := range route.Members {
		u := p.usable[m.Key]
		if !u.read {
			continue
		}
		p.Members = append(p.Members, Member{Repo: m, ID: repoID(m), Position: len(p.Members), MayFill: u.fill && m.Class == repository.Remote})
	}
	p.Fingerprint = planFingerprint(c.Subject.Endpoint, p.Members)
	return p, nil
}

// planFingerprint is the SHA-256 over the endpoint and the ordered (member id, class, mayFill) of
// the members that took part (D-7, Q-REPO-110). The populate right is in it because it decides
// which versions of a remote member a merge may list (R-AUTHZ-008), the endpoint because it can
// switch that right off for every caller (R-EP-019).
func planFingerprint(endpoint string, members []Member) [32]byte {
	h := sha256.New()
	writeInt(h, int64(len(endpoint)))
	h.Write([]byte(endpoint))
	for _, m := range members {
		writeInt(h, m.ID)
		h.Write([]byte{classByte(m.Repo.Class), boolByte(m.MayFill)})
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// writeInt feeds a number to a hash as text with a terminator, so that two sequences of numbers
// never hash alike.
func writeInt(h hash.Hash, n int64) {
	var b [24]byte
	h.Write(append(strconv.AppendInt(b[:0], n, 10), '|'))
}

func classByte(c repository.Class) byte {
	switch c {
	case repository.Local:
		return 'l'
	case repository.Remote:
		return 'r'
	}
	return 'v'
}

func boolByte(v bool) byte {
	if v {
		return 1
	}
	return 0
}

// DeployTarget is the repository a write to the virtual repository goes to (R-REPO-021,
// R-AUTHZ-009 rule 5): 405 no_deploy_target when there is none, and 404 when the target is
// disabled or being deleted, like any request to it.
func (r *Resolver) DeployTarget(v repository.Repository) (repository.Repository, error) {
	t, ok := v.DeployTarget()
	if !ok {
		return repository.Repository{}, errs.E(errs.MethodNotAllowed, errs.CodeNoDeployTarget,
			"The virtual repository has no default deployment target, so it takes no deploys.", errs.WithAllow("GET", "HEAD"))
	}
	if t.Disabled || t.Lifecycle == repository.Deleting {
		return repository.Repository{}, errs.E(errs.NotFound, errs.CodeRepositoryDisabled, "The deployment target of the virtual repository is not available.")
	}
	return t, nil
}

// notFound is the 404 of a resolution that found nothing. It is the same error for a package that
// does not exist and for one that only exists in members the caller cannot read (R-AUTHZ-009 rule 8).
func notFound() error { return authz.NotFound() }
