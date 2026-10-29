package audit

// The artifact events of the files transfer (R-OPS-016, R-STOR-005, R-STOR-018).
// artifact.deployed and artifact.deleted are transactional: the producer records
// them in the transaction of the change, one per file. Neither carries the
// revocation flag (R-OPS-014 lists none for them). artifact.downloaded is
// asynchronous and exists only with audit.record_downloads (R-OPS-015); the
// request log holds every download regardless.
//
// The repository the event concerns is Event.Repository and its path is
// Event.Path (the columns of the same names); the data below repeats the path,
// because a webhook delivery carries the data and not the columns.

// Names of the artifact events, for the producers.
const (
	EventArtifactDeployed   = "artifact.deployed"
	EventArtifactDeleted    = "artifact.deleted"
	EventArtifactDownloaded = "artifact.downloaded"
)

func init() {
	Register(EventType{
		Name: EventArtifactDeployed, Category: CategoryContent, Mode: Transactional, Webhook: true,
		Schema: `{"type":"object","properties":{` +
			`"path":{"type":"string"},"sha256":{"type":"string","pattern":"^[0-9a-f]{64}$"},` +
			`"size":{"type":"integer","minimum":0},"overwritten":{"type":"boolean"}},` +
			`"required":["path","sha256","size","overwritten"],"additionalProperties":false}`,
	})
	Register(EventType{
		Name: EventArtifactDeleted, Category: CategoryContent, Mode: Transactional, Webhook: true,
		Schema: `{"type":"object","properties":{` +
			`"path":{"type":"string"},"sha256":{"type":"string","pattern":"^[0-9a-f]{64}$"},` +
			`"size":{"type":"integer","minimum":0}},` +
			`"required":["path","sha256","size"],"additionalProperties":false}`,
	})
	Register(EventType{
		Name: EventArtifactDownloaded, Category: CategoryContent, Mode: Async,
		Schema: `{"type":"object","properties":{` +
			`"path":{"type":"string"},"sha256":{"type":"string","pattern":"^[0-9a-f]{64}$"}},` +
			`"required":["path","sha256"],"additionalProperties":false}`,
	})
}
