package cluster

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// MaxFlightLockTTL is the longest expiry a fetch lock may have (the maximum of
// its key family). cluster.flight_lock_ttl is the actual expiry.
const MaxFlightLockTTL = 10 * time.Minute

const (
	defaultFlightLockTTL = 30 * time.Second
	defaultFlightPoll    = time.Second
	// maxTraceparent bounds the trace context an outcome message may carry;
	// the whole message stays far below the 1 KiB of R-ARCH-030.
	maxTraceparent = 256
)

// FlightKey names one flight: the fetch of one cache entry (R-REPO-026 step
// 1). It is the lowercase hex SHA-256 of the repository id in decimal digits,
// a zero byte, the canonical path, a zero byte and the variant, so the key
// leaks no name and two flights never share a lock (Q-ARCH-7). The zero value
// names nothing and is refused by every call.
type FlightKey struct{ hex string }

// NewFlightKey derives the key of the flight for (repoID, path, variant).
func NewFlightKey(repoID int64, path, variant string) FlightKey {
	h := sha256.New()
	h.Write([]byte(strconv.FormatInt(repoID, 10)))
	h.Write([]byte{0})
	h.Write([]byte(path))
	h.Write([]byte{0})
	h.Write([]byte(variant))
	return FlightKey{hex: hex.EncodeToString(h.Sum(nil))}
}

// String returns the 64 hex digits that appear in the lock key, the channel
// and the negative entry.
func (f FlightKey) String() string { return f.hex }

func (f FlightKey) valid() bool { return len(f.hex) == 2*sha256.Size }

var errInvalidFlightKey = errors.New("cluster: invalid flight key: form it with NewFlightKey")

// Outcome is what a flight ended with (Q-ARCH-195). A waiter treats every
// value alike: it repeats the proxy's checks and may acquire the flight.
type Outcome string

const (
	// OutcomeStored: an entry, or a negative entry, exists now.
	OutcomeStored Outcome = "stored"
	// OutcomeFailed: the holder gave up; nothing was stored.
	OutcomeFailed Outcome = "failed"
	// OutcomeGone is returned by Wait only: the lock key is absent and no
	// outcome was heard, because the holder died and the key expired, or a
	// failover or a flush took the key, or the message was missed. A waiter
	// that first looks at the key between the release's DELIFEQ and its PUBLISH
	// gets it too; the release order (R-ARCH-033) is what lets a woken waiter
	// acquire the flight, so the window is not closed.
	OutcomeGone Outcome = "gone"
)

// FlightLock is the cross-replica lock that collapses concurrent fetches of
// one cache entry (R-ARCH-033, INV-11). It is an optimization and never a
// correctness condition: blob commits are idempotent and the entry upsert
// refuses a different digest, so a lost lock costs one duplicate fetch.
//
// Every method returns an error that wraps [ErrUnavailable] when Valkey cannot
// be used or a call to it fails, at once while the breaker is open. The caller
// then fetches under its in-process single-flight only. A context that ends
// returns the context's error instead.
type FlightLock interface {
	// Acquire tries once. ok is false while another holder has the flight.
	Acquire(ctx context.Context, f FlightKey) (h *FlightHold, ok bool, err error)
	// Wait returns when the holder publishes an outcome, when the lock is
	// gone, or when ctx ends. The caller then repeats the proxy's checks and
	// may call Acquire again.
	Wait(ctx context.Context, f FlightKey) (Outcome, error)
}

// FlightLockOptions configures [NewFlightLock].
type FlightLockOptions struct {
	// TTL is the expiry of the lock key, cluster.flight_lock_ttl; the hold
	// extends it every third of TTL. Zero means 30 s, and TTL is capped at
	// [MaxFlightLockTTL].
	TTL time.Duration
	// PollInterval is how often a waiter checks the key although no message
	// came, proxy.lock_poll_interval_ms. Zero means 1 s.
	PollInterval time.Duration
	// InstanceUID prefixes the token of every hold: "<instance uid>.<16 random
	// bytes, base64url>". Empty means the instance name of the Valkey client.
	InstanceUID string
	// Registerer receives trove_flight_lock_lost_total; nil registers nothing.
	Registerer prometheus.Registerer
}

type flightLock struct {
	v    *Valkey
	ttl  time.Duration
	poll time.Duration
	uid  string
	lost prometheus.Counter

	mu   sync.Mutex
	subs map[string]*flightSub
}

