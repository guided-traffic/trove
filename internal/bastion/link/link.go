// Package link is a replica's standing connection to the main installation (R-BAS-010, R-BAS-026 to R-BAS-028,
// Q-BAS-33, Q-BAS-44): the handshake at start, at an interval, after a failure and after a settings entry; the
// poll of the revocation feed that keeps answers/FeedLog current; the state reachable or unreachable that the
// grace period hangs on; and the mismatch and token-lifetime signals. The request flow reads the result through
// [Link.State] and [Link.AnswerState] and never talks to the main installation about any of it.
//
// The state machine, in the words of Q-BAS-44:
//
//   - A replica starts unreachable (since its start). It ends that state when a handshake and a feed poll have
//     both succeeded after the state began.
//   - It becomes unreachable when a handshake fails (a connect or TLS error, a timeout, a 5xx, 401
//     bastion_token_invalid, 400 bastion_interface_mismatch, a base URL the main installation does not honour)
//     or when no feed poll succeeded for unreachable_after. A failing forward alone does not.
//   - Unreachable, the link sends nothing but a handshake and a feed probe every poll interval.
//
// Every request of the link goes through the client without a slot of the budget (R-BAS-025).
package link

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/guided-traffic/trove/internal/bastion/answers"
	"github.com/guided-traffic/trove/internal/bastion/iface"
	"github.com/guided-traffic/trove/internal/bastion/mainclient"
	"github.com/guided-traffic/trove/internal/bastion/metrics"
	"github.com/guided-traffic/trove/internal/config"
)

const (
	// DefaultFeedLimit is the page size asked of the feed; the main installation caps it at
	// bastions.feed.max_page.
	DefaultFeedLimit = 1000
	// maxPages bounds the pages one tick reads while the answer says more.
	maxPages = 100
	// warnEvery is how often the token warning repeats (R-BAS-006).
	warnEvery = 24 * time.Hour
	// failureLogEvery is how often the same kind of failure is logged at warning level.
	failureLogEvery = time.Minute
)

// Config is what the link reads of the configuration.
type Config struct {
	// BaseURL is server.base_url: the handshake's responseBaseUrl must equal it (R-BAS-010).
	BaseURL string
	// HandshakeInterval is bastion.main.handshake_interval, PollInterval bastion.feed.poll_interval,
	// UnreachableAfter bastion.main.unreachable_after.
	HandshakeInterval time.Duration
	PollInterval      time.Duration
	UnreachableAfter  time.Duration
	// AnswerPeriod and GracePeriod are bastion.answers.period and grace_period: they set the start of the
	// feed history and how long entries are kept.
	AnswerPeriod time.Duration
	GracePeriod  time.Duration
	// WarnBefore is bastion.token.warn_before.
	WarnBefore time.Duration
	// FeedLimit is the page size; zero means [DefaultFeedLimit].
	FeedLimit int
}

// ConfigFrom reads the link's settings from the role's configuration.
func ConfigFrom(c *config.BastionConfig) Config {
	b := c.Bastion
	return Config{
		BaseURL: c.Server.BaseURL, HandshakeInterval: b.Main.HandshakeInterval, PollInterval: b.Feed.PollInterval,
		UnreachableAfter: b.Main.UnreachableAfter, AnswerPeriod: b.Answers.Period, GracePeriod: b.Answers.GracePeriod,
		WarnBefore: b.Token.WarnBefore,
	}
}

// AnswerSink is what the request flow offers so that a feed entry deletes the answers it matches: from the
// replica's memory and, best effort, from the shared answer store (R-BAS-026). The validity rules of
// answers.State hold whether or not the sink deletes anything; deleting only frees memory and takes the
// answer out of Valkey for the other replicas.
type AnswerSink interface {
	// Drop deletes the answers that the entry matches.
	Drop(e iface.FeedEntry)
	// DropAll deletes every answer, on a reset.
	DropAll()
}

