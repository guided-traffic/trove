package audit

// The event types of the management API conventions and the system operations
// (domain system., R-API-019, R-OPS-014). Transactional: the change and its
// event commit together.
func init() {
	Register(EventType{
		Name: "system.task_triggered", Category: CategorySystem, Mode: Transactional,
		Schema: `{"type":"object","required":["task","jobId"],"properties":{` +
			`"task":{"type":"string"},"jobId":{"type":"string","format":"uuid"},` +
			`"args":{"type":"object","additionalProperties":true}}}`,
	})
	Register(EventType{
		Name: "system.job.cancelled", Category: CategorySystem, Mode: Transactional,
		Schema: `{"type":"object","required":["jobId","kind"],"properties":{` +
			`"jobId":{"type":"string","format":"uuid"},"kind":{"type":"string"}}}`,
	})
}
