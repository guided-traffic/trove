package audit

// The event types of authorization (R-AUTHZ-029). Names follow the contract of
// docs/developer/m1-contracts.md; the journal flag marks the events that remove
// access or update an access-bearing object, so a restore cannot undo them
// (R-AUTHZ-031, R-OPS-014).
func init() {
	// A refused decision: read, write or management operation. Asynchronous and
	// deduplicated by the audit writer per replica, principal, required_action,
	// repository and path (R-OPS-015, Q-AUTHZ-24): the producer sends every
	// refusal. Reads are written only with authz.audit.reads denied or all.
	Register(EventType{
		Name: "authz.denied", Category: CategoryAuthz, Mode: Async,
		Schema: `{"type":"object","properties":{` +
			`"reason":{"type":"string"},"status":{"type":"integer"},` +
			`"actions":{"type":"array","maxItems":6,"items":{"type":"string"}},` +
			`"repository":{"type":"string"},"class":{"type":"string"},"format":{"type":"string"},` +
			`"package":{"type":"string"},"path":{"type":"string"},"permission":{"type":"string"},` +
			`"credential":{"type":"string"},"endpoint":{"type":"string"},"replica":{"type":"string"},` +
			`"epoch":{"type":"integer"},"required_action":{"type":"string"},` +
			`"count":{"type":"integer"},"repeat_of":{"type":"string"}}}`,
	})
	// An allowed read, written only with authz.audit.reads all (Q-AUTHZ-5).
	Register(EventType{
		Name: "authz.read.allowed", Category: CategoryAuthz, Mode: Async,
		Schema: `{"type":"object","properties":{` +
			`"reason":{"type":"string"},"actions":{"type":"array","maxItems":6,"items":{"type":"string"}},` +
			`"repository":{"type":"string"},"package":{"type":"string"},"path":{"type":"string"},` +
			`"sources":{"type":"array","maxItems":1000,"items":{"type":"string"}},` +
			`"credential":{"type":"string"},"endpoint":{"type":"string"},"replica":{"type":"string"},"epoch":{"type":"integer"}}}`,
	})
	// An allowed write or populate, recorded by the writer in the transaction of
	// the change: if the audit row cannot be written the change fails.
	Register(EventType{
		Name: "authz.write.allowed", Category: CategoryAuthz, Mode: Transactional,
		Schema: `{"type":"object","properties":{` +
			`"reason":{"type":"string"},"actions":{"type":"array","maxItems":6,"items":{"type":"string"}},` +
			`"repository":{"type":"string"},"package":{"type":"string"},"path":{"type":"string"},` +
			`"sources":{"type":"array","maxItems":1000,"items":{"type":"string"}},` +
			`"credential":{"type":"string"},"endpoint":{"type":"string"},"replica":{"type":"string"},"epoch":{"type":"integer"}}}`,
	})
	policy := `{"type":"object","properties":{"epoch":{"type":"integer"},` +
		`"before":{"type":["object","null"]},"after":{"type":["object","null"]}}}`
	Register(EventType{Name: "authz.policy.created", Category: CategoryAuthz, Mode: Transactional, Schema: policy})
	Register(EventType{Name: "authz.policy.updated", Category: CategoryAuthz, Mode: Transactional, Journal: JournalRevocation, Schema: policy})
	Register(EventType{Name: "authz.policy.deleted", Category: CategoryAuthz, Mode: Transactional, Journal: JournalRevocation, Schema: policy})
	binding := `{"type":"object","properties":{"role":{"type":"string"},"principal":{"type":"string"},"principalId":{"type":"string"},"epoch":{"type":"integer"}}}`
	Register(EventType{Name: "authz.role.bound", Category: CategoryAuthz, Mode: Transactional, Schema: binding})
	Register(EventType{Name: "authz.role.unbound", Category: CategoryAuthz, Mode: Transactional, Journal: JournalRevocation, Schema: binding})
	// The authorization settings of a repository (cacheFillAction,
	// packageOwnership, requireAuthentication, hideExistence), written by the
	// repository update next to the repository's own event (R-AUTHZ-031).
	Register(EventType{
		Name: "authz.settings.changed", Category: CategoryAuthz, Mode: Transactional, Journal: JournalRevocation,
		Schema: `{"type":"object","properties":{"repository":{"type":"string"},"repositoryId":{"type":"string"},` +
			`"before":{"type":"object"},"after":{"type":"object"},"epoch":{"type":"integer"}}}`,
	})
	// >>> M2 ownership
	// Package ownership (R-AUTHZ-018, R-AUTHZ-019, R-AUTHZ-029), written in the
	// transaction of the change: the claim of a first publish, an owner added
	// through the API (a pre-assignment included), an owner removed and the
	// release of a name with its last owner. Removing and releasing take access
	// away, so they carry the journal entry of R-AUTHZ-031 (domain, format, name,
	// the owner by ID). claim is the ID stored in package_ownership.claim_event.
	ownership := `{"type":"object","properties":{` +
		`"repository":{"type":"string"},"domain":{"type":"string"},"format":{"type":"string"},` +
		`"package":{"type":"string"},"claim":{"type":"string"},"reason":{"type":"string"},` +
		`"owner":{"type":"string"},"ownerId":{"type":"string"},` +
		`"owners":{"type":"array","maxItems":1000,"items":{"type":"string"}}}}`
	Register(EventType{Name: "authz.ownership.claimed", Category: CategoryAuthz, Mode: Transactional, Schema: ownership})
	Register(EventType{Name: "authz.ownership.added", Category: CategoryAuthz, Mode: Transactional, Schema: ownership})
	Register(EventType{Name: "authz.ownership.removed", Category: CategoryAuthz, Mode: Transactional, Journal: JournalRevocation, Schema: ownership})
	Register(EventType{Name: "authz.ownership.released", Category: CategoryAuthz, Mode: Transactional, Journal: JournalRevocation, Schema: ownership})
	// <<< M2 ownership
}
