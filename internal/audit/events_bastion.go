package audit

// The events of the Bastion registrations on the main installation (R-BAS-004). A registration is a principal
// of the kind bastion; its events are about the registration, so their resource type is bastion, and the token
// events of a Bastion token (token.created, token.revoked, registered with the identity events) carry the same
// resource type. A disable and a deletion end what the registration's tokens may do, so both are journaled
// (R-HA-054, Q-BAS-145): a disable revokes nothing, and without its entry a restore to a point before it would
// accept the registration's tokens again. Their entry is the registration's principal ID and name; the replay
// disables the registration. A deletion also journals each token it revokes (token.revoked). bastion.updated
// (the description) and bastion.enabled narrow nothing and carry no entry.

// Names of the Bastion registration events, for the producers.
const (
	EventBastionCreated  = "bastion.created"
	EventBastionUpdated  = "bastion.updated"
	EventBastionDisabled = "bastion.disabled"
	EventBastionEnabled  = "bastion.enabled"
	EventBastionDeleted  = "bastion.deleted"
)

func init() {
	bastion := func(name string, journal Journal, schema string) {
		Register(EventType{Name: name, Category: CategoryIdentity, Mode: Transactional, Webhook: true, Journal: journal, Schema: schema})
	}
	bastion(EventBastionCreated, NoJournal, `{"type":"object","properties":{"source":{"type":"string","enum":["api","admin-cli"]},"description":{"type":"string"}}}`)
	bastion(EventBastionUpdated, NoJournal, `{"type":"object","properties":{"fields":`+eventSchemaNames+`}}`)
	bastion(EventBastionDisabled, JournalRevocation, `{"type":"object"}`)
	bastion(EventBastionEnabled, NoJournal, `{"type":"object"}`)
	bastion(EventBastionDeleted, JournalRevocation, `{"type":"object","properties":{"tokensRevoked":{"type":"integer"}}}`)
}
