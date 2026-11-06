package shared

import (
	"context"
	"io/fs"
	"log/slog"
	"sync"
	"time"

	"github.com/guided-traffic/trove/internal/webui"
)

const (
	// PublishWait bounds the publish of the start-up (Q-HA-123): a replica waits for it while it is not ready yet, and
	// becomes ready without it when the store cannot take the build in that time; the Keeper then tries again every
	// retryEvery. The replica's own files never depend on the store, only the other release's pages do.
	PublishWait = time.Minute
	retryEvery  = time.Minute
)

// Keeper publishes a replica's build at start-up and keeps it in the store while the replica runs: every RenewEvery it
// renews the build's rows, publishes what is missing (after a restore, or a row another replica pruned), and prunes the
// rows no replica renewed within the retention (Q-HA-122). It is a component of the replica (internal/app).
type Keeper struct {
	store     *Store
	tree      fs.FS
	files     []webui.File
	log       *slog.Logger
	every     time.Duration
	retention time.Duration

	mu        sync.Mutex
	published bool
	cancel    context.CancelFunc
	done      chan struct{}
}

// NewKeeper returns the keeper of a build: tree and its hashed files (webui.HashedFiles).
func NewKeeper(store *Store, tree fs.FS, files []webui.File, log *slog.Logger) *Keeper {
	if log == nil {
		log = slog.Default()
	}
	return &Keeper{store: store, tree: tree, files: files, log: log, every: RenewEvery, retention: Retention}
}

// Name implements the component interface of internal/app.
func (k *Keeper) Name() string { return "ui-assets" }

// PublishAtStart publishes the build before the replica turns ready, retrying a failure until wait has passed. It
// returns the last error when the build is not in the store by then; the replica starts anyway and the loop of Start
// tries again.
func (k *Keeper) PublishAtStart(ctx context.Context, wait time.Duration) error {
	deadline := time.Now().Add(wait)
	delay := time.Second
	for {
		err := k.cycle(ctx)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil || time.Now().Add(delay).After(deadline) {
			return err
		}
		k.log.Warn("the UI's hashed files are not in the shared asset store yet; retrying", "error", err, "retry_in", delay.String())
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		delay = min(delay*2, 15*time.Second)
	}
}

// cycle is one publish and prune.
func (k *Keeper) cycle(ctx context.Context) error {
	start := time.Now()
	res, err := k.store.Publish(ctx, k.tree, k.files)
	if err != nil {
		return err
	}
	k.mu.Lock()
	first := !k.published
	k.published = true
	k.mu.Unlock()
	level := slog.LevelDebug
	if first || res.Inserted > 0 {
		level = slog.LevelInfo
	}
	k.log.Log(ctx, level, "the UI's hashed files are in the shared asset store",
		"files", res.Files, "inserted", res.Inserted, "renewed", res.Renewed, "skipped", res.Skipped,
		"took", time.Since(start).Round(time.Millisecond).String())
	pruned, err := k.store.Prune(ctx, k.retention)
	if err != nil {
		// the build is in the store; an old row stays one cycle longer
		k.log.Warn("the shared asset store was not pruned", "error", err)
		return nil
	}
	if pruned > 0 {
		k.log.Info("removed the UI files that no replica renewed within the retention from the shared asset store",
			"rows", pruned, "retention", k.retention.String())
	}
	return nil
}

// Start runs the loop: every RenewEvery once the build is published, every minute until it is.
func (k *Keeper) Start(context.Context) error {
	ctx, cancel := context.WithCancel(context.Background())
	k.mu.Lock()
	k.cancel, k.done = cancel, make(chan struct{})
	k.mu.Unlock()
	go func() {
		defer close(k.done)
		for {
			k.mu.Lock()
			wait := k.every
			if !k.published {
				wait = retryEvery
			}
			k.mu.Unlock()
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			if err := k.cycle(ctx); err != nil && ctx.Err() == nil {
				k.log.Warn("the UI's hashed files could not be renewed in the shared asset store", "error", err)
			}
		}
	}()
	return nil
}

// Stop ends the loop and waits for a cycle that runs.
func (k *Keeper) Stop(ctx context.Context) error {
	k.mu.Lock()
	cancel, done := k.cancel, k.done
	k.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