// NewFlightLock creates the lock. With v == nil every call returns
// [ErrUnavailable], which is the single-replica and the test configuration.
func NewFlightLock(v *Valkey, o FlightLockOptions) FlightLock {
	l := &flightLock{v: v, ttl: o.TTL, poll: o.PollInterval, uid: o.InstanceUID, subs: map[string]*flightSub{}}
	if l.ttl <= 0 {
		l.ttl = defaultFlightLockTTL
	}
	l.ttl = min(l.ttl, MaxFlightLockTTL)
	if l.poll <= 0 {
		l.poll = defaultFlightPoll
	}
	if l.uid == "" && v != nil {
		l.uid = v.instance
	}
	l.lost = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "trove_flight_lock_lost_total",
		Help: "Holds whose lock key was gone or taken when they extended it.",
	})
	if o.Registerer != nil {
		if err := o.Registerer.Register(l.lost); err != nil {
			var are prometheus.AlreadyRegisteredError
			if errors.As(err, &are) {
				if c, ok := are.ExistingCollector.(prometheus.Counter); ok {
					l.lost = c
				}
			}
		}
	}
	if v != nil {
		// A subscription made again on a new primary has missed whatever was
		// published meanwhile: every waiter checks its key (R-ARCH-031).
		v.OnSubscribed(func(reconnect bool) {
			if reconnect {
				l.wakeAll()
			}
		})
	}
	return l
}

func (l *flightLock) keys(f FlightKey) (Key, Channel, error) {
	if !f.valid() {
		return Key{}, Channel{}, errInvalidFlightKey
	}
	k, err := l.v.formKeyTTL(flightLockFamily, l.ttl, f.hex)
	if err != nil {
		return Key{}, Channel{}, err
	}
	ch, err := l.v.channel(flightFillChannel, f.hex)
	return k, ch, err
}

// fail turns the error of a Valkey call into the error of the interface and
// counts the fallback the caller is about to run.
func (l *flightLock) fail(ctx context.Context, err error) error {
	if cerr := ctx.Err(); cerr != nil {
		return cerr
	}
	if !errors.Is(err, ErrUnavailable) {
		err = fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	l.v.CountDegraded(MechanismFlightLock)
	return err
}

func (l *flightLock) Acquire(ctx context.Context, f FlightKey) (*FlightHold, bool, error) {
	if l.v == nil {
		return nil, false, ErrUnavailable
	}
	if !f.valid() {
		return nil, false, errInvalidFlightKey
	}
	k, ch, err := l.keys(f)
	if err != nil {
		return nil, false, l.fail(ctx, err)
	}
	token := l.uid + "." + randomToken()
	ok, err := l.v.setNX(ctx, k, token)
	if err != nil {
		return nil, false, l.fail(ctx, err)
	}
	if !ok {
		return nil, false, nil
	}
	// The extension outlives ctx: the fetch is detached from its client
	// (R-REPO-026). Closing the Valkey client ends it.
	extCtx, stop := context.WithCancel(l.v.ctx)
	h := &FlightHold{l: l, key: k, ch: ch, token: token, stop: stop, done: make(chan struct{})}
	go h.extend(extCtx)
	return h, true, nil
}

func randomToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// FlightHold is the lock of one flight, held until [FlightHold.Release].
type FlightHold struct {
	l     *flightLock
	key   Key
	ch    Channel
	token string
	stop  context.CancelFunc
	done  chan struct{}

	lost atomic.Bool

	once       sync.Once
	releaseErr error
}

// extend renews the lock every third of its expiry while the fetch runs
// (R-ARCH-033). A failed call is tried again at the next tick: the lock may
// expire meanwhile, and the next call that reaches Valkey then finds it gone.
// A call that finds the key gone or owned by another token ends the loop; the
// fetch carries on, and the hold never deletes the other holder's key.
func (h *FlightHold) extend(ctx context.Context) {
	defer close(h.done)
	t := time.NewTicker(h.l.ttl / 3)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		ok, err := h.l.v.setIfEq(ctx, h.key, h.token, h.token)
		if err != nil || ctx.Err() != nil {
			continue
		}
		if !ok {
			h.lost.Store(true)
			h.l.lost.Inc()
			return
		}
	}
}

// Lost reports whether an extension found the lock gone or owned by another
// token: another replica may fetch the same entry now. The fetch stays
// correct (INV-11).
func (h *FlightHold) Lost() bool { return h.lost.Load() }

