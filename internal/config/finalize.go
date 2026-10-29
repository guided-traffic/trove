package config

import (
	"net/url"
	"sort"
	"strings"
	"time"
)

// Shutdown budget constants (R-ARCH-024, R-HA-011).
const (
	// DefaultShutdownTimeout applies when server.shutdown_timeout is unset and the
	// pod's grace period is unknown (outside Kubernetes).
	DefaultShutdownTimeout = 25 * time.Second
	// ShutdownMargin is what the process keeps free of the grace period for the
	// flush, the close of the pools, the ops listener and the exit.
	ShutdownMargin = 5 * time.Second
	// minShutdownTimeout is the smallest timeout worth running a drain with.
	minShutdownTimeout = time.Second
)

// Finalizer is implemented by the root: it computes values that depend on
// several sections, after every validation passed. It records where a computed
// value came from in sources.
type Finalizer interface {
	Finalize(sources map[string]Source)
}

func (l *loader) finalize() {
	if f, ok := l.root.Addr().Interface().(Finalizer); ok {
		f.Finalize(l.sources)
	}
}

// Validate checks the rules that span sections: the watched-loop cadences
// against the watchdog, the garbage collector's hand-over against the job lease
// (R-STOR-024), the shutdown budget against the pod's grace period (R-HA-011)
// and the anonymous-read guard (S10).
func (c *Config) Validate(r *Report) {
	c.validateWatchedLoops(r)
	c.validateStorageCoupling(r)
	c.validateAuthIdentity(r)
	c.ValidateAuthz(r)
	if g := c.Health.DatabaseOutageGrace; g >= c.Database.Retry.MaxElapsed {
		r.Add("health.database_outage_grace", "%s must be below database.retry.max_elapsed (%s): a replica must turn unready before its queries give up (R-OPS-001)",
			g, c.Database.Retry.MaxElapsed)
	}
	if grace := c.Runtime.TerminationGracePeriod; grace > 0 {
		fixed := c.Health.ShutdownDrainDelay + ShutdownMargin
		switch t := c.Server.ShutdownTimeout; {
		case t == 0 && grace-fixed < minShutdownTimeout:
			r.Add("runtime.termination_grace_period", "%s leaves no shutdown time after health.shutdown_drain_delay (%s) and the %s margin",
				grace, c.Health.ShutdownDrainDelay, ShutdownMargin)
		case t > 0 && fixed+t > grace:
			r.Add("server.shutdown_timeout", "%s plus health.shutdown_drain_delay (%s) plus the %s margin exceeds runtime.termination_grace_period (%s); the kubelet would kill the process mid-drain",
				t, c.Health.ShutdownDrainDelay, ShutdownMargin, grace)
		}
	}
	if c.System.BootstrapAnonymousRead {
		// Every endpoint's origin counts: the bridge is checked against the origins
		// that reach the edge, not only the default one (SA3).
		bases := map[string]string{"server.base_url": c.Server.BaseURL}
		for n, ep := range c.Server.Endpoints {
			if n != DefaultEndpoint {
				bases["server.endpoints."+n+".base_url"] = ep.BaseURL
			}
		}
		for _, key := range sortedKeys(bases) {
			if b := bases[key]; !localOrigin(b) && strings.HasPrefix(strings.ToLower(b), "https://") {
				r.Add("system.bootstrap_anonymous_read", "must not be true while %s is an https origin other than localhost: it opens system.read to everyone who reaches the edge", key)
			}
		}
	}
}

// localOrigin reports whether raw names the loopback host.
func localOrigin(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	h := strings.ToLower(u.Hostname())
	switch h {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	// RFC 6761 reserves the whole localhost. name space for loopback.
	return strings.HasSuffix(h, ".localhost")
}

// Finalize adds https:// to an object-store endpoint without a scheme (Q-STOR-15) and
// derives server.shutdown_timeout when it is unset: the pod's grace
// period minus health.shutdown_drain_delay minus the 5 s margin (R-HA-011), or
// DefaultShutdownTimeout when the grace period is unknown.
func (c *Config) Finalize(sources map[string]Source) {
	c.Storage.finalizeEndpoints(sources)
	c.Server.fillEndpointDefaults()
	if c.Server.ShutdownTimeout != 0 {
		return
	}
	if grace := c.Runtime.TerminationGracePeriod; grace > 0 {
		c.Server.ShutdownTimeout = grace - c.Health.ShutdownDrainDelay - ShutdownMargin
		sources["server.shutdown_timeout"] = Source{Kind: "derived", Detail: "runtime.termination_grace_period"}
		return
	}
	c.Server.ShutdownTimeout = DefaultShutdownTimeout
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
