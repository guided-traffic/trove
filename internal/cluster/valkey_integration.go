//go:build integration

package cluster

import (
	"context"
	"time"
)

// This file is compiled only with the integration build tag, which the
// integration tests use and no build of the binary does. It gives the tests of
// test/integration/valkey the raw commands and the family registration that the
// package otherwise keeps to itself (Q-ARCH-16: outside internal/cluster no code
// reads or writes a Valkey key). TestValkeySurface checks that no other file is
// tag-gated and that the untagged build exports none of this.

// IntegrationRaw exposes the raw commands of a client to the integration tests.
type IntegrationRaw struct{ v *Valkey }

// Raw returns the raw commands of v. Integration tests only.
func Raw(v *Valkey) IntegrationRaw { return IntegrationRaw{v: v} }

// RegisterTestKeyFamily registers a key family. Integration tests only.
func RegisterTestKeyFamily(mechanism string, version int, maxTTL time.Duration) *KeyFamily {
	return registerKeyFamily(mechanism, version, maxTTL)
}

// RegisterTestChannelFamily registers a channel family. Integration tests only.
func RegisterTestChannelFamily(mechanism string, version int) *ChannelFamily {
	return registerChannelFamily(mechanism, version)
}

// Key forms a key with the family's longest expiry.
func (r IntegrationRaw) Key(f *KeyFamily, parts ...string) (Key, error) {
	return r.v.formKey(f, parts...)
}

// KeyTTL forms a key with the expiry ttl.
func (r IntegrationRaw) KeyTTL(f *KeyFamily, ttl time.Duration, parts ...string) (Key, error) {
	return r.v.formKeyTTL(f, ttl, parts...)
}

// Channel forms a channel name.
func (r IntegrationRaw) Channel(f *ChannelFamily, parts ...string) (Channel, error) {
	return r.v.channel(f, parts...)
}

// Ping is the raw PING.
func (r IntegrationRaw) Ping(ctx context.Context) error { return r.v.ping(ctx) }

// Get is the raw GET.
func (r IntegrationRaw) Get(ctx context.Context, k Key) (string, bool, error) { return r.v.get(ctx, k) }

// Set is the raw SET with the key's expiry.
func (r IntegrationRaw) Set(ctx context.Context, k Key, value string) error {
	return r.v.set(ctx, k, value)
}

// SetNX is the raw SET NX with the key's expiry.
func (r IntegrationRaw) SetNX(ctx context.Context, k Key, value string) (bool, error) {
	return r.v.setNX(ctx, k, value)
}

// SetIfEq is the raw SET IFEQ with the key's expiry.
func (r IntegrationRaw) SetIfEq(ctx context.Context, k Key, old, value string) (bool, error) {
	return r.v.setIfEq(ctx, k, old, value)
}

// DelIfEq is the raw DELIFEQ.
func (r IntegrationRaw) DelIfEq(ctx context.Context, k Key, value string) (bool, error) {
	return r.v.delIfEq(ctx, k, value)
}

// Incr is the raw INCR with the expiry set once.
func (r IntegrationRaw) Incr(ctx context.Context, k Key) (int64, error) { return r.v.incr(ctx, k) }

// Publish is the raw PUBLISH.
func (r IntegrationRaw) Publish(ctx context.Context, ch Channel, msg string) error {
	return r.v.publish(ctx, ch, msg)
}

// Subscribe registers a handler on the Pub/Sub connection.
func (r IntegrationRaw) Subscribe(ctx context.Context, ch Channel, h Handler) (func(), error) {
	return r.v.subscribe(ctx, ch, h)
}
