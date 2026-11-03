package mainclient

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/guided-traffic/trove/internal/bastion/metrics"
)

// DefaultWatchEvery is how often the token file's identity is looked at between two full reads: a rotation of
// the mounted Secret (the kubelet swaps a symbolic link) is noticed within this time, not after the reload
// interval (R-BAS-006, Q-BAS-77).
const DefaultWatchEvery = time.Second

// logQuiet is how long the same read error is logged once.
const logQuiet = 10 * time.Minute

// ReloaderOptions configure a [Reloader].
type ReloaderOptions struct {
	// Path is bastion.main.token_file.
	Path string
	// Interval is bastion.main.token_reload_interval: the file is read this often whether it looks changed or
	// not.
	Interval time.Duration
	// WatchEvery is the cadence of the file-identity check; zero means [DefaultWatchEvery].
	WatchEvery time.Duration
	// Holder is the token in use. Its current value is the baseline of the first read.
	Holder *Holder
	// Metrics counts trove_bastion_token_reloads_total.
	Metrics *metrics.Metrics
	Log     *slog.Logger
	// OnChange is called after a different valid token replaced the one in use, with the new token. It must
	// not block.
	OnChange func(Token)
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// Reloader re-reads the token file every interval and when its identity changes, and puts a different valid
// token into the holder; the next request uses it, requests in flight finish with the old one (R-BAS-006). An
// unreadable or malformed file keeps the token in use, is logged as an error and counted as refused.
type Reloader struct {
	o ReloaderOptions

	mu      sync.Mutex
	seen    os.FileInfo
	lastErr string
	errAt   time.Time
}

// NewReloader returns a reloader; Run drives it.
func NewReloader(o ReloaderOptions) *Reloader {
	if o.WatchEvery <= 0 {
		o.WatchEvery = DefaultWatchEvery
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	r := &Reloader{o: o}
	r.seen, _ = os.Stat(o.Path)
	return r
}

// Run blocks until ctx ends.
func (r *Reloader) Run(ctx context.Context) {
	watch := time.NewTicker(r.o.WatchEvery)
	defer watch.Stop()
	full := time.NewTicker(r.o.Interval)
	defer full.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-full.C:
			_, _ = r.Reload()
		case <-watch.C:
			if r.identityChanged() {
				_, _ = r.Reload()
			}
		}
	}
}

// identityChanged reports whether the file at the path is another file or has another size or time than at the
// last read. A stat error counts as changed (once) so that the read reports it.
func (r *Reloader) identityChanged() bool {
	fi, err := os.Stat(r.o.Path)
	r.mu.Lock()
	defer r.mu.Unlock()
	if err != nil {
		return r.seen != nil
	}
	return r.seen == nil || !os.SameFile(r.seen, fi) || !r.seen.ModTime().Equal(fi.ModTime()) || r.seen.Size() != fi.Size()
}

// Reload reads the file once. It reports whether a different token was put into use.
func (r *Reloader) Reload() (changed bool, err error) {
	fi, _ := os.Stat(r.o.Path)
	tok, err := ReadTokenFile(r.o.Path)
	r.mu.Lock()
	r.seen = fi
	r.mu.Unlock()
	if err != nil {
		r.o.Metrics.TokenReloads.WithLabelValues(metrics.ReloadRefused).Inc()
		r.logRefused(err)
		return false, err
	}
	r.mu.Lock()
	r.lastErr = ""
	r.mu.Unlock()
	if tok.Value == r.o.Holder.Token().Value {
		return false, nil
	}
	r.o.Holder.Set(tok)
	r.o.Metrics.TokenReloads.WithLabelValues(metrics.ReloadLoaded).Inc()
	r.o.Log.Info("bastion token reloaded", "hint", tok.Hint)
	if r.o.OnChange != nil {
		r.o.OnChange(tok)
	}
	return true, nil
}

// logRefused logs the error of a read: every new error, and the same error again after logQuiet. The error
// never contains the content of the file.
func (r *Reloader) logRefused(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.o.Now()
	if msg := err.Error(); msg == r.lastErr && now.Sub(r.errAt) < logQuiet {
		return
	}
	r.lastErr, r.errAt = err.Error(), now
	r.o.Log.Error("bastion token file refused, the token in use stays", "error", err, "path", r.o.Path,
		"hint", r.o.Holder.Token().Hint, "malformed", errors.Is(err, ErrTokenMalformed))
}
