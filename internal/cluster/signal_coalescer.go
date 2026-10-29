package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/meta"
)

// SignalHandler receives the payloads of one delivery of a signal area. coarse
// is true when the publisher had more payloads than fit into one message (or
// none to name); the consumer then treats the signal as "something happened".
type SignalHandler func(payloads []string, coarse bool)

// maxSignalPayloads and the 1 KiB cap bound a batch (R-ARCH-018).
const signalBodyLimit = MaxValueSize - 64

// Signals is the post-commit signal path of R-ARCH-018 for the signal areas.
// A rollback never reaches it, because publication is an After hook of the
// writing transaction. The coalescer sends at most one message per area per
// cluster.signal_coalesce_window and batches the payloads; the first signal of
// an idle area leaves at once, so the wake-up costs no window.
//
// Delivery is at most once, and local subscribers of the same replica get the
// batch directly. Every consumer must stay correct without a signal.
type Signals struct {
	bus    Bus
	window time.Duration
	log    *slog.Logger
	m      *signalMetrics

	mu     sync.Mutex
	closed bool
	areas  map[string]*signalArea
	wg     sync.WaitGroup
}

type signalArea struct {
	name     string
	pending  []string
	coarse   bool
	last     time.Time
	timer    *time.Timer
	handlers map[uint64]SignalHandler
	next     uint64
	cancel   func() // the bus subscription
}

type signalMetrics struct {
	published *prometheus.CounterVec
	dropped   *prometheus.CounterVec
}

// SignalsOptions configure [NewSignals].
type SignalsOptions struct {
	Bus     Bus
	Config  config.Cluster
	Log     *slog.Logger
	Metrics prometheus.Registerer
}

// NewSignals creates the signal path.
func NewSignals(o SignalsOptions) (*Signals, error) {
	m := &signalMetrics{
		published: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "trove_cluster_signals_published_total", Help: "Signal messages published to Valkey by area."}, []string{"area"}),
		dropped:   prometheus.NewCounterVec(prometheus.CounterOpts{Name: "trove_cluster_signals_dropped_total", Help: "Signals not published because Valkey was unavailable, by area."}, []string{"area"}),
	}
	if err := register(o.Metrics, m.published, m.dropped); err != nil {
		return nil, err
	}
	log := o.Log
	if log == nil {
		log = slog.Default()
	}
	return &Signals{bus: o.Bus, window: o.Config.SignalCoalesceWindow, log: log, m: m, areas: map[string]*signalArea{}}, nil
}

func (s *Signals) area(name string) *signalArea {
	a := s.areas[name]
	if a == nil {
		a = &signalArea{name: name, handlers: map[uint64]SignalHandler{}}
		s.areas[name] = a
	}
	return a
}

func signalAreaOf(name string) error {
	a, ok := LookupArea(name)
	switch {
	case !ok:
		return errors.New("cluster: unknown signal area " + name)
	case a.Class != SignalArea:
		return errors.New("cluster: " + name + " is a change area, not a signal area")
	}
	return nil
}

// Subscribe registers h for the area and returns the function that removes it.
// The first subscriber of an area subscribes the replica's Pub/Sub connection
// to the area's channel; that fails only for a missing keyspace ID.
func (s *Signals) Subscribe(ctx context.Context, area string, h SignalHandler) (cancel func(), err error) {
	if err := signalAreaOf(area); err != nil {
		return nil, err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, errors.New("cluster: signals are closed")
	}
	a := s.area(area)
	first := len(a.handlers) == 0 && a.cancel == nil
	id := a.next
	a.next++
	a.handlers[id] = h
	s.mu.Unlock()

	if first && s.bus != nil {
		ch, err := s.bus.Channel(sigChannels, area)
		if err == nil {
			var c func()
			c, err = s.bus.Subscribe(ctx, ch, func(m Message) { s.receive(area, m.Payload) })
			if err == nil {
				s.mu.Lock()
				a.cancel = c
				s.mu.Unlock()
			}
		}
		if err != nil {
			s.mu.Lock()
			delete(a.handlers, id)
			s.mu.Unlock()
			return nil, err
		}
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			delete(a.handlers, id)
			var c func()
			if len(a.handlers) == 0 {
				c, a.cancel = a.cancel, nil
			}
			s.mu.Unlock()
			if c != nil {
				c()
			}
		})
	}, nil
}

