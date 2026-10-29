package cluster

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/valkey-io/valkey-go"

	"github.com/guided-traffic/trove/internal/outbound"
)

// sentinelProbe is the answer of one Sentinel to "where is the primary".
type sentinelProbe struct {
	Addr string
	// Primary is the "host:port" the Sentinel named; empty when it did not.
	Primary string
	// Unknown is true when the Sentinel answered, but has no primary of that name.
	Unknown bool
	// Err is the failure to reach or ask the Sentinel; nil when it answered.
	Err error
}

// probeSentinels asks every configured Sentinel concurrently, each within
// valkey.sentinel_query_timeout (R-HA-056 step 4). It opens one short
// connection per Sentinel and closes it again, so a replica keeps one
// connection to the current Sentinel and no more (R-ARCH-031).
func (v *Valkey) probeSentinels(ctx context.Context) []sentinelProbe {
	addrs := v.cfg.Sentinel.Addresses
	out := make([]sentinelProbe, len(addrs))
	var wg sync.WaitGroup
	for i, addr := range addrs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i] = v.probeSentinel(ctx, addr)
		}()
	}
	wg.Wait()
	return out
}

func (v *Valkey) probeSentinel(ctx context.Context, addr string) sentinelProbe {
	q := v.cfg.SentinelQueryTimeout
	res := sentinelProbe{Addr: addr}
	ctx, cancel := context.WithTimeout(ctx, 2*q)
	defer cancel()

	type result struct {
		reply []string
		err   error
	}
	done := make(chan result, 1)
	go func() {
		c, err := valkey.NewClient(valkey.ClientOption{
			InitAddress:       []string{addr},
			ForceSingleClient: true,
			ClientName:        "trove:" + v.instance + ":probe",
			TLSConfig:         v.sentinelTLS,
			AuthCredentialsFn: v.creds.fn,
			DialCtxFn:         dialFn(v.sentinelResolver),
			DisableCache:      true,
			DisableRetry:      true,
			PipelineMultiplex: -1,
			Dialer:            outbound.NewDialer(q, v.sentinelResolver),
			ConnWriteTimeout:  q,
		})
		if err != nil {
			done <- result{err: err}
			return
		}
		defer c.Close()
		cmd := c.B().SentinelGetMasterAddrByName().Master(v.cfg.Sentinel.PrimaryName).Build()
		reply, err := c.Do(ctx, cmd).AsStrSlice()
		done <- result{reply: reply, err: err}
	}()
	select {
	case r := <-done:
		switch {
		case valkey.IsValkeyNil(r.err):
			res.Unknown = true
		case r.err != nil:
			res.Err = r.err
		case len(r.reply) == 2:
			res.Primary = net.JoinHostPort(r.reply[0], r.reply[1])
		default:
			res.Err = fmt.Errorf("unexpected reply of %d elements", len(r.reply))
		}
	case <-ctx.Done():
		res.Err = fmt.Errorf("no answer within %s: %w", 2*q, context.DeadlineExceeded)
	}
	return res
}

// sentinelLoop reconciles the Sentinel count every refresh_interval
// (trove_valkey_sentinels_reachable). The failover itself is followed by
// valkey-go's TopologyRefreshInterval; this loop only observes.
func (v *Valkey) sentinelLoop() {
	defer v.wg.Done()
	t := time.NewTicker(v.cfg.Sentinel.RefreshInterval)
	defer t.Stop()
	for {
		ctx, cancel := context.WithTimeout(v.ctx, v.cfg.SentinelQueryTimeout*3)
		probes := v.probeSentinels(ctx)
		cancel()
		n := 0
		for _, p := range probes {
			if p.Err == nil {
				n++
			}
		}
		v.mu.Lock()
		v.sentinels = n
		v.mu.Unlock()
		v.m.sentinelsReachable.Set(float64(n))
		select {
		case <-v.ctx.Done():
			return
		case <-t.C:
		}
	}
}

// classifyStartup decides whether a failed connection attempt is a
// configuration error (Q-HA-13): a rejected credential, a failed certificate
// verification, or a primary name that no reachable Sentinel knows. Anything
// else, above all unreachability, is transient. probes is empty in standalone
// mode. The returned cause is the error to log.
func classifyStartup(primaryName string, probes []sentinelProbe, connectErr error) (config bool, cause error) {
	var reachable, unknown, withPrimary int
	var firstConfig error
	for _, p := range probes {
		switch {
		case p.Err != nil:
			if firstConfig == nil && isConfigError(classify(p.Err)) {
				firstConfig = fmt.Errorf("sentinel %s: %w", p.Addr, p.Err)
			}
		case p.Unknown:
			reachable++
			unknown++
		default:
			reachable++
			withPrimary++
		}
	}
	switch {
	case withPrimary > 0:
		// The Sentinels know the primary; the fault is on the data node.
	case reachable > 0 && unknown == reachable:
		return true, fmt.Errorf("no Sentinel knows the primary name %q: %w", primaryName, errNoPrimary)
	case reachable == 0 && firstConfig != nil:
		return true, firstConfig
	}
	if connectErr != nil && isConfigError(classify(connectErr)) {
		return true, connectErr
	}
	return false, connectErr
}

// describeConnectError is the reason shown in the status of a client that has
// no connection yet.
func describeConnectError(cause error, probes []sentinelProbe) string {
	if len(probes) > 0 {
		reachable := 0
		for _, p := range probes {
			if p.Err == nil {
				reachable++
			}
		}
		if reachable == 0 {
			return fmt.Sprintf("no Sentinel reachable (0 of %d): %s", len(probes), shorten(cause))
		}
	}
	return shorten(cause)
}

func shorten(err error) string {
	if err == nil {
		return "unknown"
	}
	s := err.Error()
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return strings.TrimSpace(s)
}

// helloReply reads the HELLO reply into a map of the string fields.
func helloReply(ctx context.Context, c valkey.Client) (map[string]string, error) {
	m, err := c.Do(ctx, c.B().Arbitrary("HELLO", "3").Build()).ToMap()
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, 2)
	for _, k := range []string{"server", "version"} {
		if msg, ok := m[k]; ok {
			s, err := msg.ToString()
			if err != nil {
				return nil, errors.New("HELLO reply field " + k + " is not a string")
			}
			out[k] = s
		}
	}
	return out, nil
}
