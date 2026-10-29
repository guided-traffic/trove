package cluster

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/valkey-io/valkey-go"
)

// The methods below are the commands trove needs and no others (R-ARCH-031's
// list). Every call carries valkey.command_timeout, passes the circuit breaker,
// and returns [ErrUnavailable] without network I/O while Valkey cannot be used.
// There is no way to write a key without an expiry: a [Key] carries its own.

// exec runs fn against the current client under the breaker and the command
// timeout.
func (v *Valkey) exec(ctx context.Context, command string, fn func(ctx context.Context, c valkey.Client) error) error {
	c := v.current()
	if c == nil || v.isUnsupported() {
		return ErrUnavailable
	}
	if !v.br.allow() {
		return ErrUnavailable
	}
	callCtx, cancel := context.WithTimeout(ctx, v.cfg.CommandTimeout)
	defer cancel()
	start := time.Now()
	err := fn(callCtx, c)
	v.m.duration.WithLabelValues(command).Observe(time.Since(start).Seconds())

	kind := classify(err)
	if kind != kindNone && kind != kindCanceled {
		v.m.errors.WithLabelValues(string(kind)).Inc()
	}
	v.br.done(breakerOutcome(kind))
	if kind == kindNone {
		v.clearProbeFailure()
	}
	return err
}

func (v *Valkey) clearProbeFailure() {
	v.mu.RLock()
	failed := v.probeFailed != ""
	v.mu.RUnlock()
	if !failed {
		return
	}
	v.mu.Lock()
	v.probeFailed = ""
	v.mu.Unlock()
	v.reevaluate()
}

func checkSize(values ...string) error {
	for _, s := range values {
		if len(s) > MaxValueSize {
			return fmt.Errorf("%w (%d bytes)", ErrValueTooLarge, len(s))
		}
	}
	return nil
}

func validKey(k Key) error {
	if k.name == "" || k.ttl <= 0 {
		return errors.New("cluster: invalid key: form keys with Valkey.Key")
	}
	return nil
}

// Ping checks that the primary answers.
func (v *Valkey) Ping(ctx context.Context) error {
	return v.exec(ctx, "ping", func(ctx context.Context, c valkey.Client) error {
		return c.Do(ctx, c.B().Ping().Build()).Error()
	})
}

// Get returns the value of k; found is false when the key does not exist.
func (v *Valkey) Get(ctx context.Context, k Key) (value string, found bool, err error) {
	if err := validKey(k); err != nil {
		return "", false, err
	}
	err = v.exec(ctx, "get", func(ctx context.Context, c valkey.Client) error {
		s, err := c.Do(ctx, c.B().Get().Key(k.name).Build()).ToString()
		if valkey.IsValkeyNil(err) {
			return nil
		}
		if err != nil {
			return err
		}
		value, found = s, true
		return nil
	})
	return value, found, err
}

// Set writes value under k with k's expiry.
func (v *Valkey) Set(ctx context.Context, k Key, value string) error {
	if err := validKey(k); err != nil {
		return err
	}
	if err := checkSize(value); err != nil {
		return err
	}
	return v.exec(ctx, "set", func(ctx context.Context, c valkey.Client) error {
		return c.Do(ctx, c.B().Set().Key(k.name).Value(value).Px(k.ttl).Build()).Error()
	})
}

// SetNX writes value under k with k's expiry only if the key does not exist; ok
// reports whether it wrote.
func (v *Valkey) SetNX(ctx context.Context, k Key, value string) (ok bool, err error) {
	if err := validKey(k); err != nil {
		return false, err
	}
	if err := checkSize(value); err != nil {
		return false, err
	}
	err = v.exec(ctx, "set", func(ctx context.Context, c valkey.Client) error {
		err := c.Do(ctx, c.B().Set().Key(k.name).Value(value).Nx().Px(k.ttl).Build()).Error()
		if valkey.IsValkeyNil(err) {
			return nil
		}
		ok = err == nil
		return err
	})
	return ok, err
}

// SetIfEq replaces the value of k with value and renews k's expiry, only if the
// current value is old; ok reports whether it replaced. It is the extension of
// a lock held with value old.
func (v *Valkey) SetIfEq(ctx context.Context, k Key, old, value string) (ok bool, err error) {
	if err := validKey(k); err != nil {
		return false, err
	}
	if err := checkSize(old, value); err != nil {
		return false, err
	}
	err = v.exec(ctx, "set", func(ctx context.Context, c valkey.Client) error {
		err := c.Do(ctx, c.B().Set().Key(k.name).Value(value).Ifeq(old).Px(k.ttl).Build()).Error()
		if valkey.IsValkeyNil(err) {
			return nil
		}
		ok = err == nil
		return err
	})
	return ok, err
}

// DelIfEq deletes k only if its value is value; deleted reports whether it did.
// It is the release of a lock and the removal of a malformed value; the ACL
// user has no DEL (R-ARCH-031).
func (v *Valkey) DelIfEq(ctx context.Context, k Key, value string) (deleted bool, err error) {
	if err := validKey(k); err != nil {
		return false, err
	}
	if err := checkSize(value); err != nil {
		return false, err
	}
	err = v.exec(ctx, "delifeq", func(ctx context.Context, c valkey.Client) error {
		n, err := c.Do(ctx, c.B().Delifeq().Key(k.name).Value(value).Build()).AsInt64()
		if err != nil {
			return err
		}
		deleted = n == 1
		return nil
	})
	return deleted, err
}

// Incr increments the counter k and returns its new value. The first increment
// of a key sets k's expiry; later ones leave it, so a counter cannot be kept
// alive by traffic (R-ARCH-034). INCR and EXPIRE run in one MULTI/EXEC.
func (v *Valkey) Incr(ctx context.Context, k Key) (n int64, err error) {
	if err := validKey(k); err != nil {
		return 0, err
	}
	err = v.exec(ctx, "incr", func(ctx context.Context, c valkey.Client) error {
		res := c.DoMulti(ctx,
			c.B().Multi().Build(),
			c.B().Incr().Key(k.name).Build(),
			c.B().Expire().Key(k.name).Seconds(ceilSeconds(k.ttl)).Nx().Build(),
			c.B().Exec().Build(),
		)
		for _, r := range res[:3] {
			if err := r.Error(); err != nil {
				return err
			}
		}
		exec, err := res[3].ToArray()
		if err != nil {
			return err
		}
		if len(exec) != 2 {
			return fmt.Errorf("EXEC returned %d replies, want 2", len(exec))
		}
		n, err = exec[0].AsInt64()
		return err
	})
	return n, err
}

func ceilSeconds(d time.Duration) int64 {
	return int64((d + time.Second - 1) / time.Second)
}

// Publish sends msg on channel ch. Delivery is at most once: a subscriber that
// is not connected misses it, and correctness never depends on it (Q-ARCH-15).
func (v *Valkey) Publish(ctx context.Context, ch Channel, msg string) error {
	if ch.name == "" {
		return errors.New("cluster: invalid channel: form channels with Valkey.Channel")
	}
	if err := checkSize(msg); err != nil {
		return err
	}
	return v.exec(ctx, "publish", func(ctx context.Context, c valkey.Client) error {
		return c.Do(ctx, c.B().Publish().Channel(ch.name).Message(msg).Build()).Error()
	})
}
