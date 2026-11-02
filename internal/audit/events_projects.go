package audit

// The event types of projects (R-AUTHZ-026, R-AUTHZ-029, Q-AUTHZ-185). Every write
// to a project, its administrators or an assignment is Transactional and carries
// the authorization epoch it produced. The events that take access away carry the
// journal entry of R-AUTHZ-031 (the project, the principal or repository by ID),
// so that a restore cannot bring the access back: a project deleted or narrowed, an
// administrator removed, a repository unassigned. A move between projects is two
// events in one transaction, an unassignment from the old project and an
// assignment to the new one.
//
// Project service accounts and their tokens are recorded by the identity events
// (serviceaccount.*, token.*), which carry the member project.
const (
	EventProjectCreated              = "authz.project.created"
	EventProjectUpdated              = "authz.project.updated"
	EventProjectDeleted              = "authz.project.deleted"
	EventProjectAdminAdded           = "authz.project.admin_added"
	EventProjectAdminRemoved         = "authz.project.admin_removed"
	EventProjectRepositoryAssigned   = "authz.project.repository_assigned"
	EventProjectRepositoryUnassigned = "authz.project.repository_unassigned"
)

func init() {
	// before and after of an update hold displayName, description, managePolicies and
	// manageRepositories; the journal entry carries the two switches, for the
	// comparator of R-AUTHZ-031.
	project := `{"type":"object","properties":{"epoch":{"type":"integer"},"project":{"type":"string"},"projectId":{"type":"string"},` +
		`"version":{"type":"integer"},"before":{"type":["object","null"]},"after":{"type":["object","null"]},` +
		`"policiesDeleted":{"type":"array","maxItems":1000,"items":{"type":"string"}}}}`
	Register(EventType{Name: EventProjectCreated, Category: CategoryAuthz, Mode: Transactional, Schema: project})
	Register(EventType{Name: EventProjectUpdated, Category: CategoryAuthz, Mode: Transactional, Journal: JournalRevocation, Schema: project})
	Register(EventType{Name: EventProjectDeleted, Category: CategoryAuthz, Mode: Transactional, Journal: JournalRevocation, Schema: project})
	admin := `{"type":"object","properties":{"epoch":{"type":"integer"},"project":{"type":"string"},"projectId":{"type":"string"},` +
		`"principal":{"type":"string"},"principalId":{"type":"string"}}}`
	Register(EventType{Name: EventProjectAdminAdded, Category: CategoryAuthz, Mode: Transactional, Schema: admin})
	Register(EventType{Name: EventProjectAdminRemoved, Category: CategoryAuthz, Mode: Transactional, Journal: JournalRevocation, Schema: admin})
	assignment := `{"type":"object","properties":{"epoch":{"type":"integer"},"project":{"type":"string"},"projectId":{"type":"string"},` +
		`"repository":{"type":"string"},"repositoryId":{"type":"string"},"moved":{"type":"boolean"},"otherProject":{"type":"string"}}}`
	Register(EventType{Name: EventProjectRepositoryAssigned, Category: CategoryAuthz, Mode: Transactional, Schema: assignment})
	Register(EventType{Name: EventProjectRepositoryUnassigned, Category: CategoryAuthz, Mode: Transactional, Journal: JournalRevocation, Schema: assignment})
}
