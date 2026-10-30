package audit

// The events of the npm format (R-NPM-044). Every type the page lists is
// registered here, whichever work package produces it, so that no later package
// edits this file: publish, unpublish, dist-tag changes and deprecation are
// produced by the local repository, the version removal by the remote fill, the
// trusted publishing exchange by the identity exchange of R-NPM-022, and the
// reinstate by `trove admin npm reinstate`.
//
// The state changes are transactional: the producer records them in the
// transaction of the change. npm.unpublish carries the journal flag (R-OPS-014,
// Q-NPM-12): a restore re-applies it, so its journal entry names the repository
// ID, the package, the version, its integrity and the actor's ID and no secret.
// npm.reinstate restores access and is not journaled. A publish that a client
// repeats after its answer was lost (R-NPM-007 step 4) is an npm.publish event
// with outcome success and the reason retry_acknowledged: the retry changes
// nothing else.

// Names of the npm events, for the producers.
const (
	EventNPMPublish          = "npm.publish"
	EventNPMUnpublish        = "npm.unpublish"
	EventNPMReinstate        = "npm.reinstate"
	EventNPMDistTagSet       = "npm.dist_tag.set"
	EventNPMDistTagDelete    = "npm.dist_tag.delete"
	EventNPMDeprecate        = "npm.deprecate"
	EventNPMManifestMismatch = "npm.publish.manifest_mismatch"
	EventNPMTrustedExchange  = "npm.trusted_publishing.exchange"
	EventNPMVersionRemoved   = "npm.remote.version_removed"
)

// ReasonRetryAcknowledged is the reason of an npm.publish event whose request was
// the retry of a publish that had succeeded (R-NPM-007 step 4, Q-NPM-11).
const ReasonRetryAcknowledged = "retry_acknowledged"

func init() {
	// package and version are the normalized name and the canonical version;
	// integrity is the SRI string of the SHA-512. tag is the dist-tag the request
	// carried, latest_guard says that the latest guard kept latest where it was
	// (R-NPM-016), claim is the outcome of the namespace auto claim (R-REPO-023:
	// "created" or the reason nothing was created).
	Register(EventType{
		Name: EventNPMPublish, Category: CategoryContent, Mode: Transactional,
		Schema: `{"type":"object","properties":{` +
			`"package":{"type":"string"},"version":{"type":"string"},"integrity":{"type":"string"},` +
			`"tag":{"type":"string"},"latest_guard":{"type":"boolean"},"claim":{"type":"string"}},` +
			`"required":["package","version","integrity","tag"],"additionalProperties":false}`,
	})
	// One event per removed version. The journal entry is the one a replay applies.
	Register(EventType{
		Name: EventNPMUnpublish, Category: CategoryContent, Mode: Transactional, Journal: JournalRevocation,
		Schema: `{"type":"object","properties":{` +
			`"package":{"type":"string"},"version":{"type":"string"},"integrity":{"type":"string"}},` +
			`"required":["package","version","integrity"],"additionalProperties":false}`,
	})
	// journal_entry is the ID of the journal entry that withheld the version.
	Register(EventType{
		Name: EventNPMReinstate, Category: CategoryContent, Mode: Transactional,
		Schema: `{"type":"object","properties":{` +
			`"package":{"type":"string"},"version":{"type":"string"},"integrity":{"type":"string"},` +
			`"journal_entry":{"type":"string"}},` +
			`"required":["package","version","integrity"],"additionalProperties":false}`,
	})
	// A dist-tag change: the tag, the version it points to after the change and
	// the one it pointed to before ("" for none).
	distTag := `{"type":"object","properties":{` +
		`"package":{"type":"string"},"tag":{"type":"string"},"version":{"type":"string"},"previous":{"type":"string"}},` +
		`"required":["package","tag"],"additionalProperties":false}`
	Register(EventType{Name: EventNPMDistTagSet, Category: CategoryContent, Mode: Transactional, Schema: distTag})
	Register(EventType{Name: EventNPMDistTagDelete, Category: CategoryContent, Mode: Transactional, Schema: distTag})
	// One event per version whose deprecation message changed; message is empty
	// when the deprecation was cleared.
	Register(EventType{
		Name: EventNPMDeprecate, Category: CategoryContent, Mode: Transactional,
		Schema: `{"type":"object","properties":{` +
			`"package":{"type":"string"},"version":{"type":"string"},"message":{"type":"string","maxLength":1024}},` +
			`"required":["package","version"],"additionalProperties":false}`,
	})
	// A request manifest that differs from the package.json of its tarball, in both
	// modes of npm.publish.manifest_consistency. It is written for a publish that
	// is refused as well, which is why it is asynchronous. fields names the
	// compared fields that differ ("dependencies", "scripts.postinstall").
	Register(EventType{
		Name: EventNPMManifestMismatch, Category: CategoryContent, Mode: Async,
		Schema: `{"type":"object","properties":{` +
			`"package":{"type":"string"},"version":{"type":"string"},` +
			`"fields":{"type":"array","maxItems":1000,"items":{"type":"string"}},` +
			`"mode":{"type":"string","enum":["reject","warn"]}},` +
			`"required":["package","version","fields","mode"],"additionalProperties":false}`,
	})
	// The exchange of a CI ID token for a publish token (R-NPM-022, P1); mapping is
	// the identity mapping that matched.
	Register(EventType{
		Name: EventNPMTrustedExchange, Category: CategoryAuth, Mode: Transactional,
		Schema: `{"type":"object","properties":{` +
			`"package":{"type":"string"},"mapping":{"type":"string"},"issuer":{"type":"string"}},` +
			`"required":["package"],"additionalProperties":false}`,
	})
	// A remote repository's document stopped listing a version (R-NPM-026).
	Register(EventType{
		Name: EventNPMVersionRemoved, Category: CategoryRepository, Mode: Transactional,
		Schema: `{"type":"object","properties":{` +
			`"package":{"type":"string"},"version":{"type":"string"},"integrity":{"type":"string"}},` +
			`"required":["package","version"],"additionalProperties":false}`,
	})
}
