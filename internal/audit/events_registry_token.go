package audit

// The event of the OCI registry's token service (R-OCI-034, R-AUTH-041). The token service writes no event per issued
// token, only the metric; a token request whose credential the authentication stage refused is recorded, deduplicated per
// client address, endpoint, reason and form of the credential within audit.token_rejection_dedup_window (R-OPS-015,
// Q-OCI-53), so a client that loops on a wrong password writes one event and one summary per window and replica, whatever
// its request rate, and a summary never folds one reason under another. A rejected credential also counts in the
// failed-credential throttle of R-AUTH-038; a request without a credential (credentials_required) does not, as everywhere.

// EventRegistryTokenDenied names the event, for the token service.
const EventRegistryTokenDenied = "auth.registry_token.denied" //nolint:gosec // G101: an event name, not a credential

func init() {
	// reason is the code of the refusal: invalid_token (malformed, unknown, revoked or expired; the event does not say
	// which, token.rejected does for a revoked or expired one), credentials_required (no credential where the endpoint or
	// the runtime switch admits no anonymous request) or principal_disabled (the only trail of a disabled owner's token:
	// token.rejected is not written for it); credential is how it was presented: basic, bearer, form (the password of the
	// OAuth 2.0 request), none, or unreadable (a header in another scheme). The endpoint is the event's column. No member
	// holds a secret or a hash.
	Register(EventType{
		Name: EventRegistryTokenDenied, Category: CategoryAuth, Mode: Async,
		Schema: `{"type":"object","properties":{` +
			`"reason":{"enum":["invalid_token","credentials_required","principal_disabled"]},` +
			`"credential":{"enum":["basic","bearer","form","none","unreadable"]},` +
			`"count":{"type":"integer"},"repeat_of":{"type":"string"}},` +
			`"required":["reason","credential"],"additionalProperties":false}`,
	})
}
