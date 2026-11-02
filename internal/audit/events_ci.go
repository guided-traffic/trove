package audit

// The event types of the GitLab CI OIDC federation (R-CI-020, R-CI-015, R-CI-003, R-CI-018). The
// categories follow the object: the exchange and the signing keys are `auth`, the revocation of a CI
// token is `token`, issuers and mappings are `identity`, and the ownership events are `authz` like
// the other ownership events (Q-CI-21).
//
// Written in the transaction of the change (Transactional): the issuer and mapping events, the
// revocation, the exchange's success and the key fetch result. Written through the buffer (Async):
// every denial, the replay events (their transaction is rolled back, so the event cannot ride in
// it) and the package-ownership conflict, which is a refusal.
//
// Flagged `journal: revocation` with a required entry (R-CI-020, R-OPS-014): ci.token.revoked,
// ci.issuer.disabled, ci.issuer.deleted, ci.mapping.disabled, ci.mapping.deleted, ci.issuer.updated
// and ci.mapping.updated. Their producers (W1, W3, W5) must supply the entry; the replay of journal
// entries is not built anywhere yet (beta phase 7), the entries are. Not journaled: .created and
// .enabled (they grant or only record, and replay never grants), ci.exchange.*, ci.keys.*,
// ci.package_ownership.* and ci.job_token.lookup.
const (
	EventCIExchangeSucceeded = "ci.exchange.succeeded"
	EventCIExchangeDenied    = "ci.exchange.denied"
	EventCIExchangeReplayed  = "ci.exchange.replayed"
	EventCIExchangeRepeated  = "ci.exchange.repeated"
	EventCITokenRevoked      = "ci.token.revoked"
	EventCIIssuerCreated     = "ci.issuer.created"
	EventCIIssuerUpdated     = "ci.issuer.updated"
	EventCIIssuerDisabled    = "ci.issuer.disabled"
	EventCIIssuerEnabled     = "ci.issuer.enabled"
	EventCIIssuerDeleted     = "ci.issuer.deleted"
	EventCIMappingCreated    = "ci.mapping.created"
	EventCIMappingUpdated    = "ci.mapping.updated"
	EventCIMappingDisabled   = "ci.mapping.disabled"
	EventCIMappingEnabled    = "ci.mapping.enabled"
	EventCIMappingDeleted    = "ci.mapping.deleted"
	EventCIKeysRefreshed     = "ci.keys.refreshed"
	EventCIKeysRefreshFailed = "ci.keys.refresh_failed"
	EventCIOwnershipClaimed  = "ci.package_ownership.claimed"
	EventCIOwnershipConflict = "ci.package_ownership.conflict"
	EventCIJobTokenLookup    = "ci.job_token.lookup" //nolint:gosec // G101: an event name, not a credential
)

