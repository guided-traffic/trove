package audit

// The events of the Bastion registrations on the main installation (R-BAS-004). A registration is a principal
// of the kind bastion; its events are about the registration, so their resource type is bastion, and the token
// events of a Bastion token (token.created, token.revoked, registered with the identity events) carry the same
// resource type. None of the three registration events carries a journal flag: the revocation feed tells a
// Bastion what changed for the callers it answers (R-BAS-015), and a registration is not a caller; what ends a
// Bastion token is its token.revoked, which is journaled.

// Names of the Bastion registration events, for the producers.
const (
	EventBastionCreated = "bastion.created"
	EventBastionUpdated = "bastion.updated"
	EventBastionDeleted = "bastion.deleted"
)

func init() {
	bastion := func(name, schema string) {
		Register(EventType{Name: name, Category: CategoryIdentity, Mode: Transactional, Webhook: true, Journal: NoJournal, Schema: schema})
	}
	bastion(EventBastionCreated, `{"type":"object","properties":{"source":{"type":"string","enum":["api","admin-cli"]},"description":{"type":"string"}}}`)
	bastion(EventBastionUpdated, `{"type":"object","properties":{"fields":`+eventSchemaNames+`,"enabled":{"type":"boolean"},"tokensRevoked":{"type":"integer"}}}`)
	bastion(EventBastionDeleted, `{"type":"object","properties":{"tokensRevoked":{"type":"integer"}}}`)
}
