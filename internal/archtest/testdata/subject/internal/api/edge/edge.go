package edge

import "example.test/fix/internal/authz"

// SubjectFrom is the one conversion.
func SubjectFrom(endpoint string) authz.Subject {
	s := authz.Subject{Name: "x"}
	s.Endpoint = endpoint
	return s
}