// Options are the dependencies of a link.
type Options struct {
	// Client is the client of the main installation; the link calls it directly, so its own failures do not
	// pass through [Link.Guard].
	Client mainclient.Client
	Config Config
	// Feed is the log the feed entries are applied to; the request flow's validity rules read it.
	Feed    *answers.FeedLog
	Metrics *metrics.Metrics
	Log     *slog.Logger
	// Tokens is the token in use, for its hint in the log.
	Tokens mainclient.TokenSource
	// Sink deletes answers; nil deletes nothing. The request flow, which holds the answers, sets it with
	// [Link.SetSink] when it is built.
	Sink AnswerSink
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// State is a snapshot of the link, what the request flow needs of it.
type State struct {
	// Reachable is the replica's view of the main installation (R-BAS-027). UnreachableSince is the start of the
	// current unreachable state, zero while reachable.
	Reachable        bool
	UnreachableSince time.Time
	// Handshaken is true once a handshake has succeeded; the settings below are meaningless before.
	Handshaken bool
	// AnonymousAccess and RecordDownloads are the main installation's settings from the last handshake.
	AnonymousAccess bool
	RecordDownloads bool
	// HistoryStart is the start of the replica's feed history (R-BAS-026), zero until the first read succeeded.
	HistoryStart time.Time
	// Registration, TokenID and TokenExpiresAt describe the token in use as the main installation sees it.
	Registration   string
	TokenID        string
	TokenExpiresAt time.Time
	// Installation is the main installation's base URL and release.
	Installation iface.HandshakeInstallation
	// BaseURLMismatch and InterfaceMismatch hold while the condition does.
	BaseURLMismatch   bool
	InterfaceMismatch bool
}

// Link is the standing connection of one replica.
type Link struct {
	o   Options
	cfg Config
	now func() time.Time
	log *slog.Logger

	start time.Time
	wake  chan struct{}

	mu            sync.Mutex
	st            State
	cursor        iface.Cursor
	haveCursor    bool
	lastPollOK    time.Time
	needHandshake bool
	needPoll      bool
	handshakeDue  bool
	pollFailed    bool
	nextHandshake time.Time
	lastWarn      time.Time
	watchers      []func(State)
	failLog       map[string]time.Time
	mainVersion   int
}

// New returns a link in the state a replica starts in: unreachable since now, no history, no handshake.
func New(o Options) *Link {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Feed == nil {
		o.Feed = answers.NewFeedLog()
	}
	if o.Config.FeedLimit <= 0 {
		o.Config.FeedLimit = DefaultFeedLimit
	}
	now := o.Now()
	l := &Link{
		o: o, cfg: o.Config, now: o.Now, log: o.Log.With("component", "link"), start: now, wake: make(chan struct{}, 1),
		needHandshake: true, needPoll: true, handshakeDue: true, failLog: map[string]time.Time{},
	}
	l.st.UnreachableSince = now
	// The synthetic cursor of a starting replica reaches back over the lifetime of an answer (R-BAS-026,
	// Q-BAS-33). It is fixed at start, so a start during an outage still reads the whole history once the main
	// installation is back.
	l.cursor = iface.Cursor{At: now.Add(-(o.Config.AnswerPeriod + o.Config.GracePeriod + answers.SkewMargin)).UTC()}
	l.haveCursor = true
	m := o.Metrics
	m.MainReachable.Set(0)
	m.MainUnreachableSince.Set(float64(now.Unix()))
	m.BaseURLMismatch.Set(0)
	m.InterfaceMismatch.Set(0)
	m.InterfaceVersion.WithLabelValues(metrics.SideBastion).Set(iface.InterfaceVersion)
	m.FeedLag.Set(0)
	return l
}

// SetSink sets the sink that deletes answers; the engine calls it once when it is built.
func (l *Link) SetSink(s AnswerSink) {
	l.mu.Lock()
	l.o.Sink = s
	l.mu.Unlock()
}

func (l *Link) sink() AnswerSink {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.o.Sink
}

// Feed returns the feed log the link applies entries to.
func (l *Link) Feed() *answers.FeedLog { return l.o.Feed }

// State returns a snapshot.
func (l *Link) State() State {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.st
}

// Reachable reports whether the replica treats the main installation as reachable.
func (l *Link) Reachable() bool { return l.State().Reachable }

// AnswerState returns the state the validity rules of R-BAS-020 are judged against, for the given time.
func (l *Link) AnswerState(now time.Time) answers.State {
	st := l.State()
	return answers.State{
		Now: now, Period: l.cfg.AnswerPeriod, Grace: l.cfg.GracePeriod, Unreachable: !st.Reachable,
		HistoryStart: st.HistoryStart, Log: l.o.Feed,
	}
}

// Watch registers a function that is called, outside the link's lock, after every change of the state. It runs
// on the link's goroutine and must not block.
func (l *Link) Watch(f func(State)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.watchers = append(l.watchers, f)
}

// RequestHandshake makes the next tick handshake: after a reload of the token (its expiry is then another's)
// and after a settings entry.
func (l *Link) RequestHandshake() {
	l.mu.Lock()
	l.handshakeDue = true
	l.mu.Unlock()
	select {
	case l.wake <- struct{}{}:
	default:
	}
}

// TokenChanged tells the link that another token is in use: the warning clock restarts and the handshake
// brings the new token's expiry.
func (l *Link) TokenChanged() {
	l.mu.Lock()
	l.lastWarn = time.Time{}
	l.mu.Unlock()
	l.RequestHandshake()
}

// Run polls until ctx ends. The first tick is immediate.
func (l *Link) Run(ctx context.Context) {
	t := time.NewTimer(0)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-l.wake:
		}
		l.Tick(ctx)
		if !t.Stop() {
			select {
			case <-t.C:
			default:
			}
		}
		t.Reset(l.cfg.PollInterval)
	}
}

