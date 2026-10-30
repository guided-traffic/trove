package audit

// The events of the remote engine (R-REPO-047, R-REPO-041, R-REPO-029, R-OPS-016).
//
// proxy.upstream_gone is transactional: the transaction that marks the document
// writes it, so a mark and its event commit together. The other four are security
// events about a request that stored nothing, so there is no transaction to join:
// they go through the asynchronous buffer, and the log copy keeps what a full buffer
// drops (R-OPS-015). tls_untrusted and egress_denied are written at most once per
// repository and host and minute by the producer, because a broken upstream fails
// every request.
//
// artifact.cached is the webhook event of a remote cache fill (R-OPS-035). Its audit
// mode is none: it is delivered to webhook subscriptions and never stored as an audit
// row (R-OPS-014 lists it as "webhook only, no audit"). The fill transaction hands it
// to the recorder like any event, which records nothing for this mode, and the
// outbox of R-OPS-035 takes it from there when that is built.

// Names of the proxy events, for the producers.
const (
	EventProxyUpstreamGone          = "proxy.upstream_gone"
	EventProxyUpstreamDigestChanged = "proxy.upstream_digest_changed"
	EventProxyIntegrityMismatch     = "proxy.integrity_mismatch"
	EventProxyTLSUntrusted          = "proxy.tls_untrusted"
	EventProxyEgressDenied          = "proxy.egress_denied"
	EventArtifactCached             = "artifact.cached"
)

func init() {
	Register(EventType{
		Name: EventProxyUpstreamGone, Category: CategoryRepository, Mode: Transactional,
		Schema: `{"type":"object","properties":{` +
			`"path":{"type":"string"},"variant":{"type":"string"}},` +
			`"required":["path"],"additionalProperties":false}`,
	})
	Register(EventType{
		Name: EventProxyUpstreamDigestChanged, Category: CategoryRepository, Mode: Async,
		Schema: `{"type":"object","properties":{` +
			`"path":{"type":"string"},` +
			`"cachedSha256":{"type":"string","pattern":"^[0-9a-f]{64}$"},` +
			`"announced":{"type":"object","properties":{` +
			`"sha256":{"type":"string"},"sha512":{"type":"string"},"sha1":{"type":"string"}},"additionalProperties":false},` +
			`"fetchedSha256":{"type":"string","pattern":"^[0-9a-f]{64}$"}},` +
			`"required":["path","cachedSha256"],"additionalProperties":false}`,
	})
	Register(EventType{
		Name: EventProxyIntegrityMismatch, Category: CategoryRepository, Mode: Async,
		Schema: `{"type":"object","properties":{` +
			`"path":{"type":"string"},` +
			`"announced":{"type":"object","properties":{` +
			`"sha256":{"type":"string"},"sha512":{"type":"string"},"sha1":{"type":"string"}},"additionalProperties":false},` +
			`"computedSha256":{"type":"string","pattern":"^[0-9a-f]{64}$"}},` +
			`"required":["path","computedSha256"],"additionalProperties":false}`,
	})
	Register(EventType{
		Name: EventProxyTLSUntrusted, Category: CategoryRepository, Mode: Async,
		Schema: `{"type":"object","properties":{"host":{"type":"string"}},"required":["host"],"additionalProperties":false}`,
	})
	Register(EventType{
		Name: EventProxyEgressDenied, Category: CategoryRepository, Mode: Async,
		Schema: `{"type":"object","properties":{` +
			`"host":{"type":"string"},"address":{"type":"string"},"rule":{"type":"string"}},` +
			`"required":["host"],"additionalProperties":false}`,
	})
	Register(EventType{
		Name: EventArtifactCached, Category: CategoryContent, Mode: None, Webhook: true,
		Schema: `{"type":"object","properties":{` +
			`"path":{"type":"string"},"name":{"type":"string"},` +
			`"sha256":{"type":"string","pattern":"^[0-9a-f]{64}$"},"size":{"type":"integer","minimum":0},` +
			`"upstreamUrl":{"type":"string"}},` +
			`"required":["path","sha256","size","upstreamUrl"],"additionalProperties":false}`,
	})
}
