package cluster

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/valkey-io/valkey-go"
)

// The Valkey side of the rate-limit counters of R-ARCH-034 (the limiter itself
// is in limiter.go). Keys are rl:v1:<limiter>:<bucket>:<window> with the bucket
// as a hash (R-ARCH-030) and the window as the integer index of the current
// window, so a window's counter expires on its own two windows after it opened.

// maxLimiterWindow is the longest window of a limiter.
const maxLimiterWindow = 12 * time.Hour

// windowKeys forms the keys of the current and the previous window.
func (v *Valkey) windowKeys(limiter, bucket string, idx int64, ttl time.Duration) (cur, prev Key, err error) {
	cur, err = v.formKeyTTL(rlFamily, ttl, limiter, bucket, strconv.FormatInt(idx, 10))
	if err != nil {
		return Key{}, Key{}, err
	}
	prev, err = v.formKeyTTL(rlFamily, ttl, limiter, bucket, strconv.FormatInt(idx-1, 10))
	return cur, prev, err
}

// windowHit is the check of a limiter that counts requests: one round trip,
// MULTI, INCR of the current window's key, EXPIRE <key> <ttl> NX, GET of the
// previous window's key, EXEC. It returns the counter of the current window
// including this request and the counter of the previous one. No script is
// used (R-ARCH-034).
func (v *Valkey) windowHit(ctx context.Context, limiter, bucket string, idx int64, ttl time.Duration) (cur, prev int64, err error) {
	ck, pk, err := v.windowKeys(limiter, bucket, idx, ttl)
	if err != nil {
		return 0, 0, err
	}
	err = v.exec(ctx, "incr", func(ctx context.Context, c valkey.Client) error {
		res := c.DoMulti(ctx,
			c.B().Multi().Build(),
			c.B().Incr().Key(ck.name).Build(),
			c.B().Expire().Key(ck.name).Seconds(ceilSeconds(ck.ttl)).Nx().Build(),
			c.B().Get().Key(pk.name).Build(),
			c.B().Exec().Build(),
		)
		for _, r := range res[:4] {
			if err := r.Error(); err != nil {
				return err
			}
		}
		out, err := res[4].ToArray()
		if err != nil {
			return err
		}
		if len(out) != 3 {
			return fmt.Errorf("EXEC returned %d replies, want 3", len(out))
		}
		if cur, err = out[0].AsInt64(); err != nil {
			return err
		}
		prev, err = optionalCounter(out[2])
		return err
	})
	return cur, prev, err
}

// windowRead is the check of the count-only form: a GET of the current and of
// the previous window's key, without INCR.
func (v *Valkey) windowRead(ctx context.Context, limiter, bucket string, idx int64, ttl time.Duration) (cur, prev int64, err error) {
	ck, pk, err := v.windowKeys(limiter, bucket, idx, ttl)
	if err != nil {
		return 0, 0, err
	}
	err = v.exec(ctx, "get", func(ctx context.Context, c valkey.Client) error {
		res := c.DoMulti(ctx, c.B().Get().Key(ck.name).Build(), c.B().Get().Key(pk.name).Build())
		for i, dst := range []*int64{&cur, &prev} {
			m, err := res[i].ToMessage()
			if valkey.IsValkeyNil(err) {
				continue // no counter yet: zero
			}
			if err != nil {
				return err
			}
			if *dst, err = optionalCounter(m); err != nil {
				return err
			}
		}
		return nil
	})
	return cur, prev, err
}

// windowCount is Count: MULTI, INCR of the current window's key, EXPIRE NX,
// EXEC.
func (v *Valkey) windowCount(ctx context.Context, limiter, bucket string, idx int64, ttl time.Duration) error {
	ck, _, err := v.windowKeys(limiter, bucket, idx, ttl)
	if err != nil {
		return err
	}
	_, err = v.incr(ctx, ck)
	return err
}

// optionalCounter reads a GET reply that is a counter or nil.
func optionalCounter(m valkey.ValkeyMessage) (int64, error) {
	if m.IsNil() {
		return 0, nil
	}
	return m.AsInt64()
}
