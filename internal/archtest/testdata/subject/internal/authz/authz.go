package authz

// Subject carries the endpoint of the request.
type Subject struct {
	Name     string
	Endpoint string
}

// The decision point builds and edits subjects.
func inside() { s := Subject{Name: "x"}; s.Endpoint = "customers"; _ = new(Subject) }

type Decider struct{}

func (Decider) Decide(Subject) bool { return false }
