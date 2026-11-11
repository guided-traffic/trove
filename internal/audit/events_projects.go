package audit

// The event types of projects (R-AUTHZ-026, R-AUTHZ-029, R-AUTHZ-031, Q-AUTHZ-185).
// Every write to a project, its members or the project of a repository is
// Transactional, carries the authorization epoch it produced and names the project
// (Event.Project, Q-AUTHZ-209). The events that take access away carry the journal
// entry of R-AUTHZ-031 so that a restore cannot bring the access back: a project
// deleted, a member removed, a role changed (the comparator replays only a role that
// is not higher, so a raise is journaled too and never replayed as one), a
// repository moved to another project. The display name and the description of a
// project are not access-bearing, so its update is not journaled (R-AUTHZ-031).
//
// Project service accounts and their tokens are recorded by the identity events
// (serviceaccount.*, token.*), which carry the member project.
const (
	EventProjectCreated       = "authz.project.created"
	EventProjectUpdated       = "authz.project.updated"
	EventProjectDeleted       = "authz.project.deleted"
	EventProjectMemberAdded   = "authz.project.member_added"
	EventProjectMemberChanged = "authz.project.member_changed"
	EventProjectMemberRemoved = "authz.project.member_removed"
	// EventProjectVisibilityChanged is a change of a project's visibility level (R-AUTHZ-036): the level before and after and the
	// repositories the change lowered. It takes access away when the level is lowered, so it carries the journal entry; the
	// comparator replays only a level that is not higher than the current one.
	EventProjectVisibilityChanged = "authz.project.visibility_changed"
	// EventRepositoryMoved is the move of a repository to another project (R-REPO-060): the
	// repository's ID, the source and the target project. It is recorded in the source project
	// under the repository's former name and carries the journal entry.
	EventRepositoryMoved = "repository.moved"
	// EventRepositoryMovedIn is the same move recorded in the target project under the
	// repository's new name, so that the audit of each project shows it (Q-REPO-233). It has the
	// details of repository.moved and no journal entry: the restore reads the move once.
	EventRepositoryMovedIn = "repository.moved_in"
)

func init() {
	// before and after of an update hold displayName and description, which are not
	// access-bearing; the journal entry of a delete names the project.
	project := `{"type":"object","properties":{"epoch":{"type":"integer"},"project":{"type":"string"},"projectId":{"type":"string"},` +
		`"version":{"type":"integer"},"before":{"type":["object","null"]},"after":{"type":["object","null"]},` +
		`"policiesDeleted":{"type":"array","maxItems":1000,"items":{"type":"string"}}}}`
	Register(EventType{Name: EventProjectCreated, Category: CategoryAuthz, Mode: Transactional, Schema: project})
	Register(EventType{Name: EventProjectUpdated, Category: CategoryAuthz, Mode: Transactional, Schema: project})
	Register(EventType{Name: EventProjectDeleted, Category: CategoryAuthz, Mode: Transactional, Journal: JournalRevocation, Schema: project})
	// role is the role a member was added or removed with; before and after the roles of a
	// change (reader, developer, maintainer, owner).
	member := `{"type":"object","properties":{"epoch":{"type":"integer"},"project":{"type":"string"},"projectId":{"type":"string"},` +
		`"principal":{"type":"string"},"principalId":{"type":"string"},` +
		`"role":{"type":"string","enum":["reader","developer","maintainer","owner"]},` +
		`"before":{"type":"string","enum":["reader","developer","maintainer","owner"]},` +
		`"after":{"type":"string","enum":["reader","developer","maintainer","owner"]}},` +
		`"required":["epoch","project","projectId","principal","principalId"],"additionalProperties":false}`
	Register(EventType{Name: EventProjectMemberAdded, Category: CategoryAuthz, Mode: Transactional, Schema: member})
	Register(EventType{Name: EventProjectMemberChanged, Category: CategoryAuthz, Mode: Transactional, Journal: JournalRevocation, Schema: member})
	Register(EventType{Name: EventProjectMemberRemoved, Category: CategoryAuthz, Mode: Transactional, Journal: JournalRevocation, Schema: member})
	visibility := `{"type":"object","properties":{"epoch":{"type":"integer"},"project":{"type":"string"},"projectId":{"type":"string"},` +
		`"before":{"type":"string","enum":["private","internal","public"]},"after":{"type":"string","enum":["private","internal","public"]},` +
		`"lowered":{"type":"array","maxItems":1000,"items":{"type":"object","properties":{"repository":{"type":"string"},` +
		`"repositoryId":{"type":"string"},"before":{"type":"object"},"after":{"type":"object"}}}}},` +
		`"required":["epoch","project","projectId","before","after"]}`
	Register(EventType{Name: EventProjectVisibilityChanged, Category: CategoryAuthz, Mode: Transactional, Journal: JournalRevocation, Schema: visibility})
	// from and to are the project keys, fromId and toId their IDs; repository is the key, which
	// the move keeps.
	moved := `{"type":"object","properties":{"epoch":{"type":"integer"},"repository":{"type":"string"},"repositoryId":{"type":"string"},` +
		`"from":{"type":"string"},"fromId":{"type":"string"},"to":{"type":"string"},"toId":{"type":"string"},` +
		`"configVersion":{"type":"integer"}},` +
		`"required":["epoch","repository","repositoryId","from","fromId","to","toId"],"additionalProperties":false}`
	Register(EventType{Name: EventRepositoryMoved, Category: CategoryRepository, Mode: Transactional, Journal: JournalRevocation, Schema: moved})
	Register(EventType{Name: EventRepositoryMovedIn, Category: CategoryRepository, Mode: Transactional, Schema: moved})
}
