package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/guided-traffic/trove/internal/app"
	"github.com/guided-traffic/trove/internal/config"
)

// serveCmd runs one replica of a role (R-ARCH-001) until SIGTERM or SIGINT, then
// shuts down in the order of R-ARCH-024. A second signal ends the process at once.
func serveCmd(args []string, s streams) error {
	c := newFlags("serve", s)
	c.fs.String("role", "", "web, worker or all; overrides process.role")
	flags, names, err := c.parse(args, map[string]string{"role": "process.role", "log-level": "log.level"})
	if err != nil {
		return err
	}
	loaded, err := c.loadConfig(s, config.CommandServe, flags, names)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	go func() {
		<-ctx.Done()
		stop() // restore default handling: a second signal terminates the process
	}()
	out := s.stdout
	if out == nil {
		out = os.Stdout
	}
	return app.RunWith(ctx, app.Options{Loaded: loaded, Environ: s.environ, LogWriter: out})
}
