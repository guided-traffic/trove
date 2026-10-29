package app

import (
	"context"
	"strings"
	"time"
)

// warnEvery is how often the standing configuration warnings repeat (R-OPS-005).
const warnEvery = time.Hour

// standingWarnings logs the warnings about a configuration that is allowed but
// unsafe outside development: at start and then every hour, so a log search a
// week later still finds them.
func (a *App) standingWarnings() {
	if a.sub.ClientListener && a.cfg.Server.BaseURL == "" {
		a.log.Warn("server.base_url is not set: generated URLs are derived from the Host header of each request and a client can steer them; set it for any installation that is not local development (R-OPS-005)")
	}
	if a.cfg.System.BootstrapAnonymousRead {
		a.log.Warn("system.bootstrap_anonymous_read is true: anyone who reaches the client listener can read the system information and the replica list; for local development only",
			"base_url_https", strings.HasPrefix(strings.ToLower(a.cfg.Server.BaseURL), "https://"))
	}
}

// warnLoop repeats standingWarnings every interval until ctx ends. The first
// round runs before it is started.
func (a *App) warnLoop(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.standingWarnings()
		}
	}
}

func (a *App) warnEvery() time.Duration {
	if a.warnInterval > 0 {
		return a.warnInterval
	}
	return warnEvery
}
