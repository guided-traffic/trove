package app

import (
	"context"
	"io"

	"github.com/guided-traffic/trove/internal/bastion/server"
	"github.com/guided-traffic/trove/internal/config"
)

// BastionOptions configure a replica of the role bastion.
type BastionOptions struct {
	// Loaded is the Bastion's configuration (config.LoadBastion).
	Loaded *config.BastionLoaded
	// Environ is the process environment; nil means os.Environ.
	Environ []string
	// LogWriter receives the JSON log; nil means os.Stdout.
	LogWriter io.Writer
}

// RunBastion runs one replica of the role bastion until ctx ends (R-BAS-001). The role has a composition root
// of its own (internal/bastion/server): it builds none of the areas of the other roles, so it does not go
// through New, SubsystemsFor or the wiring steps, and `trove serve --role bastion` is the only way in. The
// archtest rule of R-BAS-001 keeps its packages from importing the subsystems that those areas wire.
func RunBastion(ctx context.Context, o BastionOptions) error {
	return server.Run(ctx, server.Options{Loaded: o.Loaded, Environ: o.Environ, LogWriter: o.LogWriter})
}
