package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/guided-traffic/trove/internal/cache"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/observability"
	"github.com/guided-traffic/trove/internal/outbound"
	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/internal/secrets"
)

// CacheClients is the name of the registered cache of the built clients
// (R-ARCH-018, R-REPO-049).
const CacheClients = "proxy_upstream_clients"

// failureMemory is how long a client whose secrets could not be resolved is not
// built again: a flood of requests for a repository with a missing file costs one
// resolution per interval, not one per request.
const failureMemory = 5 * time.Second

// maxClients bounds the cache: one client per remote repository in its current
// configuration version, a few more while versions change.
const maxClients = 2048

// SecretSource reads the stored secret fields of a repository.
// *repository.SecretStore implements it.
type SecretSource interface {
	Secrets(ctx context.Context, repoID int64) ([]repository.StoredSecret, error)
}

// Options configure [New].
type Options struct {
	// Factory builds the outbound clients; the egress rules and the trust store
	// are its.
	Factory *outbound.Factory
	// Store reads the sealed values and references of saved repositories.
	Store SecretSource
	// Ring opens sealed values.
	Ring *secrets.KeyRing
	// Caches registers the client cache.
	Caches *cache.Registry
	// Catalog is the format catalog (the probe resource); nil is catalog.Default.
	Catalog *catalog.Registry
	// Proxy supplies proxy.max_redirects and proxy.global_offline.
	Proxy config.Proxy
	// SecretsDir is config_as_code.secrets_dir.
	SecretsDir string
	// SafetyTTL bounds the age of a cached client (cache.snapshot_safety_ttl).
	SafetyTTL time.Duration
	Log       *slog.Logger

	// The seams of the tests: the clock, the environment, the file reader, the
	// interface lister of the source network.
	Now        func() time.Time
	LookupEnv  func(string) (string, bool)
	ReadFile   func(string) ([]byte, error)
	Interfaces func() ([]netip.Addr, error)
}

// Clients builds and keeps the clients of the remote repositories of one replica.
type Clients struct {
	o     Options
	cat   *catalog.Registry
	res   resolver
	cache *cache.Cache[clientKey, *Client]

	mu     sync.Mutex
	calls  map[clientKey]*call
	failed map[clientKey]failure
	latest map[int64]*Client // the newest client of each repository, to retire the older one
}

// clientKey names a client: the repository and the version of its configuration.
// A changed configuration or secret moves config_version, so it is a new key.
type clientKey struct {
	repo    int64
	version int64
}

type failure struct {
	err   error
	until time.Time
}

type call struct {
	done chan struct{}
	cl   *Client
	err  error
}

