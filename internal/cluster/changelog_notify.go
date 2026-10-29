package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/guided-traffic/trove/internal/meta"
)

// ErrPayloadTooLarge is returned by Notify for a payload that would not fit into
// a change message. A change that touches many objects sends one flush entry
// per cache instead (R-ARCH-018).
var ErrPayloadTooLarge = errors.New("cluster: change payload exceeds the 1 KiB limit; send a coarse flush entry instead")

// maxPayload leaves room for the envelope {"a":…,"e":…,"v":…,"p":…}.
const maxPayload = MaxValueSize - 96

// The two statements of R-ARCH-018 step 1. They run on the writer's
// transaction, last, so the area's row is held for the commit only.
const (
	sqlBumpEpoch    = `UPDATE cluster_epoch SET epoch = epoch + 1, changed_at = now() WHERE area = $1 RETURNING epoch`
	sqlInsertChange = `INSERT INTO cluster_change (area, epoch, payload) VALUES ($1, $2, $3::text::jsonb)`
)

type note struct {
	area    string
	version int
	payload json.RawMessage
	epoch   int64
}

// pendingNotes tracks the notifications of open transactions. meta.Tx has no
// per-transaction slot, so the notifications of one transaction are found by its
// identity. The entry is removed by the transaction's After hook; the entry of a
// rolled-back transaction (whose hooks never run) is swept after a minute.
type pendingNotes struct {
	mu sync.Mutex
	by map[meta.Tx]*txNotes
}

type txNotes struct {
	notes   []note
	created time.Time
}

func (p *pendingNotes) init() { p.by = map[meta.Tx]*txNotes{} }

// add records a note and reports whether it is the first of its transaction.
func (p *pendingNotes) add(tx meta.Tx, n note) (first bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	if len(p.by) > 64 {
		for k, v := range p.by {
			if now.Sub(v.created) > time.Minute {
				delete(p.by, k)
			}
		}
	}
	t := p.by[tx]
	if t == nil {
		t = &txNotes{created: now}
		p.by[tx] = t
		first = true
	}
	t.notes = append(t.notes, n)
	return first
}

func (p *pendingNotes) take(tx meta.Tx) []note {
	p.mu.Lock()
	defer p.mu.Unlock()
	t := p.by[tx]
	delete(p.by, tx)
	if t == nil {
		return nil
	}
	return t.notes
}

func (p *pendingNotes) peek(tx meta.Tx) []note {
	p.mu.Lock()
	defer p.mu.Unlock()
	if t := p.by[tx]; t != nil {
		return append([]note(nil), t.notes...)
	}
	return nil
}

func (p *pendingNotes) setEpochs(tx meta.Tx, ns []note) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if t := p.by[tx]; t != nil {
		t.notes = ns
	}
}

// Notify attaches a change of a change area to the writing transaction
// (R-ARCH-018 step 1). Just before COMMIT, whatever point of the transaction
// called Notify, the area's epoch is raised and the change row inserted; a
// transaction that notifies several areas updates their epoch rows in area-name
// order, so two such transactions cannot deadlock. After COMMIT the writer
// applies its own invalidation at once and publishes the change on Valkey. A
// rollback leaves neither the row nor the epoch, and nothing is published.
//
// version is the payload version (at most the area's PayloadVersion), payload
// any JSON-encodable value under 1 KiB. A payload never carries a secret or a
// value from which a live credential can be derived.
func (c *Changes) Notify(tx meta.Tx, area string, version int, payload any) error {
	st := c.areas[area]
	if st == nil {
		return fmt.Errorf("cluster: %q is not a change area this replica follows", area)
	}
	if version < 1 || version > st.meta.PayloadVersion {
		return fmt.Errorf("cluster: payload version %d of area %q is outside 1..%d", version, area, st.meta.PayloadVersion)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("cluster: payload of area %q: %w", area, err)
	}
	if len(raw) > maxPayload {
		return ErrPayloadTooLarge
	}
	if c.pend.add(tx, note{area: area, version: version, payload: raw}) {
		tx.Before(func(ctx context.Context, tx meta.Tx) error { return c.writeNotes(ctx, tx) })
		tx.After(func() { c.afterCommit(tx) })
	}
	return nil
}

// writeNotes is the Before hook: the two statements per note, areas in name
// order.
func (c *Changes) writeNotes(ctx context.Context, tx meta.Tx) error {
	ns := c.pend.peek(tx)
	sort.SliceStable(ns, func(i, j int) bool { return ns[i].area < ns[j].area })
	for i := range ns {
		var epoch int64
		if err := tx.QueryRow(ctx, sqlBumpEpoch, ns[i].area).Scan(&epoch); err != nil {
			return fmt.Errorf("cluster: raising the epoch of area %q (is its cluster_epoch row seeded by a migration?): %w", ns[i].area, err)
		}
		body, err := json.Marshal(changeRow{V: ns[i].version, P: ns[i].payload})
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, sqlInsertChange, ns[i].area, epoch, string(body)); err != nil {
			return fmt.Errorf("cluster: writing the change of area %q: %w", ns[i].area, err)
		}
		ns[i].epoch = epoch
	}
	c.pend.setEpochs(tx, ns)
	return nil
}

// afterCommit applies the writer's own invalidation and queues the Valkey
// messages. It must not block: it runs on the request that committed.
func (c *Changes) afterCommit(tx meta.Tx) {
	for _, n := range c.pend.take(tx) {
		c.applyChange(n.area, n.version, n.payload)
		c.m.applied.WithLabelValues(n.area, sourceLocal).Inc()
		msg, err := json.Marshal(wireChange{A: n.area, E: n.epoch, V: n.version, P: n.payload})
		if err != nil {
			continue
		}
		select {
		case c.pubq <- pubItem{area: n.area, msg: string(msg)}:
		default:
			c.m.dropped.WithLabelValues(n.area).Inc()
		}
	}
}

// publishLoop sends the queued change messages. Delivery is at most once and
// never a correctness condition: while Valkey is unavailable the message is
// dropped and counted, and the poll applies the change.
func (c *Changes) publishLoop(ctx context.Context) {
	defer c.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case it := <-c.pubq:
			c.publish(ctx, it)
		}
	}
}

type pubItem struct{ area, msg string }

func (c *Changes) publish(ctx context.Context, it pubItem) {
	area, msg := it.area, it.msg
	bus := c.o.Bus
	if bus == nil || !bus.Available() {
		c.m.dropped.WithLabelValues(area).Inc()
		if bus != nil {
			bus.CountDegraded(MechanismChangeMessage)
		}
		return
	}
	ch, err := bus.Channel(invChannels)
	if err != nil {
		c.m.dropped.WithLabelValues(area).Inc()
		return
	}
	pctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := bus.Publish(pctx, ch, msg); err != nil {
		c.m.dropped.WithLabelValues(area).Inc()
		bus.CountDegraded(MechanismChangeMessage)
		return
	}
	c.m.published.WithLabelValues(area).Inc()
}
