package app

import (
	"context"
	"fmt"

	debdata "github.com/guided-traffic/trove/internal/formatdata/deb"
	"github.com/guided-traffic/trove/internal/formathost"
	"github.com/guided-traffic/trove/internal/formats"
	debformat "github.com/guided-traffic/trove/internal/formats/deb"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/mirror/discover"
)

// debBackground gives the jobs of the Debian remote class the upstream of a repository for one run (Host.BackgroundUpstream,
// Q-REPO-272). It asks the mirror host's builder when a run needs it: the host exists in the roles that run jobs and is built on its
// first use (wire_mirror_host.go).
type debBackground struct{ a *App }

var _ debdata.Background = debBackground{}

// Upstream implements [debdata.Background].
func (b debBackground) Upstream(ctx context.Context, repoID int64, jobID string) (discover.Upstream, error) {
	area := part[mirrorEngineArea](b.a.parts)
	if area.b == nil {
		return nil, fmt.Errorf("formats: this process has no remote engine for the background fetches of the Debian remote class")
	}
	mh, err := area.b.get()
	if err != nil {
		return nil, err
	}
	return mh.Background(formathost.WithJobID(ctx, jobID), repoID)
}

// startDebRemote builds the runtime of the jobs of the Debian remote class on a replica that claims jobs (R-DEB-021, R-DEB-023):
// the parse of an index, the re-verification and housekeeping of a remote, and the remote part of the periodic task.
func (a *App) startDebRemote(ports map[formats.ID]any) error {
	st, ok := ports[debformat.ID].(*debdata.Store)
	if !ok {
		return fmt.Errorf("formats: the Debian data port is not the PostgreSQL store")
	}
	a.debRemote.set(&debdata.RemoteJobs{
		Store: st, Background: debBackground{a}, ByHashKeep: a.cfg.Deb.ByHashKeep, Log: a.log.With("component", "deb-remote"),
		Items: func() debdata.Remover {
			if s, ok := a.itemStore.get(); ok {
				return s
			}
			return nil
		},
	})
	return nil
}

// debRemoteHandlers are the handlers of the jobs the remote class runs; they find the runtime when a job runs.
func (a *App) debRemoteHandlers() debdata.RemoteHandlers {
	return debdata.RemoteHandlers{
		Parse: a.debRemote.handler("Debian remote parse", func(ctx context.Context, j *debdata.RemoteJobs, run *jobs.Run) error {
			return j.RunParse(ctx, run)
		}),
		Index: a.debRemote.handler("Debian remote housekeeping", func(ctx context.Context, j *debdata.RemoteJobs, run *jobs.Run) error {
			return j.RunIndex(ctx, run)
		}),
	}
}

// registerDebRemoteRefresh adds the remote part to the periodic task deb-index-refresh. A replica without the runtime (it claims no
// job) does nothing; the task runs where jobs run.
func (a *App) registerDebRemoteRefresh() {
	debdata.RegisterRefreshHook(func(ctx context.Context, run *jobs.Run) error {
		j, ok := a.debRemote.get()
		if !ok {
			return nil
		}
		return j.Refresh(ctx, run)
	})
}
