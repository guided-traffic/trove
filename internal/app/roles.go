package app

import "fmt"

// Subsystems is the set a role enables (R-ARCH-001, INV-2). Every replica of a
// role runs the same set and is interchangeable with any other of that role.
type Subsystems struct {
	// ClientListener serves :8080: the UI, the management API, the package protocols.
	ClientListener bool
	// OpsListener serves :8081: probes and metrics.
	OpsListener bool
	// MainPool is the transaction-scoped PostgreSQL pool.
	MainPool bool
	// LeasePool holds session-scoped connections for leases and singleton jobs.
	LeasePool bool
	// JobLoops are the claim loop, the reaper and the scheduler.
	JobLoops bool
	// Valkey is the Valkey client.
	Valkey bool
	// ChangePoll is the change-log poll and the signal subscriber.
	ChangePoll bool
	// Registry is the replica registry row and its heartbeat.
	Registry bool
}

// SubsystemsFor returns the subsystems of a serving role.
func SubsystemsFor(role string) (Subsystems, error) {
	switch role {
	case "web":
		return Subsystems{ClientListener: true, OpsListener: true, MainPool: true, Valkey: true, ChangePoll: true, Registry: true}, nil
	case "worker":
		return Subsystems{OpsListener: true, MainPool: true, LeasePool: true, JobLoops: true, Valkey: true, ChangePoll: true, Registry: true}, nil
	case "all":
		return Subsystems{ClientListener: true, OpsListener: true, MainPool: true, LeasePool: true, JobLoops: true, Valkey: true, ChangePoll: true, Registry: true}, nil
	}
	return Subsystems{}, fmt.Errorf("process.role must be web, worker or all (got %q)", role)
}
