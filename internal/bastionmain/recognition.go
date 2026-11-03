package bastionmain

import "context"

// Recognition is the result of recognizing a request of a Bastion (R-BAS-008, Q-BAS-25): the request carried a
// valid `bst` token of an enabled registration, in Trove-Bastion-Authorization on a forwarded request or in
// Authorization on an interface route. A request with Trove-Bastion-* headers and no valid token is served as
// an ordinary request and carries no Recognition; one with an invalid token is answered 401
// bastion_token_invalid before it gets this far.
//
// The recognition stage (work package 2) stores it with [WithRecognition]. Nothing else may: a Recognition in
// a context is the proof that the token was checked, which the shared authorization step of the interface
// routes and the marking rely on.
type Recognition struct {
	// Bastion is the registration's name and BastionID its principal ID.
	Bastion   string
	BastionID string
	// TokenID is the ID of the Bastion token that authenticated the request.
	TokenID string
	// Interface is the interface version the request announced; recognition requires it to equal
	// iface.InterfaceVersion, so after recognition the two are the same number.
	Interface int
	// Replica is the Trove-Bastion-Replica header, empty when absent.
	Replica string
	// Probe is true on a bodiless probe of a request that is not a GET or HEAD (Trove-Bastion-Probe: 1,
	// R-BAS-014).
	Probe bool
	// Route is true on a route of the interface (below iface.PathPrefix): the token was taken from
	// Authorization and the request is no forwarded read. False on a forwarded request, which the caller's
	// own credential decides (R-BAS-005).
	Route bool
}

type recognitionKey struct{}

// WithRecognition returns ctx carrying rec. Only the recognition stage calls it.
func WithRecognition(ctx context.Context, rec Recognition) context.Context {
	return context.WithValue(ctx, recognitionKey{}, rec)
}

// RecognitionFrom returns the recognition of the request, if it is a Bastion request.
func RecognitionFrom(ctx context.Context) (Recognition, bool) {
	rec, ok := ctx.Value(recognitionKey{}).(Recognition)
	return rec, ok
}
