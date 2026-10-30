package audit

// Subject is the subject of an audit event, not a decision subject: anyone builds it.
type Subject struct{ Kind, Name string }

func build() Subject { return Subject{Kind: "repository"} }
