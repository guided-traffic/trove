package v1

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// overviewCacheSeconds is how long a replica reuses the counts of GET
// /api/v1/system/overview (Q-OPS-175): a dashboard that refreshes every few
// seconds counts once per period and replica, not once per request.
const overviewCacheSeconds = 30

// GetSystemOverview implements GET /api/v1/system/overview (R-OPS-004,
// Q-OPS-175); settings:read is enforced before it runs. The counts come from
// PostgreSQL, the endpoints from the configuration; an unavailable database
// answers 503 like the other system reads.
func (s *Server) GetSystemOverview(w http.ResponseWriter, r *http.Request) {
	if s.o.API.Store == nil {
		httpserver.Problem(w, r, unavailable("The system overview"))
		return
	}
	out, err := s.overview.get(r.Context(), time.Now, overviewCacheSeconds*time.Second, s.countOverview)
	if err != nil {
		httpserver.Problem(w, r, errs.Wrap(err, "api.systemOverview"))
		return
	}
	writeJSON(w, r, http.StatusOK, out)
}

// overviewCache is the last overview this replica counted. One count runs at a
// time: a request that finds the cache stale while a count runs waits for that
// count instead of starting its own, and starts one itself only when that count
// failed. A failed count is never kept. The zero value is empty.
type overviewCache struct {
	mu    sync.Mutex
	value gen.SystemOverview
	until time.Time // monotonic; zero while nothing is cached
	// counting is closed when the running count ends; nil while none runs.
	counting chan struct{}
}

func (c *overviewCache) get(ctx context.Context, now func() time.Time, ttl time.Duration,
	count func(context.Context) (gen.SystemOverview, error),
) (gen.SystemOverview, error) {
	for {
		c.mu.Lock()
		if !c.until.IsZero() && now().Before(c.until) {
			v := c.value
			c.mu.Unlock()
			return v, nil
		}
		if wait := c.counting; wait != nil {
			c.mu.Unlock()
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return gen.SystemOverview{}, ctx.Err()
			}
		}
		done := make(chan struct{})
		c.counting = done
		c.mu.Unlock()
		return c.fill(ctx, now, ttl, count, done)
	}
}

// fill runs the count this request started and settles the cache however the
// count ends, a panic included, so a waiting request is never left behind.
func (c *overviewCache) fill(ctx context.Context, now func() time.Time, ttl time.Duration,
	count func(context.Context) (gen.SystemOverview, error), done chan struct{},
) (v gen.SystemOverview, err error) {
	start, ok := now(), false
	defer func() {
		c.mu.Lock()
		if ok {
			c.value, c.until = v, start.Add(ttl)
		}
		c.counting = nil
		close(done)
		c.mu.Unlock()
	}()
	v, err = count(ctx)
	ok = err == nil
	return v, err
}

// countOverview counts on one connection of the request pool. The statements
// are in queries/overview.sql; each is linear in the rows it counts, and their
// cost is in docs/developer/http-api.md.
func (s *Server) countOverview(ctx context.Context) (gen.SystemOverview, error) {
	var out gen.SystemOverview
	err := s.o.API.Store.Read(ctx, func(db q.DBTX) error {
		// Read may run this again after a failover: every attempt starts empty.
		out = gen.SystemOverview{ComputedAt: time.Now().UTC(), CacheSeconds: overviewCacheSeconds}
		return overviewCounts(ctx, q.New(db), &out)
	})
	if err != nil {
		return gen.SystemOverview{}, err
	}
	out.Counts.Endpoints = len(s.o.Endpoints.Endpoints)
	return out, nil
}

// overviewCounts fills the members of out that PostgreSQL counts.
func overviewCounts(ctx context.Context, qs *q.Queries, out *gen.SystemOverview) error {
	repos, err := qs.OverviewRepositories(ctx)
	if err != nil {
		return err
	}
	c := &out.Counts
	c.Repositories.ByClass = map[string]int{"local": 0, "remote": 0, "virtual": 0}
	for _, r := range repos {
		c.Repositories.ByClass[r.Class] += int(r.Repositories)
		c.Repositories.Total += int(r.Repositories)
	}
	principals, err := qs.OverviewPrincipals(ctx)
	if err != nil {
		return err
	}
	for _, p := range principals {
		switch p.Kind {
		case "user":
			c.Users = int(p.Principals)
		case "group":
			c.Groups = int(p.Principals)
		case "service_account":
			c.ServiceAccounts = int(p.Principals)
		case "bastion":
			c.Bastions = int(p.Principals)
		}
	}
	content, err := qs.OverviewContent(ctx)
	if err != nil {
		return err
	}
	c.Packages, c.PackageVersions = content.Packages, content.PackageVersions
	c.ActiveTokens, c.Projects = int(content.ActiveTokens), int(content.Projects)
	st, err := qs.OverviewStorage(ctx)
	if err != nil {
		return err
	}
	out.Storage.Blobs, out.Storage.Bytes, out.Storage.PendingDeletion = st.Blobs, st.Bytes, st.PendingDeletion
	jobs, err := qs.OverviewJobs(ctx)
	if err != nil {
		return err
	}
	out.Jobs.Queued, out.Jobs.Running, out.Jobs.FailedLast24h = int(jobs.Queued), int(jobs.Running), int(jobs.FailedLast24h)
	return nil
}
