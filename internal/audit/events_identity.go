package audit

// The identity events of R-AUTH-041: tokens, users, groups, service accounts and
// the runtime authentication settings. The login events (auth.*, cli_login.*)
// are registered by the login area. The journal flag follows the table of
// R-AUTH-041: an event that removes access or updates an access-bearing object.
// A disable or deletion that revokes a principal's tokens records one
// token.revoked per token besides its own event (Q-AUTH-198).
//
// The data schemas describe Event.Details. No schema has a field for a secret: a
// token value, a hash, a session secret or a password never appears in an event.

// Names of the identity events, for the producers.
const (
	EventTokenCreated        = "token.created"
	EventTokenRotated        = "token.rotated"
	EventTokenRevoked        = "token.revoked"
	EventTokenExpiring       = "token.expiring" //nolint:gosec // G101: an event name, not a credential
	EventTokenRejected       = "token.rejected"
	EventUserCreated         = "user.created"
	EventUserUpdated         = "user.updated"
	EventUserRenamed         = "user.renamed"
	EventUserRenameConflict  = "user.rename_conflict"
	EventUserDisabled        = "user.disabled"
	EventUserEnabled         = "user.enabled"
	EventUserDeleted         = "user.deleted"
	EventUserDormancyWarning = "user.dormancy_warning"
	EventUserAvatarSet       = "user.avatar.set"
	EventUserAvatarRemoved   = "user.avatar.removed"
	EventUserGroupsSynced    = "user.groups.synced"
	EventUserGroupsClaimMiss = "user.groups.claim_missing"
	EventGroupCreated        = "group.created"
	EventGroupUpdated        = "group.updated"
	EventGroupDeleted        = "group.deleted"
	EventGroupMembersChanged = "group.members.changed"
	EventSACreated           = "serviceaccount.created"
	EventSAUpdated           = "serviceaccount.updated"
	EventSADisabled          = "serviceaccount.disabled"
	EventSADeleted           = "serviceaccount.deleted"
	EventSettingsAuthChanged = "settings.auth.changed"
)

// JSON Schema fragments shared by several events. Every array declares maxItems (R-OPS-014).
const (
	eventSchemaMemberships    = `{"type":"array","maxItems":1000,"items":{"type":"object","properties":{"group":{"type":"string"},"member":{"type":"string"},"source":{"type":"string"}}}}`
	eventSchemaNames          = `{"type":"array","maxItems":1000,"items":{"type":"string"}}`
	eventSchemaScope          = `{"type":"object"}`
	eventSchemaSettingsValues = `{"type":"object","properties":{"anonymousAccess":{"type":"boolean"},"patDefaultTTLSeconds":{"type":["integer","null"]},"patMaxTTLSeconds":{"type":["integer","null"]},"satDefaultTTLSeconds":{"type":["integer","null"]},"satMaxTTLSeconds":{"type":["integer","null"]},"expiryNoticeSeconds":{"type":["integer","null"]}}}`
)

