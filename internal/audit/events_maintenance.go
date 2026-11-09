package audit

// The event types of the maintenance commands of the restore procedure (R-HA-025), written by `trove admin maintenance` and
// `trove admin storage restore-versions` as the actor system:admin, all transactional and none a webhook hook. No detail
// holds a credential.
//
//   - system.deletions_paused: the deletion pause was set by hand (pause-deletions). The pause a post-restore sets is part of
//     system.restored.
//   - system.deletions_resumed: the deletion pause was cleared (resume-deletions); the data are the time and the reason of the
//     pause that ended.
//   - system.storage.versions_restored: restore-versions removed delete markers from the blob bucket; the data are t and the
//     counts. A run that removed nothing, and a dry run, write none.

// Names of the maintenance events, for the producers.
const (
	EventDeletionsPaused         = "system.deletions_paused"
	EventDeletionsResumed        = "system.deletions_resumed"
	EventStorageVersionsRestored = "system.storage.versions_restored"
)

func init() {
	Register(EventType{
		Name: EventDeletionsPaused, Category: CategorySystem, Mode: Transactional,
		Schema: `{"type":"object","properties":{"reason":{"type":"string"}},"required":["reason"],"additionalProperties":false}`,
	})
	Register(EventType{
		Name: EventDeletionsResumed, Category: CategorySystem, Mode: Transactional,
		Schema: `{"type":"object","properties":{"pausedAt":{"type":"string"},"reason":{"type":"string"}},` +
			`"required":["pausedAt","reason"],"additionalProperties":false}`,
	})
	Register(EventType{
		Name: EventStorageVersionsRestored, Category: CategorySystem, Mode: Transactional,
		Schema: `{"type":"object","properties":{"before":{"type":"string","format":"date-time"},"markersRemoved":{"type":"integer"},` +
			`"restored":{"type":"integer"},"remaining":{"type":"integer"}},` +
			`"required":["before","markersRemoved","restored","remaining"],"additionalProperties":false}`,
	})
}