// Tick runs one round: a handshake when it is due, a feed poll, and the evaluation of the state. Tests call it.
func (l *Link) Tick(ctx context.Context) {
	if l.takeHandshakeDue() {
		l.handshake(ctx)
	}
	l.poll(ctx)
	l.evaluate()
}

func (l *Link) takeHandshakeDue() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	due := l.handshakeDue || l.needHandshake || !l.now().Before(l.nextHandshake)
	l.handshakeDue = false
	return due
}

// handshake calls the main installation and applies the answer.
func (l *Link) handshake(ctx context.Context) {
	hs, err := l.o.Client.Handshake(ctx)
	if err != nil {
		l.fail("handshake", err)
		l.mu.Lock()
		l.handshakeDue = false
		l.needHandshake = true
		l.mu.Unlock()
		return
	}
	now := l.now()
	var changed bool
	l.mu.Lock()
	prev := l.st
	l.mainVersion = hs.InterfaceVersion
	l.st.Handshaken = true
	l.st.AnonymousAccess, l.st.RecordDownloads = hs.AnonymousAccess, hs.RecordDownloads
	l.st.Registration, l.st.TokenID, l.st.TokenExpiresAt = hs.Bastion.Name, hs.Bastion.TokenID, hs.Bastion.TokenExpiresAt
	l.st.Installation = hs.Installation
	l.st.InterfaceMismatch = false
	l.nextHandshake = now.Add(l.cfg.HandshakeInterval)
	mismatch := !sameOrigin(hs.ResponseBaseURL, l.cfg.BaseURL)
	l.st.BaseURLMismatch = mismatch
	l.needHandshake = mismatch
	changed = prev.AnonymousAccess != l.st.AnonymousAccess || prev.RecordDownloads != l.st.RecordDownloads || !prev.Handshaken
	l.mu.Unlock()

	m := l.o.Metrics
	m.InterfaceMismatch.Set(0)
	m.InterfaceVersion.WithLabelValues(metrics.SideMain).Set(float64(hs.InterfaceVersion))
	if mismatch {
		m.BaseURLMismatch.Set(1)
		l.logEvery("base_url", slog.LevelError, "bastion base URL not honoured by the main installation",
			"responseBaseUrl", hs.ResponseBaseURL, "baseUrl", l.cfg.BaseURL, "release", hs.Installation.Release)
	} else {
		m.BaseURLMismatch.Set(0)
		l.clearFailure("base_url")
	}
	l.clearFailure("handshake")
	if mismatch {
		l.markUnreachable("base URL not honoured")
	}
	l.updateToken()
	if changed {
		l.notify()
	}
}

