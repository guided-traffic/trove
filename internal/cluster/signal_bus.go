package cluster

import "context"

// Bus is what the change log and the signals need of Valkey: Pub/Sub and its
// availability. Its methods are unexported, which seals it to this package
// (Q-ARCH-16, R-ARCH-030): no other package can implement or call it. *Valkey implements it; tests use a fake. A nil Bus means
// "never available", which is what a replica without Valkey sees.
type Bus interface {
	// Available reports whether calls reach Valkey right now.
	Available() bool
	publish(ctx context.Context, ch Channel, msg string) error
	subscribe(ctx context.Context, ch Channel, h Handler) (cancel func(), err error)
	channel(f *ChannelFamily, parts ...string) (Channel, error)
	// OnSubscribed registers fn to run each time the Pub/Sub connection has
	// subscribed every wanted channel; reconnect is false the first time.
	OnSubscribed(fn func(reconnect bool))
	// CountDegraded records that a mechanism ran its fallback.
	CountDegraded(mechanism string)
}

var _ Bus = (*Valkey)(nil)

// The channel families of R-ARCH-018: trove:<ks>:inv:v1 carries the change
// messages, trove:<ks>:sig:v1:<area> the signals.
var (
	invChannels = registerChannelFamily("inv", 1)
	sigChannels = registerChannelFamily("sig", 1)
)
