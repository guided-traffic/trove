package cluster

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/valkey-io/valkey-go"
)

// Message is a Pub/Sub message.
type Message struct {
	Channel string
	Payload string
}

// Handler receives the messages of a subscription. It runs on the Pub/Sub
// connection's reader goroutine and must return quickly; messages are
// delivered at most once and in order per connection (Q-ARCH-15).
type Handler func(Message)

// pubsub owns the replica's one Pub/Sub connection (R-ARCH-031). The
// connection is a dedicated one on the current primary, named
// trove:<instance>:pubsub. A single goroutine drives it: it subscribes every
// wanted channel, and when the connection breaks or the primary changes, it
// reconnects and subscribes them all again. Which channels are wanted is a
// local fact, so a subscription made while Valkey is down takes effect when it
// is back.
type pubsub struct {
	v *Valkey

	mu       sync.Mutex
	handlers map[string]map[uint64]Handler
	next     uint64
	wake     chan struct{}
	ready    []func(reconnect bool)
	times    int // times the connection became ready

	subscribed atomic.Bool
}

func newPubsub(v *Valkey) *pubsub {
	return &pubsub{v: v, handlers: map[string]map[uint64]Handler{}, wake: make(chan struct{}, 1)}
}

func (p *pubsub) poke() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

func (p *pubsub) isSubscribed() bool { return p.subscribed.Load() }

// Subscribe registers h for ch and returns the function that removes it. The
// registration is local and never fails because Valkey is down: the Pub/Sub
// connection subscribes when it can, and subscribes again after every primary
// change. ctx ends the subscription as well.
func (v *Valkey) Subscribe(ctx context.Context, ch Channel, h Handler) (cancel func(), err error) {
	if ch.name == "" {
		return nil, errors.New("cluster: invalid channel: form channels with Valkey.Channel")
	}
	if h == nil {
		return nil, errors.New("cluster: nil handler")
	}
	p := v.ps
	p.mu.Lock()
	if v.isClosed() {
		p.mu.Unlock()
		return nil, errors.New("cluster: valkey client is closed")
	}
	id := p.next
	p.next++
	if p.handlers[ch.name] == nil {
		p.handlers[ch.name] = map[uint64]Handler{}
	}
	p.handlers[ch.name][id] = h
	p.mu.Unlock()
	p.poke()

	var once sync.Once
	cancel = func() {
		once.Do(func() {
			p.mu.Lock()
			delete(p.handlers[ch.name], id)
			if len(p.handlers[ch.name]) == 0 {
				delete(p.handlers, ch.name)
			}
			p.mu.Unlock()
			p.poke()
		})
	}
	context.AfterFunc(ctx, cancel)
	return cancel, nil
}

// OnSubscribed registers fn to run each time the Pub/Sub connection has
// subscribed every wanted channel on a primary. reconnect is false the first
// time and true after a primary change or a broken connection. This is the
// moment for the replica's change-log poll (R-ARCH-018) and for flight-lock
// waiters to re-check their keys (R-ARCH-033). fn must not block.
func (v *Valkey) OnSubscribed(fn func(reconnect bool)) {
	v.ps.mu.Lock()
	v.ps.ready = append(v.ps.ready, fn)
	v.ps.mu.Unlock()
}

func (v *Valkey) isClosed() bool {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.closed
}

func (p *pubsub) dispatch(m valkey.PubSubMessage) {
	p.mu.Lock()
	hs := make([]Handler, 0, len(p.handlers[m.Channel]))
	for _, h := range p.handlers[m.Channel] {
		hs = append(hs, h)
	}
	p.mu.Unlock()
	for _, h := range hs {
		h(Message{Channel: m.Channel, Payload: m.Message})
	}
}

func (p *pubsub) wanted() map[string]bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[string]bool, len(p.handlers))
	for ch := range p.handlers {
		out[ch] = true
	}
	return out
}

// run is the goroutine of the Pub/Sub connection.
func (p *pubsub) run() {
	v := p.v
	defer v.wg.Done()
	backoff := v.cfg.Reconnect.InitialBackoff
	for v.ctx.Err() == nil {
		c := v.current()
		if c == nil || v.isUnsupported() {
			p.sleep(250 * time.Millisecond)
			continue
		}
		start := time.Now()
		err := p.session(c)
		p.subscribed.Store(false)
		v.m.pubsubSubscribed.Set(0)
		if v.ctx.Err() != nil {
			return
		}
		if err != nil {
			v.m.errors.WithLabelValues(string(kindLabel(classify(err)))).Inc()
			v.log.Debug("valkey pubsub connection ended", "error", err.Error())
		}
		if time.Since(start) > 10*time.Second {
			backoff = v.cfg.Reconnect.InitialBackoff
		}
		p.sleep(jitter(backoff))
		backoff = min(backoff*2, v.cfg.Reconnect.MaxBackoff)
	}
}

func (p *pubsub) sleep(d time.Duration) {
	select {
	case <-p.v.ctx.Done():
	case <-p.wake:
	case <-time.After(d):
	}
}

// session holds one Pub/Sub connection until it breaks, the primary changes or
// the client closes.
func (p *pubsub) session(c valkey.Client) error {
	v := p.v
	ded, release := c.Dedicate()
	defer release()

	onAddr := primaryAddr(c)
	disconnected := ded.SetPubSubHooks(valkey.PubSubHooks{OnMessage: p.dispatch})

	ctx, cancel := context.WithTimeout(v.ctx, v.cfg.CommandTimeout)
	err := ded.Do(ctx, ded.B().ClientSetname().ConnectionName("trove:"+v.instance+":pubsub").Build()).Error()
	cancel()
	if err != nil {
		return err
	}

	subscribed := map[string]bool{}
	reconcile := func() error {
		want := p.wanted()
		var add, drop []string
		for ch := range want {
			if !subscribed[ch] {
				add = append(add, ch)
			}
		}
		for ch := range subscribed {
			if !want[ch] {
				drop = append(drop, ch)
			}
		}
		ctx, cancel := context.WithTimeout(v.ctx, v.cfg.CommandTimeout)
		defer cancel()
		if len(add) > 0 {
			if err := ded.Do(ctx, ded.B().Subscribe().Channel(add...).Build()).Error(); err != nil {
				return err
			}
			for _, ch := range add {
				subscribed[ch] = true
			}
		}
		if len(drop) > 0 {
			if err := ded.Do(ctx, ded.B().Unsubscribe().Channel(drop...).Build()).Error(); err != nil {
				return err
			}
			for _, ch := range drop {
				delete(subscribed, ch)
			}
		}
		return nil
	}
	if err := reconcile(); err != nil {
		return err
	}
	p.subscribed.Store(true)
	v.m.pubsubSubscribed.Set(1)
	p.announceReady()

	t := time.NewTicker(monitorTick)
	defer t.Stop()
	for {
		select {
		case <-v.ctx.Done():
			return nil
		case err := <-disconnected:
			if err == nil {
				err = errors.New("pubsub connection closed")
			}
			return err
		case <-p.wake:
			if err := reconcile(); err != nil {
				return err
			}
		case <-t.C:
		}
		// A demoted primary can keep the connection open; the client's own
		// refresh has then moved on and this subscription listens on the wrong node.
		if cur := primaryAddr(c); cur != onAddr {
			return errors.New("primary changed from " + onAddr + " to " + cur)
		}
	}
}

func (p *pubsub) announceReady() {
	p.mu.Lock()
	reconnect := p.times > 0
	p.times++
	fns := append([]func(bool){}, p.ready...)
	p.mu.Unlock()
	for _, fn := range fns {
		fn(reconnect)
	}
}