// sameOrigin compares two origins: scheme, host and port (a default port is the same as none).
func sameOrigin(a, b string) bool {
	norm := func(raw string) string {
		u, err := url.Parse(strings.TrimSpace(raw))
		if err != nil || u.Host == "" {
			return ""
		}
		port := u.Port()
		if (u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80") {
			port = ""
		}
		host := strings.ToLower(u.Hostname())
		if strings.Contains(host, ":") {
			host = "[" + host + "]"
		}
		if port != "" {
			host += ":" + port
		}
		return u.Scheme + "://" + host + strings.TrimRight(u.Path, "/")
	}
	na, nb := norm(a), norm(b)
	return na != "" && na == nb
}

// poll reads the feed from the cursor, page by page, and applies what it returns.
func (l *Link) poll(ctx context.Context) {
	for range maxPages {
		more, ok := l.pollOnce(ctx)
		if !ok || !more {
			return
		}
	}
}

func (l *Link) pollOnce(ctx context.Context) (more, ok bool) {
	l.mu.Lock()
	after := l.cursor
	l.mu.Unlock()
	sent := l.now()
	ans, err := l.o.Client.Feed(ctx, after, l.cfg.FeedLimit)
	if err != nil {
		l.fail("feed", err)
		l.mu.Lock()
		l.pollFailed = true
		l.mu.Unlock()
		return false, false
	}
	next, perr := iface.ParseCursor(ans.Cursor)
	if perr != nil {
		l.fail("feed", &mainclient.AnswerError{Status: 200, Err: perr})
		return false, false
	}
	received := l.now()
	if ans.Reset {
		l.applyReset(received, next)
	} else {
		l.applyEntries(ans.Entries, received)
		if len(ans.Entries) == 0 && next.Synthetic() {
			// The main installation read from a time, not from an entry. Move the time forward to this
			// request, so the next read is cheap: its lookback covers entries committed late (Q-BAS-76).
			next = iface.Cursor{At: sent.UTC()}
		}
	}
	l.o.Feed.Prune(received.Add(-(l.cfg.AnswerPeriod + l.cfg.GracePeriod + answers.SkewMargin)))

	l.mu.Lock()
	l.cursor = next
	l.lastPollOK = received
	l.needPoll = false
	if l.pollFailed {
		// The first poll after a failure is a reconnect: the handshake follows (R-BAS-010).
		l.pollFailed = false
		l.handshakeDue = true
	}
	if !ans.Reset && l.st.HistoryStart.IsZero() {
		// The first read of a start began at the synthetic cursor: the replica knows every entry since then.
		l.st.HistoryStart = l.startCursorLocked()
	}
	l.mu.Unlock()
	l.o.Metrics.FeedLag.Set(0)
	l.clearFailure("feed")
	return ans.More, true
}

// startCursorLocked is the time the synthetic start cursor named.
func (l *Link) startCursorLocked() time.Time {
	return l.start.Add(-(l.cfg.AnswerPeriod + l.cfg.GracePeriod + answers.SkewMargin)).UTC()
}

