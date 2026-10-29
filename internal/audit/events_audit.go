package audit

// The event types of the audit area (R-OPS-016). Names are listed in
// docs/developer/m1-contracts.md.

// AuditExported is the name of the event every export writes.
const AuditExported = "audit.exported"

func init() {
	// An export changes no state, but reading the audit trail is itself audited
	// and must not run unrecorded: the handler records the event in a transaction
	// of its own before it sends the first batch (R-OPS-017).
	Register(EventType{
		Name: AuditExported, Category: CategorySystem, Mode: Transactional,
		Schema: `{
  "type": "object",
  "properties": {
    "from": {"type": "string", "format": "date-time"},
    "to": {"type": "string", "format": "date-time"},
    "filters": {"type": "object", "additionalProperties": {"type": "string"}},
    "format": {"type": "string", "enum": ["jsonl", "csv"]}
  },
  "required": ["from", "to", "format"]
}`,
	})
}