func init() {
	token := func(name string, mode Mode, journal Journal, schema string) {
		Register(EventType{Name: name, Category: CategoryToken, Mode: mode, Webhook: true, Journal: journal, Schema: schema})
	}
	ident := func(name string, journal Journal, schema string) {
		Register(EventType{Name: name, Category: CategoryIdentity, Mode: Transactional, Webhook: true, Journal: journal, Schema: schema})
	}

	token(EventTokenCreated, Transactional, NoJournal, `{"type":"object","properties":{`+
		`"type":{"type":"string","enum":["pat","sat","cit","bst"]},"owner":{"type":"string"},"scope":`+eventSchemaScope+`,`+
		`"expiresAt":{"type":["string","null"]},"createdVia":{"type":"string"},"description":{"type":"string"},"derivedFrom":{"type":"string"},`+
		`"project":{"type":"string"},"bastion":{"type":"string"},"tokenId":{"type":"string"},"ciIssuer":{"type":"string"},"ciMappings":`+eventSchemaNames+`,"ciClaims":{"type":"object"}}}`)
	token(EventTokenRotated, Transactional, JournalRevocation, `{"type":"object","properties":{`+
		`"predecessor":{"type":"string"},"overlapSeconds":{"type":"integer"},"predecessorExpiresAt":{"type":["string","null"]}}}`)
	token(EventTokenRevoked, Transactional, JournalRevocation, `{"type":"object","properties":{`+
		`"reason":{"type":"string"},"cascaded":`+eventSchemaNames+`,"owner":{"type":"string"},"project":{"type":"string"},`+
		`"bastion":{"type":"string"},"tokenId":{"type":"string"}}}`)
	token(EventTokenExpiring, Transactional, NoJournal, `{"type":"object","properties":{`+
		`"owner":{"type":"string"},"hint":{"type":"string"},"description":{"type":"string"},"expiresAt":{"type":"string"}}}`)
	token(EventTokenRejected, Async, NoJournal, `{"type":"object","properties":{`+
		`"reason":{"type":"string","enum":["revoked","expired","endpoint_not_accepted"]},"hint":{"type":"string"},"owner":{"type":"string"},`+
		`"refusal":{"type":"string","enum":["token_type","token_binding"]},"tokenType":{"type":"string"}}}`)

	userSource := `"source":{"type":"string","enum":["jit","admin","scim","dormancy","admin-cli"]}`
	ident(EventUserCreated, NoJournal, `{"type":"object","properties":{`+userSource+`,"provider":{"type":"string"}}}`)
	ident(EventUserUpdated, NoJournal, `{"type":"object","properties":{`+userSource+`,"fields":`+eventSchemaNames+`}}`)
	ident(EventUserRenamed, NoJournal, `{"type":"object","properties":{`+userSource+`,"from":{"type":"string"},"to":{"type":"string"}}}`)
	ident(EventUserRenameConflict, NoJournal, `{"type":"object","properties":{`+userSource+`,"wanted":{"type":"string"}}}`)
	ident(EventUserDisabled, JournalRevocation, `{"type":"object","properties":{`+userSource+`,`+
		`"reason":{"type":"string"},"note":{"type":"string","maxLength":1024},"tokensRevoked":{"type":"integer"},"sessionsEnded":{"type":"integer"}}}`)
	ident(EventUserEnabled, NoJournal, `{"type":"object","properties":{`+userSource+`}}`)
	ident(EventUserDeleted, JournalRevocation, `{"type":"object","properties":{`+userSource+`,`+
		`"tokensDeleted":{"type":"integer"},"sessionsEnded":{"type":"integer"},"membershipsDeleted":{"type":"integer"},"identitiesDeleted":{"type":"integer"}}}`)
	// The avatar of a user (R-API-055): the image is the stored PNG, named by its digest; no event holds image bytes.
	avatarSource := `"source":{"type":"string","enum":["self","admin"]}`
	ident(EventUserAvatarSet, NoJournal, `{"type":"object","properties":{`+avatarSource+`,"sha256":{"type":"string"},"size":{"type":"integer"}}}`)
	ident(EventUserAvatarRemoved, NoJournal, `{"type":"object","properties":{`+avatarSource+`,"sha256":{"type":"string"}}}`)
	ident(EventUserDormancyWarning, NoJournal, `{"type":"object","properties":{"deadline":{"type":"string"}}}`)
	ident(EventUserGroupsSynced, JournalRevocation, `{"type":"object","properties":{`+
		`"provider":{"type":"string"},"added":`+eventSchemaNames+`,"removed":`+eventSchemaNames+`}}`)
	Register(EventType{
		Name: EventUserGroupsClaimMiss, Category: CategoryIdentity, Mode: Transactional, Webhook: true,
		Schema: `{"type":"object","properties":{"provider":{"type":"string"},"reason":{"type":"string","enum":["absent","wrong_type","overage"]}}}`,
	})

	ident(EventGroupCreated, NoJournal, `{"type":"object","properties":{"origin":{"type":"string"},"autoJoin":{"type":"boolean"},"members":`+eventSchemaNames+`}}`)
	// The entry carries the auto-join flag before and after on every update, not
	// only when the update clears it: clearing it ends the group's grants for the
	// users created afterwards (R-AUTH-019), an unchanged value is a no-op for the
	// replay, and a producer that always supplies the entry cannot drop the
	// narrowing by a bug (Q-AUTH-198, as Q-AUTH-18 for the anonymous switch).
	ident(EventGroupUpdated, JournalRevocation, `{"type":"object","properties":{"fields":`+eventSchemaNames+`}}`)
	ident(EventGroupDeleted, JournalRevocation, `{"type":"object","properties":{"membershipsDeleted":{"type":"integer"}}}`)
	ident(EventGroupMembersChanged, JournalRevocation, `{"type":"object","properties":{`+
		`"added":`+eventSchemaMemberships+`,"removed":`+eventSchemaMemberships+`}}`)

	// project is the key of the project of a project service account (Q-AUTHZ-169),
	// absent for a global one.
	ident(EventSACreated, NoJournal, `{"type":"object","properties":{"source":{"type":"string"},"admin":{"type":"boolean"},"project":{"type":"string"}}}`)
	ident(EventSAUpdated, NoJournal, `{"type":"object","properties":{"fields":`+eventSchemaNames+`,"project":{"type":"string"}}}`)
	ident(EventSADisabled, JournalRevocation, `{"type":"object","properties":{"tokensRevoked":{"type":"integer"},"project":{"type":"string"}}}`)
	ident(EventSADeleted, JournalRevocation, `{"type":"object","properties":{"tokensDeleted":{"type":"integer"},"membershipsDeleted":{"type":"integer"},"project":{"type":"string"}}}`)

	// The entry carries anonymousAccess before and after on every change, not
	// only when the change sets it: an unchanged value is a no-op for the replay
	// comparator (R-AUTH-041), and a producer that always supplies the entry
	// cannot drop a narrowing by a bug (Q-AUTH-18).
	Register(EventType{
		Name: EventSettingsAuthChanged, Category: CategoryConfig, Mode: Transactional, Webhook: true,
		Journal: JournalRevocation, Schema: `{"type":"object","properties":{"before":` + eventSchemaSettingsValues + `,"after":` + eventSchemaSettingsValues + `}}`,
	})
}
