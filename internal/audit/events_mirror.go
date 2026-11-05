package audit

// The pass events of the mirror mode (R-REPO-057, R-OPS-016, Q-OPS-159), written by the mirror engine. All four are
// asynchronous: the system is the actor, the process that runs the pass writes them, and none is a webhook hook in the MVP.
// mirror.pass_requested, the fifth type, is transactional and belongs to the run operation of the management API; it is
// registered with that operation, not here.
//
//   - mirror.pass_completed: a pass attempt ended complete, partial, bound_reached, offline or stopped.
//   - mirror.pass_failed: a pass attempt failed itself; the next one follows after the back-off (R-REPO-053).
//   - mirror.bound_reached: the byte bound was reached; once per transition (R-REPO-052).
//   - mirror.rate_limited: the upstream rate-limited a pass; at most once per pass and hour (R-REPO-054).
//
// The category is repository: the events concern one repository, whose key is the event's repository column. No detail holds
// a URL, a credential or a package name; a host is the host name of the upstream alone.

// Names of the mirror events, for the producers.
const (
	EventMirrorPassCompleted = "mirror.pass_completed"
	EventMirrorPassFailed    = "mirror.pass_failed"
	EventMirrorBoundReached  = "mirror.bound_reached"
	EventMirrorRateLimited   = "mirror.rate_limited"
)

func init() {
	// kind is full or incremental, method feed, listing or names (empty when the pass ended before it chose one), outcome one
	// of the five a pass ends with, job the id of the job; the counts are the pass's.
	Register(EventType{
		Name: EventMirrorPassCompleted, Category: CategoryRepository, Mode: Async,
		Schema: `{"type":"object","properties":{` +
			`"kind":{"type":"string","enum":["full","incremental"]},"method":{"type":"string","enum":["","feed","listing","names"]},` +
			`"outcome":{"type":"string","enum":["complete","partial","bound_reached","offline","stopped"]},` +
			`"packages":{"type":"integer"},"packagesFailed":{"type":"integer"},"itemsStored":{"type":"integer"},"bytesStored":{"type":"integer"},` +
			`"discoveryComplete":{"type":"boolean"},"job":{"type":"string"}},` +
			`"required":["kind","outcome","packages","packagesFailed","itemsStored","bytesStored","discoveryComplete","job"],"additionalProperties":false}`,
	})
	// code is the problem code of the failure, failures the count in a row, nextPassAt the time the next attempt is due.
	Register(EventType{
		Name: EventMirrorPassFailed, Category: CategoryRepository, Mode: Async,
		Schema: `{"type":"object","properties":{` +
			`"code":{"type":"string"},"failures":{"type":"integer"},"nextPassAt":{"type":"string"},"job":{"type":"string"}},` +
			`"required":["code","failures","nextPassAt","job"],"additionalProperties":false}`,
	})
	// bound is the bound that was reached (bytes; quota and capacity belong to the quotas, H-506), usedBytes the repository's
	// logical bytes as the pass counted them, limitBytes the bound's value.
	Register(EventType{
		Name: EventMirrorBoundReached, Category: CategoryRepository, Mode: Async,
		Schema: `{"type":"object","properties":{` +
			`"bound":{"type":"string","enum":["bytes","quota","capacity"]},"usedBytes":{"type":"integer"},"limitBytes":{"type":"integer"}},` +
			`"required":["bound","usedBytes","limitBytes"],"additionalProperties":false}`,
	})
	// status is the HTTP status of the answer, reason one of too_many_requests, slow_down, retry_after and quota_header,
	// waitSeconds the wait the pass began, hinted whether the upstream named it, host the upstream's host name.
	Register(EventType{
		Name: EventMirrorRateLimited, Category: CategoryRepository, Mode: Async,
		Schema: `{"type":"object","properties":{` +
			`"status":{"type":"integer"},"reason":{"type":"string","enum":["too_many_requests","slow_down","retry_after","quota_header"]},` +
			`"waitSeconds":{"type":"integer"},"hinted":{"type":"boolean"},"host":{"type":"string"}},` +
			`"required":["status","reason","waitSeconds","hinted"],"additionalProperties":false}`,
	})
}
