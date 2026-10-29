package audit

// The event types of the login area (WP1.1): browser and break-glass sign-in,
// sessions, CSRF and the break-glass lockout (R-AUTH-041). The spelling is the one
// R-AUTH-041 defines (Q-OPS-37), including the two names whose last segment is a
// noun (auth.breakglass.login, auth.logout). None of them carries the journal
// flag: sessions and login flows are transient, a restore ends them all
// (R-AUTH-015), and the lockout is not an access grant.
//
// Every schema describes details only; actor, outcome, reason, source and
// subject are fields of the Event itself. No schema has a field for a secret:
// no session secret, CSRF token, password, ID token or authorization code
// (R-AUTH-041).

// Names of the login events.
const (
	EventAuthLoginSucceeded     = "auth.login.succeeded"
	EventAuthLoginFailed        = "auth.login.failed"
	EventAuthBreakGlassLogin    = "auth.breakglass.login"
	EventAuthBreakGlassLocked   = "auth.breakglass.locked"
	EventAuthBreakGlassUnlocked = "auth.breakglass.unlocked"
	EventAuthLogout             = "auth.logout"
	EventAuthSessionEnded       = "auth.session.ended"
	EventAuthCSRFFailed         = "auth.csrf_failed"
	EventAuthCredentialRejected = "auth.credential.rejected" //nolint:gosec // G101: an event name, not a credential
)

func init() {
	// A sign-in creates a session row whose transaction the event does not join:
	// the event records access, so it is asynchronous (R-OPS-016).
	Register(EventType{
		Name: EventAuthLoginSucceeded, Category: CategoryAuth, Mode: Async,
		Schema: `{"type":"object","properties":{` +
			`"method":{"enum":["oidc","breakglass"]},"provider":{"type":"string"},"issuer":{"type":"string"},` +
			`"session_id":{"type":"string"},"purpose":{"enum":["login","reauth"]}},"additionalProperties":false}`,
	})
	Register(EventType{
		Name: EventAuthLoginFailed, Category: CategoryAuth, Mode: Async,
		Schema: `{"type":"object","properties":{` +
			`"method":{"enum":["oidc"]},"provider":{"type":"string"},"issuer":{"type":"string"},` +
			`"idp_error":{"type":"string","maxLength":200},"purpose":{"enum":["login","reauth"]}},"additionalProperties":false}`,
	})
	// Every break-glass attempt, success or failure, with severity high (R-AUTH-014).
	Register(EventType{
		Name: EventAuthBreakGlassLogin, Category: CategoryAuth, Mode: Async,
		Schema: `{"type":"object","properties":{` +
			`"severity":{"const":"high"},"session_id":{"type":"string"},"username":{"type":"string","maxLength":128}},` +
			`"required":["severity"],"additionalProperties":false}`,
	})
	// Written in the transaction whose failure count locked the account (R-AUTH-038).
	Register(EventType{
		Name: EventAuthBreakGlassLocked, Category: CategoryAuth, Mode: Transactional,
		Schema: `{"type":"object","properties":{` +
			`"severity":{"const":"high"},"failures":{"type":"integer"},"locked_until":{"type":"string","format":"date-time"}},` +
			`"required":["severity"],"additionalProperties":false}`,
	})
	// trove admin break-glass unlock and, from wave 2, POST /api/v1/users/{breakGlassName}/unlock.
	Register(EventType{
		Name: EventAuthBreakGlassUnlocked, Category: CategoryAuth, Mode: Transactional,
		Schema: `{"type":"object","properties":{` +
			`"severity":{"const":"high"},"was_locked":{"type":"boolean"}},"required":["severity"],"additionalProperties":false}`,
	})
	Register(EventType{
		Name: EventAuthLogout, Category: CategoryAuth, Mode: Async,
		Schema: `{"type":"object","properties":{` +
			`"session_id":{"type":"string"},"rp_initiated":{"type":"boolean"}},"additionalProperties":false}`,
	})
	// A session ended by someone or something other than its user: an
	// administrator or trove admin (revoked), the session limit (evicted), a
	// disabled break-glass account, a password rotation. Idle and absolute expiry
	// and the rotation of a re-authentication are not recorded (R-AUTH-041).
	Register(EventType{
		Name: EventAuthSessionEnded, Category: CategoryAuth, Mode: Transactional,
		Schema: `{"type":"object","properties":{` +
			`"reason":{"enum":["revoked","evicted","principal_disabled","password_changed"]},` +
			`"session_ids":{"type":"array","items":{"type":"string"},"maxItems":1000},"count":{"type":"integer"}},` +
			`"required":["reason"],"additionalProperties":false}`,
	})
	// At most one per session and minute on each replica (R-AUTH-041).
	Register(EventType{
		Name: EventAuthCSRFFailed, Category: CategoryAuth, Mode: Async,
		Schema: `{"type":"object","properties":{` +
			`"session_id":{"type":"string"},"method":{"type":"string"},"path":{"type":"string","maxLength":512},` +
			`"check":{"enum":["token","origin"]}},"additionalProperties":false}`,
	})
	// Malformed or unknown credentials: the log stream and the metric only,
	// because of their volume (R-AUTH-041). The authentication stage emits it.
	Register(EventType{
		Name: EventAuthCredentialRejected, Category: CategoryAuth, Mode: None,
		Schema: `{"type":"object","properties":{` +
			`"credential":{"enum":["bearer","basic","api_key","session"]},"token_type":{"type":"string"}},"additionalProperties":false}`,
	})
}