// Release stops the extension, deletes the lock if this hold still owns it
// (DELIFEQ) and publishes the outcome on the flight's channel, in that order,
// so a woken waiter finds the lock gone. traceparent, when it is a short
// printable token, travels with the outcome for a span link. Both commands run
// even when ctx has been cancelled (the fetch is detached from its client).
// Release may be called again and does nothing the second time.
func (h *FlightHold) Release(ctx context.Context, o Outcome, traceparent string) error {
	h.once.Do(func() {
		h.stop()
		<-h.done
		ctx := context.WithoutCancel(ctx)
		_, derr := h.l.v.delIfEq(ctx, h.key, h.token)
		perr := h.l.v.publish(ctx, h.ch, outcomeMessage(o, traceparent))
		h.releaseErr = errors.Join(derr, perr)
	})
	return h.releaseErr
}

// outcomeMessage is "<outcome>" or "<outcome> <traceparent>" (Q-ARCH-195).
func outcomeMessage(o Outcome, traceparent string) string {
	if traceparent == "" || len(traceparent) > maxTraceparent {
		return string(o)
	}
	for _, r := range traceparent {
		if r <= ' ' || r > '~' {
			return string(o)
		}
	}
	return string(o) + " " + traceparent
}

func parseOutcome(msg string) (Outcome, bool) {
	name, _, _ := strings.Cut(msg, " ")
	switch o := Outcome(name); o {
	case OutcomeStored, OutcomeFailed:
		return o, true
	}
	return "", false
}

// flightSub is the replica's one subscription to a flight's channel, shared by
// every waiter of the flight: 50 waiters cost one SUBSCRIBE.
type flightSub struct {
	refs    int
	cancel  func()
	waiters map[*flightWaiter]struct{}
}

type flightWaiter struct {
	msg  chan Outcome
	wake chan struct{}
}

// join registers a waiter on the flight's channel, subscribing first when it
// is the first one. leave must be called once.
func (l *flightLock) join(ch Channel) (w *flightWaiter, leave func(), err error) {
	w = &flightWaiter{msg: make(chan Outcome, 1), wake: make(chan struct{}, 1)}
	name := ch.String()
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.subs[name]
	if s == nil {
		s = &flightSub{waiters: map[*flightWaiter]struct{}{}}
		s.cancel, err = l.v.subscribe(l.v.ctx, ch, func(m Message) { l.deliver(s, m) })
		if err != nil {
			return nil, nil, err
		}
		l.subs[name] = s
	}
	s.refs++
	s.waiters[w] = struct{}{}
	return w, func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		delete(s.waiters, w)
		if s.refs--; s.refs == 0 {
			s.cancel()
			delete(l.subs, name)
		}
	}, nil
}

// deliver runs on the Pub/Sub reader goroutine: it hands the outcome to every
// waiter of the flight without blocking.
func (l *flightLock) deliver(s *flightSub, m Message) {
	o, ok := parseOutcome(m.Payload)
	if !ok {
		return // the poll covers a message nobody can read
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for w := range s.waiters {
		select {
		case w.msg <- o:
		default:
		}
	}
}

func (l *flightLock) wakeAll() {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, s := range l.subs {
		for w := range s.waiters {
			select {
			case w.wake <- struct{}{}:
			default:
			}
		}
	}
}

func (l *flightLock) Wait(ctx context.Context, f FlightKey) (Outcome, error) {
	if l.v == nil {
		return "", ErrUnavailable
	}
	if !f.valid() {
		return "", errInvalidFlightKey
	}
	k, ch, err := l.keys(f)
	if err != nil {
		return "", l.fail(ctx, err)
	}
	// Subscribe first, then look at the key: a release between the caller's
	// failed Acquire and the subscription is then seen as a missing key. The
	// subscription takes effect on the Pub/Sub connection a moment later; the
	// poll below covers that gap and every missed message (Q-ARCH-15).
	w, leave, err := l.join(ch)
	if err != nil {
		return "", l.fail(ctx, err)
	}
	defer leave()
	tick := time.NewTimer(l.poll)
	defer tick.Stop()
	for {
		_, found, err := l.v.get(ctx, k)
		if err != nil {
			return "", l.fail(ctx, err)
		}
		if !found {
			return OutcomeGone, nil
		}
		select {
		case o := <-w.msg:
			return o, nil
		case <-w.wake:
		case <-tick.C:
			tick.Reset(l.poll)
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}
