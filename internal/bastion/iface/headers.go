package iface

// The headers of the interface (R-BAS-008, R-BAS-013, R-BAS-014, R-BAS-017). Names
// are canonical MIME header keys; compare them through net/http's Header, which
// canonicalizes.

// Headers a Bastion sends on every request to the main installation.
const (
	// HeaderInterface carries the interface version, on every request of the
	// Bastion and on every answer of the main installation to a Bastion request
	// (R-BAS-009).
	HeaderInterface = "Trove-Bastion-Interface"
	// HeaderAuthorization carries the Bastion token as "Bearer <token>" on a
	// forwarded request; the caller's own credential stays in Authorization
	// (R-BAS-008). On the interface routes the token is Authorization itself.
	HeaderAuthorization = "Trove-Bastion-Authorization"
	// HeaderReplica carries the pod name of the sending replica (R-BAS-008).
	HeaderReplica = "Trove-Bastion-Replica"
	// HeaderProbe is [ProbeValue] on the bodiless probe of a request that is not a
	// GET or HEAD (R-BAS-014).
	HeaderProbe = "Trove-Bastion-Probe"
)

// Headers the main installation adds to the response of a recognized Bastion
// request (R-BAS-013). They are stripped from every other response.
const (
	// HeaderClass is the [Class] of the response.
	HeaderClass = "Trove-Bastion-Class"
	// HeaderRepository is the addressed repository key.
	HeaderRepository = "Trove-Bastion-Repository"
	// HeaderRead is the caller's [Read] shape on the addressed repository.
	HeaderRead = "Trove-Bastion-Read"
	// HeaderSubject is the caller's subject in the format of [Subject.Header].
	HeaderSubject = "Trove-Bastion-Subject"
	// HeaderRoute is [RouteReadWithBody] on the 204 answer to a probe of a read
	// route that takes a body (R-BAS-014).
	HeaderRoute = "Trove-Bastion-Route"
)

// HeaderPrefix starts every header of the interface. The main installation never
// echoes a request header with this prefix and redacts [HeaderAuthorization] like
// Authorization in every log, span and support bundle (R-BAS-008, R-OPS-013).
const HeaderPrefix = "Trove-Bastion-"

// The values of the headers above.
const (
	// ProbeValue is the value of [HeaderProbe].
	ProbeValue = "1"
	// RouteReadWithBody is the value of [HeaderRoute] on a 204 probe answer: the
	// route is a read route that takes a body, so the Bastion forwards the real
	// request (R-BAS-014).
	RouteReadWithBody = "read-with-body"
	// BearerScheme is the scheme of the Bastion token in [HeaderAuthorization] and
	// in Authorization on the interface routes.
	BearerScheme = "Bearer"
	// UserAgentPrefix starts the User-Agent of a Bastion: "trove-bastion/<release>"
	// (R-BAS-008, R-BAS-010).
	UserAgentPrefix = "trove-bastion/"
	// TokenTypeBST is the token type of the Bastion token (R-BAS-004).
	TokenTypeBST = "bst"
	// TokenPrefix is the prefix of a Bastion token.
	TokenPrefix = "trv_bst_" //nolint:gosec // G101: the prefix of a token type, not a credential
)

// The forwarded-for headers the Bastion sends to the main installation
// (R-BAS-017). The Bastion writes exactly one entry in each family and replaces
// whatever the caller sent.
const (
	HeaderForwardedFor   = "X-Forwarded-For"
	HeaderForwardedProto = "X-Forwarded-Proto"
	HeaderForwardedHost  = "X-Forwarded-Host"
	HeaderForwardedPort  = "X-Forwarded-Port"
	HeaderForwarded      = "Forwarded"
)