// applyEntries records each new entry with its receipt time and deletes the answers it matches (R-BAS-026).
func (l *Link) applyEntries(entries []iface.FeedEntry, received time.Time) {
	for _, e := range entries {
		if !e.Kind.Valid() {
			// A kind this build does not know cannot occur under equal versions (R-BAS-009); if it does,
			// the only safe reading is that every permission answer is stale.
			l.log.Error("feed entry of an unknown kind is applied as grants", "id", e.ID, "kind", string(e.Kind))
			e = iface.FeedEntry{ID: e.ID, At: e.At, Kind: iface.FeedGrants}
		}
		if !l.o.Feed.Record(e, received) {
			continue
		}
		l.o.Metrics.FeedApplied.WithLabelValues(string(e.Kind)).Inc()
		if sink := l.sink(); sink != nil {
			sink.Drop(e)
		}
		if e.Kind == iface.FeedSettings {
			l.mu.Lock()
			l.handshakeDue = true
			l.mu.Unlock()
		}
	}
}

// applyReset drops every answer and starts the history at the reset (R-BAS-026).
func (l *Link) applyReset(received time.Time, next iface.Cursor) {
	l.log.Warn("feed reset: every answer is dropped", "cursor", next.String())
	l.o.Feed.Reset()
	if sink := l.sink(); sink != nil {
		sink.DropAll()
	}
	l.o.Metrics.FeedResets.Inc()
	l.mu.Lock()
	l.st.HistoryStart = received
	l.mu.Unlock()
}

// fail handles the error of a handshake or a feed read. A failed handshake begins the unreachable state; a
// failed feed read does not by itself, only the absence of a successful one for unreachable_after does
// (evaluate). A refused token and an interface mismatch begin it whichever request met them.
func (l *Link) fail(what string, err error) {
	if errors.Is(err, context.Canceled) {
		return // the replica is stopping; that says nothing about the main installation
	}
	var mm *mainclient.MismatchError
	unreachable := what == "handshake"
	switch {
	case errors.As(err, &mm):
		l.mismatch(mm.Main)
		unreachable = true
	case errors.Is(err, mainclient.ErrTokenRefused):
		l.logEvery("token_refused", slog.LevelError, "the main installation refused the Bastion token", "request", what, "hint", l.hint())
		unreachable = true
	default:
		l.logEvery(what, slog.LevelWarn, "request to the main installation failed", "request", what, "error", err)
	}
	if unreachable {
		l.markUnreachable(what)
	}
}

// Note tells the link of an error a request of the flow got. Only a refused token and an interface mismatch
// matter: they make the replica unreachable at once (R-BAS-006, R-BAS-028); a failing forward alone does not
// (R-BAS-027).
func (l *Link) Note(err error) {
	var mm *mainclient.MismatchError
	switch {
	case errors.As(err, &mm):
		l.mismatch(mm.Main)
	case errors.Is(err, mainclient.ErrTokenRefused):
		l.logEvery("token_refused", slog.LevelError, "the main installation refused the Bastion token", "request", "forward", "hint", l.hint())
	default:
		return
	}
	l.mu.Lock()
	l.needHandshake = true
	l.mu.Unlock()
	l.markUnreachable("request")
}

// mismatch records an interface version mismatch (R-BAS-028).
func (l *Link) mismatch(main int) {
	l.mu.Lock()
	l.st.InterfaceMismatch = true
	release := l.st.Installation.Release
	l.mu.Unlock()
	m := l.o.Metrics
	m.InterfaceMismatch.Set(1)
	m.InterfaceVersion.WithLabelValues(metrics.SideBastion).Set(iface.InterfaceVersion)
	m.InterfaceVersion.WithLabelValues(metrics.SideMain).Set(float64(main))
	l.logEvery("mismatch", slog.LevelError, "bastion interface version differs from the main installation's",
		"bastion", iface.InterfaceVersion, "main", main, "mainRelease", release)
}

// markUnreachable begins the unreachable state, or notes that its conditions still hold.
func (l *Link) markUnreachable(why string) {
	now := l.now()
	l.mu.Lock()
	if !l.st.Reachable {
		l.mu.Unlock()
		return
	}
	l.st.Reachable = false
	l.st.UnreachableSince = now
	l.needHandshake, l.needPoll = true, true
	l.mu.Unlock()
	l.o.Metrics.MainReachable.Set(0)
	l.o.Metrics.MainUnreachableSince.Set(float64(now.Unix()))
	l.log.Warn("main installation unreachable", "reason", why)
	l.notify()
}

