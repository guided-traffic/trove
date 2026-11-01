package formats

// Adapter is the contract a format implements.
type Adapter interface{ Name() string }

// Gate is an optional interface of an Adapter: the shape the rule forbids to find by assertion.
type Gate interface{ Allow() bool }

// Plain is a struct of the package; asserting to it is no business of the rule.
type Plain struct{}

// Register may read a capability off a registering format.
func Register(a Adapter) bool {
	_, ok := a.(Gate)
	return ok
}

// Other is not Register and may not assert either.
func Other(a Adapter) bool {
	_, ok := a.(Gate)
	return ok
}
