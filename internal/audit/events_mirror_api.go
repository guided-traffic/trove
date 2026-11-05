package audit

// The event of the mirror's run operation (R-REPO-056, R-REPO-057, Q-REPO-145). The other mirror events, which the passes
// write, are registered with the engine (events_mirror.go).

// EventMirrorPassRequested is a caller's request of a pass of a remote repository's mirror: transactional, in the
// transaction that records the request and enqueues the job, so that no pass is requested without its record
// (R-OPS-015). The actor is the caller, the repository the event's repository. full says whether the request asked for a
// full pass, job is the ID of the enqueued job, which `GET /api/v1/system/jobs/<job>` reads.
const EventMirrorPassRequested = "mirror.pass_requested" //nolint:gosec // G101: an event name, not a credential

func init() {
	Register(EventType{
		Name: EventMirrorPassRequested, Category: CategoryRepository, Mode: Transactional,
		Schema: `{"type":"object","properties":{"full":{"type":"boolean"},"job":{"type":"string"}},` +
			`"required":["full","job"],"additionalProperties":false}`,
	})
}
