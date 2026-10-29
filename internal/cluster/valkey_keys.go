package cluster

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// MaxValueSize is the largest value or payload trove stores or publishes
// (R-ARCH-030: small values).
const MaxValueSize = 1024

// A key is <key_prefix><keyspace>:<mechanism>:v<version>[:<part>...], a channel
// the same without expiry (R-ARCH-030). The only way to obtain a [Key] is a
// [KeyFamily], and a family exists only with an expiry, so a key without one
// cannot be written: the write methods of [Valkey] take a Key and take the
// expiry from it. Families are registered, and keys formed, inside this
// package only (Q-ARCH-16, see valkey_ops.go).

var (
	mechanismPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(:[a-z][a-z0-9_]*)*$`)
	// partPattern admits identifiers and hashes only. It keeps raw names
	// (paths, package names, addresses) out of keys by refusing the characters
	// they need, and it keeps a part from adding a segment or a glob.
	partPattern     = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	keyspacePattern = regexp.MustCompile(`^[0-9a-fA-F]{12,}$`)

	families = struct {
		mu       sync.Mutex
		keys     map[string]*KeyFamily
		channels map[string]*ChannelFamily
	}{keys: map[string]*KeyFamily{}, channels: map[string]*ChannelFamily{}}
)

// KeyFamily is one registered kind of key: a mechanism, its encoding version
// and the longest expiry its keys may have.
type KeyFamily struct {
	mechanism string
	version   int
	maxTTL    time.Duration
}

// ChannelFamily is one registered kind of Pub/Sub channel.
type ChannelFamily struct {
	mechanism string
	version   int
}

// registerKeyFamily registers a key family. It is meant for package
// initialisation of the owning mechanism and panics on a programming error: an
// invalid or duplicate mechanism name, a version below 1, or a maxTTL that is
// not positive (every key expires, R-ARCH-030).
func registerKeyFamily(mechanism string, version int, maxTTL time.Duration) *KeyFamily {
	checkFamily(mechanism, version)
	if maxTTL <= 0 {
		panic(fmt.Sprintf("cluster: key family %q needs a positive expiry: every key expires", mechanism))
	}
	families.mu.Lock()
	defer families.mu.Unlock()
	checkUnique(mechanism, version)
	f := &KeyFamily{mechanism: mechanism, version: version, maxTTL: maxTTL}
	families.keys[famID(mechanism, version)] = f
	return f
}

// registerChannelFamily registers a Pub/Sub channel family, with the same
// panics as registerKeyFamily except the expiry, which channels do not have.
func registerChannelFamily(mechanism string, version int) *ChannelFamily {
	checkFamily(mechanism, version)
	families.mu.Lock()
	defer families.mu.Unlock()
	checkUnique(mechanism, version)
	f := &ChannelFamily{mechanism: mechanism, version: version}
	families.channels[famID(mechanism, version)] = f
	return f
}

func famID(mechanism string, version int) string { return fmt.Sprintf("%s:v%d", mechanism, version) }

func checkFamily(mechanism string, version int) {
	if !mechanismPattern.MatchString(mechanism) {
		panic(fmt.Sprintf("cluster: invalid key mechanism %q", mechanism))
	}
	if version < 1 {
		panic(fmt.Sprintf("cluster: key family %q needs a version of 1 or more", mechanism))
	}
}

// checkUnique must run with families.mu held.
func checkUnique(mechanism string, version int) {
	id := famID(mechanism, version)
	if _, ok := families.keys[id]; ok {
		panic(fmt.Sprintf("cluster: family %s is registered twice", id))
	}
	if _, ok := families.channels[id]; ok {
		panic(fmt.Sprintf("cluster: family %s is registered twice", id))
	}
}

// familyInfo describes a registered family for documentation and tests.
type familyInfo struct {
	Mechanism string
	Version   int
	// MaxTTL is zero for a channel family.
	MaxTTL time.Duration
}

// registeredFamilies lists every registered family, sorted by name.
func registeredFamilies() []familyInfo {
	families.mu.Lock()
	defer families.mu.Unlock()
	out := make([]familyInfo, 0, len(families.keys)+len(families.channels))
	for _, f := range families.keys {
		out = append(out, familyInfo{f.mechanism, f.version, f.maxTTL})
	}
	for _, f := range families.channels {
		out = append(out, familyInfo{f.mechanism, f.version, 0})
	}
	sort.Slice(out, func(i, j int) bool {
		return famID(out[i].Mechanism, out[i].Version) < famID(out[j].Mechanism, out[j].Version)
	})
	return out
}

// Key is a Valkey key together with its expiry. Its zero value is invalid and
// every write rejects it.
type Key struct {
	name string
	ttl  time.Duration
}

// String returns the full key name.
func (k Key) String() string { return k.name }

// TTL returns the expiry every write of the key sets.
func (k Key) TTL() time.Duration { return k.ttl }

// Channel is a Pub/Sub channel name. Its zero value is invalid.
type Channel struct{ name string }

// String returns the full channel name.
func (c Channel) String() string { return c.name }

// normalizeKeyspace validates a keyspace ID and returns the 12 lowercase hex
// digits that name the keyspace (R-ARCH-030).
func normalizeKeyspace(id string) (string, error) {
	if !keyspacePattern.MatchString(id) {
		return "", errors.New("keyspace ID must be at least 12 hex digits (system_state.keyspace_id)")
	}
	return strings.ToLower(id[:12]), nil
}

// SetKeyspaceID sets the keyspace once, for a caller that learns it after
// [NewValkey]. The keyspace is read from PostgreSQL at start-up and never
// changes while the process runs (R-ARCH-030), so a second call with another
// value fails.
func (v *Valkey) SetKeyspaceID(id string) error {
	ks, err := normalizeKeyspace(id)
	if err != nil {
		return err
	}
	if !v.keyspace.CompareAndSwap(nil, &ks) {
		if cur := v.keyspace.Load(); cur != nil && *cur != ks {
			return fmt.Errorf("keyspace is already %s; it does not change while the process runs", *cur)
		}
	}
	return nil
}

func (v *Valkey) prefix() (string, error) {
	ks := v.keyspace.Load()
	if ks == nil {
		return "", ErrNoKeyspace
	}
	return v.cfg.KeyPrefix + *ks + ":", nil
}

func checkParts(parts []string) error {
	for _, p := range parts {
		if !partPattern.MatchString(p) {
			return fmt.Errorf("key part %q must be an identifier or hash ([A-Za-z0-9_-], 1 to 128 characters)", p)
		}
	}
	return nil
}

func join(prefix, mechanism string, version int, parts []string) string {
	var b strings.Builder
	b.WriteString(prefix)
	b.WriteString(famID(mechanism, version))
	for _, p := range parts {
		b.WriteByte(':')
		b.WriteString(p)
	}
	return b.String()
}

// formKey forms a key of the family with the family's longest expiry. The
// parts are identifiers or hashes, never raw names (R-ARCH-030).
func (v *Valkey) formKey(f *KeyFamily, parts ...string) (Key, error) {
	if f == nil {
		return Key{}, errors.New("cluster: nil key family")
	}
	return v.formKeyTTL(f, f.maxTTL, parts...)
}

// formKeyTTL forms a key whose expiry is ttl, which must be positive and at
// most the family's longest expiry.
func (v *Valkey) formKeyTTL(f *KeyFamily, ttl time.Duration, parts ...string) (Key, error) {
	if f == nil || f.mechanism == "" {
		return Key{}, errors.New("cluster: key family not registered")
	}
	if ttl <= 0 || ttl > f.maxTTL {
		return Key{}, fmt.Errorf("cluster: expiry %s of family %s must be positive and at most %s", ttl, famID(f.mechanism, f.version), f.maxTTL)
	}
	if err := checkParts(parts); err != nil {
		return Key{}, err
	}
	p, err := v.prefix()
	if err != nil {
		return Key{}, err
	}
	return Key{name: join(p, f.mechanism, f.version, parts), ttl: ttl}, nil
}

// channel forms a channel name of the family.
func (v *Valkey) channel(f *ChannelFamily, parts ...string) (Channel, error) {
	if f == nil || f.mechanism == "" {
		return Channel{}, errors.New("cluster: channel family not registered")
	}
	if err := checkParts(parts); err != nil {
		return Channel{}, err
	}
	p, err := v.prefix()
	if err != nil {
		return Channel{}, err
	}
	return Channel{name: join(p, f.mechanism, f.version, parts)}, nil
}

// Key families of the M1 areas. A mechanism lives in its own valkey_<mechanism>.go
// file of this package (Q-ARCH-16); the family variable is declared here, inside
// the marker of the area that owns the mechanism, so that parallel work does not
// edit the same lines. Reserved names are in docs/developer/m1-contracts.md.

// >>> M1 api

// rlFamily is the key family of the rate-limit counters (R-ARCH-034, mechanism in
// valkey_ratelimit.go). Its longest expiry bounds the longest window of a
// limiter to half of it, because a counter lives two windows.
var rlFamily = registerKeyFamily("rl", 1, 24*time.Hour)

// <<< M1 api
//
// >>> M1 identity

// tokSeenFamily holds the throttle-exemption markers of recently validated
// credentials (R-AUTH-038, R-ARCH-030). The longest expiry is one hour; the
// configured recent_token_window must not exceed it.
var tokSeenFamily = registerKeyFamily("tokseen", 1, MaxTokenSeenWindow)

// <<< M1 identity
