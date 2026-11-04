package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/guided-traffic/trove/internal/app"
	"github.com/guided-traffic/trove/internal/config"
)

// serveBastion runs one replica of the role bastion (R-BAS-001). The role has a configuration tree of its own,
// so it is chosen here, before the main tree is loaded, by --role and by nothing else: process.role is not a
// key of the Bastion's tree. A key of another section stops startup with an error that names the key and the
// role.
func serveBastion(c *commonFlags, s streams, flags, names map[string]string) error {
	delete(flags, "process.role")
	delete(names, "process.role")
	loaded, err := config.LoadBastion(config.Options{
		File: c.file, Environ: s.environ, Flags: flags, FlagNames: names, Command: config.CommandServe,
	})
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
	return app.RunBastion(ctx, app.BastionOptions{Loaded: loaded, Environ: s.environ, LogWriter: out})
}

// serveCmd runs one replica of a role (R-ARCH-001) until SIGTERM or SIGINT, then
// shuts down in the order of R-ARCH-024. A second signal ends the process at once.
func serveCmd(args []string, s streams) error {
	c := newFlags("serve", s)
	c.fs.String("role", "", "api, packages, worker, web, all or bastion; overrides process.role")
	flags, names, err := c.parse(args, map[string]string{"role": "process.role", "log-level": "log.level"})
	if err != nil {
		return err
	}
	if flags["process.role"] == config.BastionRole {
		return serveBastion(c, s, flags, names)
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
