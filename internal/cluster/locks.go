package cluster

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/guided-traffic/trove/internal/meta"
)

// The advisory-lock categories of R-ARCH-017. Every advisory lock uses the
// two-int4 form and its first key is one of these. No category hashes its
// second key. A new category is added here and in R-ARCH-017's table, never as
// an ad-hoc key; the source test of this package fails on any other use.
const (
	// LockLease is a lease of a worker replica: a session lock on a lease-pool
	// connection, second key = the registered lease number (R-HA-006).
	LockLease int32 = 1
	// LockFetchRetired is reserved and never used again: the fetch lock lives
	// in Valkey since 2026-10-28 (R-ARCH-033). The number is never reused.
	LockFetchRetired int32 = 2
	// LockMigration is taken by the migration runner and by backup and restore,
	// second key 0. internal/meta/postgres keeps its own copy of the number
	// (a test of this package compares both) because it must not import this
	// package.
	LockMigration int32 = 3
	// LockConfig is a transaction-scoped configuration lock, second key = the
	// registered object type below.
	LockConfig int32 = 4
)

// Object types of LockConfig.
const (
	// ConfigVirtualMembership serialises virtual-membership changes.
	ConfigVirtualMembership int32 = 1
	// ConfigDocuments serialises the apply of configuration documents.
	ConfigDocuments int32 = 2
)

// LockConfigObject takes the transaction-scoped configuration lock of an
// object type as the first statement of a configuration transaction. It ends
// with the transaction and works through a transaction-mode pooler.
func LockConfigObject(ctx context.Context, tx meta.Tx, object int32) error {
	if object != ConfigVirtualMembership && object != ConfigDocuments {
		return fmt.Errorf("cluster: %d is not a registered configuration lock object", object)
	}
	_, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1::int, $2::int)", LockConfig, object)
	return err
}

// LeaseID names a lease and carries its registered number, the second key of
// LockLease.
type LeaseID struct {
	name   string
	number int32
}

// Name is the lease's name as the replica registry and the metrics show it.
func (l LeaseID) Name() string { return l.name }

// Number is the registered lease number.
func (l LeaseID) Number() int32 { return l.number }

var leaseIDs = struct {
	mu     sync.Mutex
	byName map[string]LeaseID
	byNum  map[int32]LeaseID
}{byName: map[string]LeaseID{}, byNum: map[int32]LeaseID{}}

// RegisterLease registers a lease number. It panics on a duplicate name or
// number or on a number below 1. Package initialisation of the owning package
// calls it.
func RegisterLease(name string, number int32) LeaseID {
	if !areaPattern.MatchString(name) {
		panic(fmt.Sprintf("cluster: invalid lease name %q", name))
	}
	if number < 1 {
		panic(fmt.Sprintf("cluster: lease %q needs a number of 1 or more", name))
	}
	leaseIDs.mu.Lock()
	defer leaseIDs.mu.Unlock()
	if _, ok := leaseIDs.byName[name]; ok {
		panic(fmt.Sprintf("cluster: lease %q is registered twice", name))
	}
	if o, ok := leaseIDs.byNum[number]; ok {
		panic(fmt.Sprintf("cluster: lease number %d is taken by %q", number, o.name))
	}
	id := LeaseID{name: name, number: number}
	leaseIDs.byName[name] = id
	leaseIDs.byNum[number] = id
	return id
}

// Leases lists the registered leases by number.
func RegisteredLeases() []LeaseID {
	leaseIDs.mu.Lock()
	defer leaseIDs.mu.Unlock()
	out := make([]LeaseID, 0, len(leaseIDs.byNum))
	for _, id := range leaseIDs.byNum {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].number < out[j].number })
	return out
}

// LeaseScheduler is the lease of the job scheduler (R-ARCH-016): lease number 1.
var LeaseScheduler = RegisterLease("scheduler", 1)
