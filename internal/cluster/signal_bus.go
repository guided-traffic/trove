package cluster

import "context"

// Bus is what the change log and the signals need of Valkey: Pub/Sub and its
// availability. *Valkey implements it; tests use a fake. A nil Bus means
// "never available", which is what a replica without Valkey sees.
type Bus interface {
	// Available reports whether calls reach Valkey right now.
	Available() bool
	Publish(ctx context.Context, ch Channel, msg string) error
	Subscribe(ctx context.Context, ch Channel, h Handler) (cancel func(), err error)
	Channel(f *ChannelFamily, parts ...string) (Channel, error)
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
	invChannels = RegisterChannelFamily("inv", 1)
	sigChannels = RegisterChannelFamily("sig", 1)
)