// evaluate applies the two conditions of Q-BAS-44 and refreshes the gauges that follow the clock.
func (l *Link) evaluate() {
	now := l.now()
	l.mu.Lock()
	reachable := l.st.Reachable
	stale := reachable && !l.lastPollOK.IsZero() && now.Sub(l.lastPollOK) >= l.cfg.UnreachableAfter
	if reachable && l.lastPollOK.IsZero() {
		stale = true
	}
	back := !reachable && !l.needHandshake && !l.needPoll
	lagFrom := l.lastPollOK
	if lagFrom.IsZero() {
		lagFrom = l.start
	}
	l.mu.Unlock()
	l.o.Metrics.FeedLag.Set(now.Sub(lagFrom).Seconds())
	switch {
	case stale:
		l.markUnreachable("no feed poll succeeded for " + l.cfg.UnreachableAfter.String())
	case back:
		l.markReachable(now)
	}
	l.updateToken()
}

func (l *Link) markReachable(now time.Time) {
	l.mu.Lock()
	since := l.st.UnreachableSince
	l.st.Reachable = true
	l.st.UnreachableSince = time.Time{}
	l.mu.Unlock()
	l.o.Metrics.MainReachable.Set(1)
	l.o.Metrics.MainUnreachableSince.Set(0)
	l.log.Info("main installation reachable", "unreachable_for", now.Sub(since).Round(time.Millisecond).String())
	l.notify()
}

// updateToken refreshes the remaining lifetime of the token in use and, once a day below the threshold, warns
// (R-BAS-006).
func (l *Link) updateToken() {
	now := l.now()
	l.mu.Lock()
	st := l.st
	warn := false
	if st.Handshaken && !st.TokenExpiresAt.IsZero() && st.TokenExpiresAt.Sub(now) < l.cfg.WarnBefore &&
		(l.lastWarn.IsZero() || now.Sub(l.lastWarn) >= warnEvery) {
		warn = true
		l.lastWarn = now
	}
	l.mu.Unlock()
	switch {
	case !st.Handshaken:
	case st.TokenExpiresAt.IsZero():
		l.o.Metrics.TokenRemaining.Set(math.Inf(1))
	default:
		l.o.Metrics.TokenRemaining.Set(st.TokenExpiresAt.Sub(now).Seconds())
	}
	if warn {
		l.log.Warn("bastion token expires soon", "registration", st.Registration, "token_id", st.TokenID, "hint", l.hint(),
			"expiresAt", st.TokenExpiresAt.UTC().Format(time.RFC3339), "remaining", st.TokenExpiresAt.Sub(now).Round(time.Second).String())
	}
}

func (l *Link) hint() string {
	if l.o.Tokens == nil {
		return ""
	}
	return l.o.Tokens.Token().Hint
}

// notify calls the watchers with a snapshot.
func (l *Link) notify() {
	l.mu.Lock()
	st := l.st
	ws := slices.Clone(l.watchers)
	l.mu.Unlock()
	for _, w := range ws {
		w(st)
	}
}

// logEvery logs a condition at the given level when it begins and then at most once a minute; the rest goes to
// debug, so a main installation that is down does not fill the log at the poll interval.
func (l *Link) logEvery(key string, level slog.Level, msg string, args ...any) {
	l.mu.Lock()
	now := l.now()
	last, seen := l.failLog[key]
	quiet := seen && now.Sub(last) < failureLogEvery
	if !quiet {
		l.failLog[key] = now
	}
	l.mu.Unlock()
	if quiet {
		level = slog.LevelDebug
	}
	l.log.Log(context.Background(), level, msg, args...)
}

func (l *Link) clearFailure(key string) {
	l.mu.Lock()
	delete(l.failLog, key)
	l.mu.Unlock()
}
