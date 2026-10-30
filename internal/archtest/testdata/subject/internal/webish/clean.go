package webish

import (
	"example.test/fix/internal/api/edge"
	"example.test/fix/internal/audit"
	"example.test/fix/internal/authz"
)

// Passing a subject on and reading it is what every package does.
type caller struct {
	Subject authz.Subject
}

func name(s authz.Subject) string { return s.Name + s.Endpoint }

func decide(d authz.Decider, c caller) bool { return d.Decide(c.Subject) }

func taken() caller {
	sub := edge.SubjectFrom("customers")
	var held = sub
	return caller{Subject: held}
}

// An audit subject is a different type that happens to share the name.
func event() audit.Subject { return audit.Subject{Kind: "repository"} }
