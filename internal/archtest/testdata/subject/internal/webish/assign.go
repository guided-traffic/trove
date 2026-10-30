package webish

import (
	"example.test/fix/internal/api/edge"
	"example.test/fix/internal/authz"
)

// Editing a field of a subject it was handed strips the ceiling.
func viaFieldAssignment(sub authz.Subject, p *authz.Subject) {
	sub.Endpoint = ""
	p.Endpoint = "default"
	other := edge.SubjectFrom("customers")
	other.Name = "mallory"
}