// New returns the clients of a replica and registers their cache.
func New(o Options) (*Clients, error) {
	switch {
	case o.Factory == nil:
		return nil, errors.New("upstream: no outbound factory")
	case o.Caches == nil:
		return nil, errors.New("upstream: no cache registry")
	case o.SafetyTTL <= 0:
		return nil, errors.New("upstream: no safety TTL for the client cache")
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.LookupEnv == nil {
		o.LookupEnv = os.LookupEnv
	}
	if o.ReadFile == nil {
		o.ReadFile = readLimited
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	c := &Clients{
		o: o, cat: catalog.Or(o.Catalog),
		res:   resolver{dir: o.SecretsDir, lookup: o.LookupEnv, readFile: o.ReadFile},
		calls: map[clientKey]*call{}, failed: map[clientKey]failure{}, latest: map[int64]*Client{},
	}
	c.cache = cache.Register(o.Caches, CacheClients, cache.Options[clientKey, *Client]{
		MaxEntries: maxClients, SafetyTTL: o.SafetyTTL, Areas: []string{cluster.AreaRepoConfig},
		OnChange: func(cc *cache.Cache[clientKey, *Client], _ string, _ int, payload json.RawMessage) {
			// The key carries config_version, so a stale client is unreachable once the
			// snapshot has the new version; the eviction keeps the cache small and the
			// pool of a repository that was deleted from living on.
			var p struct {
				ID string `json:"id"`
			}
			uerr := json.Unmarshal(payload, &p)
			id, err := strconv.ParseInt(p.ID, 10, 64)
			if uerr != nil || err != nil {
				cc.Flush(cache.ReasonChange)
				c.retireAll()
				return
			}
			cc.InvalidateFunc(func(k clientKey) bool { return k.repo == id })
			c.retireRepo(id)
		},
	})
	return c, nil
}

// For returns the client of a saved remote repository in its current
// configuration version, building it when no replica-local copy exists. A
// credential that cannot be unsealed or resolved is 502
// upstream_secret_unavailable, and other repositories are unaffected.
func (c *Clients) For(ctx context.Context, r repository.Repository, s repository.RemoteSettings) (*Client, error) {
	id, err := strconv.ParseInt(r.ID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("upstream: repository id %q: %w", r.ID, err)
	}
	k := clientKey{repo: id, version: r.Version}
	if cl, ok := c.cache.Get(k); ok {
		return cl, nil
	}
	if err := c.recentFailure(k); err != nil {
		return nil, err
	}
	cl, err := c.once(ctx, k, func(ctx context.Context) (*Client, error) {
		return c.cache.Load(ctx, k, func(ctx context.Context) (*Client, error) { return c.buildSaved(ctx, id, r.Key, s) })
	})
	if err != nil {
		c.remember(k, err)
		return nil, err
	}
	c.retire(id, cl)
	return cl, nil
}

// once shares one build among the concurrent callers of a key. The build belongs
// to every caller, so it does not end with the first one's request.
func (c *Clients) once(ctx context.Context, k clientKey, fn func(context.Context) (*Client, error)) (*Client, error) {
	c.mu.Lock()
	if cl, ok := c.calls[k]; ok {
		c.mu.Unlock()
		select {
		case <-cl.done:
			return cl.cl, cl.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	cl := &call{done: make(chan struct{})}
	c.calls[k] = cl
	c.mu.Unlock()

	bctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	cl.cl, cl.err = fn(bctx)
	cancel()

	c.mu.Lock()
	delete(c.calls, k)
	c.mu.Unlock()
	close(cl.done)
	return cl.cl, cl.err
}

func (c *Clients) recentFailure(k clientKey) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	f, ok := c.failed[k]
	if !ok {
		return nil
	}
	if c.o.Now().After(f.until) {
		delete(c.failed, k)
		return nil
	}
	return f.err
}

// remember keeps the refusal of a secret for failureMemory. Only that failure is
// kept: a database error or a cancelled request says nothing about the secret.
func (c *Clients) remember(k clientKey, err error) {
	if e, ok := errs.As(err); !ok || e.Code != errs.CodeUpstreamSecretUnavailable {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.failed) >= maxClients {
		clear(c.failed)
	}
	c.failed[k] = failure{err: err, until: c.o.Now().Add(failureMemory)}
}

// retire closes the idle connections of the client a newer version replaced. The
// requests that still run on it finish; its pool is not reused afterwards.
func (c *Clients) retire(repo int64, cl *Client) {
	c.mu.Lock()
	old := c.latest[repo]
	c.latest[repo] = cl
	c.mu.Unlock()
	if old != nil && old != cl {
		old.Close()
	}
}

// retireRepo closes the idle connections of the client a repository change
// replaces, and forgets it: a deleted repository leaves nothing behind.
func (c *Clients) retireRepo(repo int64) {
	c.mu.Lock()
	old := c.latest[repo]
	delete(c.latest, repo)
	c.mu.Unlock()
	if old != nil {
		old.Close()
	}
}

// retireAll does that for every repository, after a flush.
func (c *Clients) retireAll() {
	c.mu.Lock()
	all := c.latest
	c.latest = map[int64]*Client{}
	c.mu.Unlock()
	for _, cl := range all {
		cl.Close()
	}
}

// Ephemeral builds a client that is not cached and not tied to a repository, for
// the upstream test of a configuration that is not saved (R-REPO-024). password
// is the inline secret of the request; a reference in the settings is resolved
// like a saved one. The caller closes it.
func (c *Clients) Ephemeral(key string, s repository.RemoteSettings, password *observability.Secret) (*Client, error) {
	var sec *secretValue
	switch {
	case password != nil:
		sec = newFixedSecret(*password)
	case s.Password != nil && s.Password.Ref != nil:
		sec = newRefSecret(*s.Password.Ref, c.res, c.o.Now)
	}
	return c.build(key, s, sec)
}

// buildSaved resolves the secrets of the stored rows and builds the client.
func (c *Clients) buildSaved(ctx context.Context, id int64, key string, s repository.RemoteSettings) (*Client, error) {
	var sec *secretValue
	if s.AuthMode != repository.AuthNone && s.Password != nil {
		rows, err := c.o.Store.Secrets(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("upstream: read the secrets of repository %q: %w", key, err)
		}
		sec, err = c.secretOf(id, key, rows)
		if err != nil {
			c.o.Log.Warn("upstream: a secret of the repository cannot be resolved; its fetches fail until it can",
				"repo", key, "field", "/remote/password", "error", err)
			return nil, secretUnavailable(key, err)
		}
	}
	return c.build(key, s, sec)
}

// secretOf picks the password row and unseals or resolves it.
func (c *Clients) secretOf(id int64, key string, rows []repository.StoredSecret) (*secretValue, error) {
	const field = "/remote/password"
	for _, r := range rows {
		if r.Field != field {
			continue
		}
		if r.Ref != nil {
			if why := r.Ref.Valid(); why != "" {
				return nil, errors.New("the stored reference is invalid: " + why)
			}
			sv := newRefSecret(*r.Ref, c.res, c.o.Now)
			if _, err := sv.get(); err != nil {
				return nil, err
			}
			return sv, nil
		}
		v, err := repository.OpenSecret(c.o.Ring, id, field, r.Sealed)
		if err != nil {
			return nil, fmt.Errorf("unseal the value of repository %q: %w", key, err)
		}
		return newFixedSecret(v), nil
	}
	return nil, errors.New("the repository has no stored value for its password")
}

// build applies the settings to an outbound client.
func (c *Clients) build(key string, s repository.RemoteSettings, sec *secretValue) (*Client, error) {
	origin, err := url.Parse(s.URL)
	if err != nil {
		return nil, fmt.Errorf("upstream: the url of repository %q: %w", key, err)
	}
	if origin.Host == "" {
		return nil, fmt.Errorf("upstream: the url of repository %q has no host", key)
	}
	cred := &credential{mode: s.AuthMode, username: s.Username, secret: sec}
	policy := &outbound.CredentialPolicy{Origin: origin, ForwardOnRedirect: s.ForwardAuthOnRedirect, Headers: cred.headers}
	for _, h := range s.ExtraHosts {
		if h.SendCredentials {
			policy.SendCredentials = append(policy.SendCredentials, h.Pattern)
		}
	}
	opts := []outbound.ClientOption{
		outbound.WithConnectTimeout(time.Duration(s.ConnectTimeoutMs) * time.Millisecond),
		outbound.WithResponseHeaderTimeout(time.Duration(s.ReadTimeoutMs) * time.Millisecond),
		outbound.WithMaxRedirects(c.o.Proxy.MaxRedirects),
		outbound.WithLogAttrs(slog.String("repo", key)),
		outbound.WithHopPolicy(policy),
	}
	if s.SourceNetwork != nil {
		opts = append(opts, outbound.WithLocalPrefix(*s.SourceNetwork, c.o.Interfaces))
	}
	if s.OutboundProxy.Mode == repository.ProxyDirect {
		opts = append(opts, outbound.WithDirect())
	}
	hc := c.o.Factory.Client(outbound.PurposeRemote, opts...)
	hc.Transport = &guard{next: hc.Transport, repo: key, cred: cred}
	return &Client{key: key, settings: s, origin: origin, http: hc, cred: cred}, nil
}
