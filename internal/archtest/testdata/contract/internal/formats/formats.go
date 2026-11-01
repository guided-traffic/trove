package formats

// Adapter is the contract a format implements.
type Adapter interface{ Name() string }

// Gate is an optional interface of an Adapter: the shape the rule forbids to find by assertion.
type Gate interface{ Allow() bool }

// Plain is a struct of the package; asserting to it is no business of the rule.
type Plain struct{}

// CapabilitiesOf may read an optional capability off a format.
func CapabilitiesOf(a Adapter) bool {
	_, ok := a.(Gate)
	return ok
}

// Other is not CapabilitiesOf and may not assert either.
func Other(a Adapter) bool {
	_, ok := a.(Gate)
	return ok
}
