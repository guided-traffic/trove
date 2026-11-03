package audit

// The events of the Bastion interface that the main installation writes itself (R-BAS-010, R-BAS-016): the
// change of what a Bastion replica reports about itself, and the count of download events a Bastion lost.
// Both are asynchronous: they record access to the interface and never gate a change. The events of the
// registration lifecycle (bastion.created and the like) belong to the registration service (R-BAS-004).
// The actor is the Bastion registration (kind bastion) with the Bastion token that authenticated the call.

// Names of the interface events, for the producers.
const (
	EventBastionContact          = "bastion.contact"
	EventBastionDownloadsDropped = "bastion.downloads.dropped"
)

func init() {
	// A replica reported for the first time, or one of its reported values changed: the release from its
	// User-Agent, the interface version, the base URL the main installation derived for it and the client
	// address it came from (the source of the event holds the same address). No value is a secret.
	Register(EventType{
		Name: EventBastionContact, Category: CategorySystem, Mode: Async,
		Schema: `{"type":"object","properties":{"replica":{"type":"string"},"release":{"type":"string"},` +
			`"interfaceVersion":{"type":"integer"},"responseBaseUrl":{"type":"string"},"clientAddress":{"type":"string"}}}`,
	})
	// A batch of download events told the main installation that events were lost: count in all, of which
	// reported by the Bastion since since (a full buffer, a crash, an ephemeral spool), expired (older than
	// bastions.downloads.max_age on arrival) and invalid (breaking the contract of the event) dropped by the
	// intake. The loss is in the audit trail itself (R-BAS-016).
	Register(EventType{
		Name: EventBastionDownloadsDropped, Category: CategorySystem, Mode: Async,
		Schema: `{"type":"object","properties":{"count":{"type":"integer","minimum":1},"reported":{"type":"integer"},` +
			`"expired":{"type":"integer"},"invalid":{"type":"integer"},"since":{"type":"string"},"replica":{"type":"string"}},"required":["count"]}`,
	})
}