func init() {
	// The exchange. Before the signature is verified only the unverified iss and kid are recorded and
	// no other claim (R-CI-020); after it the allowlisted claim snapshot. count is the aggregated
	// number of a denial summary (R-CI-021).
	exchange := `{"type":"object","properties":{"issuer":{"type":"string"},"reason":{"type":"string"},` +
		`"iss":{"type":"string"},"kid":{"type":"string"},"tokenId":{"type":"string"},"mappings":{"type":"array","maxItems":1000,"items":{"type":"string"}},` +
		`"claims":{"type":"object"},"requestedScope":{"type":"string"},"grantedScope":{"type":"string"},"ttlSeconds":{"type":"integer"},` +
		`"crossProject":{"type":"boolean"},"jti":{"type":"string"},"exchangeCount":{"type":"integer"},"count":{"type":"integer"},"repeatOf":{"type":"string"}}}`
	Register(EventType{Name: EventCIExchangeSucceeded, Category: CategoryAuth, Mode: Transactional, Schema: exchange})
	Register(EventType{Name: EventCIExchangeDenied, Category: CategoryAuth, Mode: Async, Schema: exchange})
	Register(EventType{Name: EventCIExchangeReplayed, Category: CategoryAuth, Mode: Async, Schema: exchange})
	Register(EventType{Name: EventCIExchangeRepeated, Category: CategoryAuth, Mode: Async, Schema: exchange})

	// The revocation of CI tokens: at most 1,000 token IDs per event (R-OPS-014); a larger revocation
	// writes several events. cause is mapping, issuer, bulk, single or self (R-CI-017).
	revoked := `{"type":"object","properties":{"issuer":{"type":"string"},"mapping":{"type":"string"},"cause":{"type":"string"},` +
		`"tokenIds":{"type":"array","maxItems":1000,"items":{"type":"string"}},"filter":{"type":"object"}}}`
	Register(EventType{Name: EventCITokenRevoked, Category: CategoryToken, Mode: Transactional, Journal: JournalRevocation, Schema: revoked})

	// Issuers and mappings: the full record before and after the change; no secret is in it
	// (ca_bundle_ref is a reference, R-CI-020). version is the record's version after the change.
	record := `{"type":"object","properties":{"name":{"type":"string"},"id":{"type":"string"},"issuer":{"type":"string"},"tier":{"type":"string"},` +
		`"priority":{"type":"integer"},"project":{"type":"string"},"version":{"type":"integer"},"source":{"type":"string"},"cascade":{"type":"boolean"},` +
		`"before":{"type":["object","null"]},"after":{"type":["object","null"]}}}`
	Register(EventType{Name: EventCIIssuerCreated, Category: CategoryIdentity, Mode: Transactional, Schema: record})
	Register(EventType{Name: EventCIIssuerEnabled, Category: CategoryIdentity, Mode: Transactional, Schema: record})
	for _, n := range []string{EventCIIssuerUpdated, EventCIIssuerDisabled, EventCIIssuerDeleted} {
		Register(EventType{Name: n, Category: CategoryIdentity, Mode: Transactional, Journal: JournalRevocation, Schema: record})
	}
	Register(EventType{Name: EventCIMappingCreated, Category: CategoryIdentity, Mode: Transactional, Schema: record})
	Register(EventType{Name: EventCIMappingEnabled, Category: CategoryIdentity, Mode: Transactional, Schema: record})
	for _, n := range []string{EventCIMappingUpdated, EventCIMappingDisabled, EventCIMappingDeleted} {
		Register(EventType{Name: n, Category: CategoryIdentity, Mode: Transactional, Journal: JournalRevocation, Schema: record})
	}

	// The key fetch: the key IDs on success, the failure class, the HTTP status and the host of the URL
	// on failure, never bytes of a response body (R-CI-003).
	keys := `{"type":"object","properties":{"issuer":{"type":"string"},"trigger":{"type":"string"},"kids":{"type":"array","maxItems":1000,"items":{"type":"string"}},` +
		`"changed":{"type":"boolean"},"errorClass":{"type":"string"},"httpStatus":{"type":"integer"},"host":{"type":"string"}}}`
	Register(EventType{Name: EventCIKeysRefreshed, Category: CategoryAuth, Mode: Transactional, Schema: keys})
	Register(EventType{Name: EventCIKeysRefreshFailed, Category: CategoryAuth, Mode: Transactional, Schema: keys})

	// Package ownership by CI project (R-CI-015): written with the publish.
	ownership := `{"type":"object","properties":{"repository":{"type":"string"},"package":{"type":"string"},"owner":{"type":"string"},` +
		`"tokenId":{"type":"string"},"epoch":{"type":"integer"}}}`
	Register(EventType{Name: EventCIOwnershipClaimed, Category: CategoryAuthz, Mode: Transactional, Schema: ownership})
	Register(EventType{Name: EventCIOwnershipConflict, Category: CategoryAuthz, Mode: Async, Schema: ownership})

	// The job-token fallback (R-CI-022, P2): registered so the name is reserved; nothing writes it.
	lookup := `{"type":"object","properties":{"issuer":{"type":"string"},"outcome":{"type":"string"},"gitlabStatus":{"type":"integer"},"latencyMs":{"type":"integer"}}}`
	Register(EventType{Name: EventCIJobTokenLookup, Category: CategoryAuth, Mode: Async, Schema: lookup})
}
