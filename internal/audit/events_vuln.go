package audit

// The event types of the vulnerability scanning (R-VULN-024, R-VULN-013, R-VULN-012, R-VULN-015, R-VULN-010, R-VULN-004,
// R-VULN-005). Every type the page lists is registered here, whichever work package produces it, so that no later package
// edits this file. None is a webhook hook in the MVP.
//
// The categories: the status changes and the refusal of a download concern content (the finding's repository is the event's
// repository column), the claimed-name match concerns a repository's claims, and the sync events are system events.
//
// Transactional (written in the transaction of the change): vulnerability.finding.status_changed (the triager's change, or
// each change of a bulk set-status, all with one bulk id), vulnerability.finding.reopened (trove's reopen of a dismissed
// finding) and vulnerability.source.imported (an import of mode repository). Asynchronous: the security events
// vulnerability.download_blocked (at most once a minute per repository, version and client address, R-OPS-015) and
// vulnerability.claimed_name_match (once per advisory and name), and vulnerability.source.failed (at most once an hour per
// source and ecosystem).
//
// The journal (R-OPS-014, Q-VULN-48): the two finding events carry the revocation flag with an optional entry. A change
// that sets a download block removes access to a version, which a restore to a point before it would bring back, so its
// producer supplies the entry; a change that lifts a block, or touches none, widens or keeps access and supplies none. The
// entry is optional because the same type is written for the changes that set no block, and because two packages produce
// the types (the triage the first, the evaluation's reopen the second). The Bastion feed reads both as `content` entries
// when their details say `block: "set"` (internal/bastionmain/feed.go, R-BAS-015).

// Names of the vulnerability events, for the producers.
const (
	EventVulnFindingStatusChanged = "vulnerability.finding.status_changed"
	EventVulnFindingReopened      = "vulnerability.finding.reopened"
	EventVulnDownloadBlocked      = "vulnerability.download_blocked"
	EventVulnClaimedNameMatch     = "vulnerability.claimed_name_match"
	EventVulnSourceFailed         = "vulnerability.source.failed"
	EventVulnSourceImported       = "vulnerability.source.imported"
)

func init() {
	// finding is the finding's ID, package and version the stored name and version, advisory the canonical identifier; from
	// and to are statuses; reason is the dismissal reason; bulk is the identifier of the bulk operation that carried the
	// change; block says that the change set or lifted a download block under the repository's current policy (R-VULN-013),
	// absent when it touched none.
	Register(EventType{
		Name: EventVulnFindingStatusChanged, Category: CategoryContent, Mode: Transactional, Journal: JournalRevocation, Entry: EntryOptional,
		Schema: `{"type":"object","properties":{` +
			`"finding":{"type":"string"},"package":{"type":"string"},"version":{"type":"string"},"advisory":{"type":"string"},` +
			`"from":{"type":"string"},"to":{"type":"string"},"reason":{"type":"string"},"bulk":{"type":"string"},` +
			`"block":{"type":"string","enum":["set","lifted"]}},` +
			`"required":["finding","package","version","advisory","from","to"],"additionalProperties":false}`,
	})
	// change is what moved a dismissed finding to needs_triage (the severity level rose, the kind became malicious, the
	// advisory became known exploited); block is set when the reopened finding is malicious and blocks again (R-VULN-012).
	Register(EventType{
		Name: EventVulnFindingReopened, Category: CategoryContent, Mode: Transactional, Journal: JournalRevocation, Entry: EntryOptional,
		Schema: `{"type":"object","properties":{` +
			`"finding":{"type":"string"},"advisory":{"type":"string"},"change":{"type":["string","object"]},` +
			`"block":{"type":"string","enum":["set"]}},` +
			`"required":["finding","advisory","change"],"additionalProperties":false}`,
	})
	// A download refused, or counted in the mode report (R-VULN-015, R-VULN-016). reason is malicious, triage or
	// missing_data; enforced is false in the mode report; stored says whether the version was in the repository, false for
	// a remote miss refused before the fetch. advisory is absent for missing_data.
	Register(EventType{
		Name: EventVulnDownloadBlocked, Category: CategoryContent, Mode: Async,
		Schema: `{"type":"object","properties":{` +
			`"package":{"type":"string"},"version":{"type":"string"},"advisory":{"type":"string"},` +
			`"reason":{"type":"string","enum":["malicious","triage","missing_data"]},"enforced":{"type":"boolean"},"stored":{"type":"boolean"}},` +
			`"required":["package","version","reason","enforced","stored"],"additionalProperties":false}`,
	})
	// A public advisory names a claimed internal name (R-VULN-010): the sign of a dependency-confusion attempt.
	// claimRepository is the repository whose claim covers the name.
	Register(EventType{
		Name: EventVulnClaimedNameMatch, Category: CategoryRepository, Mode: Async,
		Schema: `{"type":"object","properties":{` +
			`"package":{"type":"string"},"advisory":{"type":"string"},"kind":{"type":"string","enum":["vulnerability","malicious"]},` +
			`"claimRepository":{"type":"string"}},` +
			`"required":["package","advisory","kind","claimRepository"],"additionalProperties":false}`,
	})
	// A sync run failed (R-VULN-004). source is osv or kev, ecosystem is empty for kev, code is the problem code of the
	// run's last_error.
	Register(EventType{
		Name: EventVulnSourceFailed, Category: CategorySystem, Mode: Async,
		Schema: `{"type":"object","properties":{` +
			`"source":{"type":"string"},"ecosystem":{"type":"string"},"code":{"type":"string"}},` +
			`"required":["source","ecosystem","code"],"additionalProperties":false}`,
	})
	// A run of mode repository imported an item (R-VULN-005): sha256 is the digest of the imported item, deployedBy the
	// principal that deployed it (the import's audit event names the deployer, H-309), acceptShrink whether the run
	// overrode the shrink guard.
	Register(EventType{
		Name: EventVulnSourceImported, Category: CategorySystem, Mode: Transactional,
		Schema: `{"type":"object","properties":{` +
			`"source":{"type":"string"},"ecosystem":{"type":"string"},"sha256":{"type":"string"},"deployedBy":{"type":"string"},` +
			`"acceptShrink":{"type":"boolean"}},` +
			`"required":["source","ecosystem","sha256"],"additionalProperties":false}`,
	})
}