// Publish announces a signal on the area after the writing transaction
// committed. It never blocks and never fails: with Valkey unavailable the
// signal is dropped and counted, which the consumers' own polling covers.
func (s *Signals) Publish(area, payload string) {
	if signalAreaOf(area) != nil {
		s.log.Error("signal on an unregistered or non-signal area", "area", area)
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	a := s.area(area)
	if len(payload) > 0 {
		a.add(payload)
	} else {
		a.coarse = true
	}
	local := s.localHandlersLocked(a)
	now := time.Now()
	var send bool
	switch {
	case a.timer != nil:
		// a trailing send is already scheduled; this payload rides along
	case now.Sub(a.last) >= s.window:
		send = true
	default:
		a.timer = time.AfterFunc(s.window-now.Sub(a.last), func() { s.flush(a) })
	}
	s.mu.Unlock()

	for _, h := range local {
		if payload == "" {
			h(nil, true)
		} else {
			h([]string{payload}, false)
		}
	}
	if send {
		s.flush(a)
	}
}

func (a *signalArea) add(p string) {
	for _, q := range a.pending {
		if q == p {
			return
		}
	}
	a.pending = append(a.pending, p)
}

func (s *Signals) localHandlersLocked(a *signalArea) []SignalHandler {
	out := make([]SignalHandler, 0, len(a.handlers))
	for _, h := range a.handlers {
		out = append(out, h)
	}
	return out
}

// flush sends the pending batch of an area.
func (s *Signals) flush(a *signalArea) {
	s.mu.Lock()
	if a.timer != nil {
		a.timer.Stop()
		a.timer = nil
	}
	payloads, coarse := a.pending, a.coarse
	a.pending, a.coarse = nil, false
	a.last = time.Now()
	closed := s.closed
	s.mu.Unlock()
	if closed || (len(payloads) == 0 && !coarse) {
		return
	}
	if s.bus == nil || !s.bus.Available() {
		s.m.dropped.WithLabelValues(a.name).Inc()
		if s.bus != nil {
			s.bus.CountDegraded(MechanismSignal)
		}
		return
	}
	body, err := encodeSignal(payloads, coarse)
	if err != nil {
		s.log.Warn("signal not published", "area", a.name, "error", err)
		return
	}
	ch, err := s.bus.Channel(sigChannels, a.name)
	if err != nil {
		s.log.Warn("signal not published", "area", a.name, "error", err)
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := s.bus.Publish(ctx, ch, body); err != nil {
			s.m.dropped.WithLabelValues(a.name).Inc()
			s.bus.CountDegraded(MechanismSignal)
			return
		}
		s.m.published.WithLabelValues(a.name).Inc()
	}()
}

type signalMessage struct {
	P []string `json:"p,omitempty"`
	C bool     `json:"c,omitempty"`
}

// encodeSignal renders a batch. A batch that does not fit into one message
// becomes a coarse one: the consumer polls (or wakes) without a payload.
func encodeSignal(payloads []string, coarse bool) (string, error) {
	b, err := json.Marshal(signalMessage{P: payloads, C: coarse})
	if err != nil {
		return "", err
	}
	if len(b) > signalBodyLimit {
		b, err = json.Marshal(signalMessage{C: true})
	}
	return string(b), err
}

// receive handles a message from the bus. Messages published by this replica
// arrive here too; the handlers are idempotent wake-ups.
func (s *Signals) receive(area, body string) {
	var m signalMessage
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		return // a forged or foreign message can only be ignored
	}
	s.mu.Lock()
	a := s.areas[area]
	if a == nil {
		s.mu.Unlock()
		return
	}
	hs := s.localHandlersLocked(a)
	s.mu.Unlock()
	for _, h := range hs {
		h(m.P, m.C)
	}
}

// Notify attaches a signal to the transaction: it is published once the
// transaction has committed and dropped on a rollback.
func (s *Signals) Notify(tx meta.Tx, area, payload string) {
	tx.After(func() { s.Publish(area, payload) })
}

// Close stops the coalescer. Pending signals are sent first (R-ARCH-024
// step 6: pending signals are published), then subscriptions end.
func (s *Signals) Close() {
	s.mu.Lock()
	var pending []*signalArea
	for _, a := range s.areas {
		if len(a.pending) > 0 || a.coarse {
			pending = append(pending, a)
		}
	}
	s.mu.Unlock()
	for _, a := range pending {
		s.flush(a)
	}
	s.wg.Wait()
	s.mu.Lock()
	s.closed = true
	var cancels []func()
	for _, a := range s.areas {
		if a.timer != nil {
			a.timer.Stop()
		}
		if a.cancel != nil {
			cancels = append(cancels, a.cancel)
			a.cancel = nil
		}
	}
	s.mu.Unlock()
	for _, c := range cancels {
		c()
	}
}
