package audit

// The events of the Debian (APT) format (R-DEB-033). Four of them are journaled (R-HA-054, R-DEB-032, Q-DEB-22, Q-DEB-31, Q-DEB-34):
// the deletion of a package file, the removal of a file's coordinates, and the promotion and the retirement of a signing key. A
// restore returns the repository to its snapshot, and the replay of these entries only restricts: it withholds a restored package,
// removes restored coordinates and withholds a restored key, and never deletes content or erases a private key. The producers record
// each event in the transaction of the change. The fifth journaled change, the removal of fingerprints from deb.upstreamKeys, is a part
// of repository.narrowed (R-REPO-061, Q-REPO-274).
//
// deb.upstream_signature_invalid is a security event about a request that stored nothing, so it goes through the asynchronous buffer
// like the other events of the remote engine, at most once per repository, directory and hour (R-DEB-033).

// Names of the Debian events, for the producers.
const (
	EventDebPackageDeleted         = "deb.package.deleted"
	EventDebCoordinatesChanged     = "deb.coordinates.changed"
	EventDebCoordinatesRemoved     = "deb.coordinates.removed"
	EventDebSigningKeyCreated      = "deb.signing_key.created"
	EventDebSigningKeyPromoted     = "deb.signing_key.promoted"
	EventDebSigningKeyRetired      = "deb.signing_key.retired"
	EventDebSigningKeyReinstated   = "deb.signing_key.reinstated"
	EventDebReinstate              = "deb.reinstate"
	EventDebUpstreamSignatureError = "deb.upstream_signature_invalid"
)

const (
	debSHA256   = `"sha256":{"type":"string","pattern":"^[0-9a-f]{64}$"}`
	debPath     = `"path":{"type":"string","maxLength":4096}`
	debFpr      = `"^[0-9A-F]{40}$"`
	debCoordSet = `{"type":"array","maxItems":1000,"items":{"type":"object","properties":{` +
		`"distribution":{"type":"string","maxLength":64},"component":{"type":"string","maxLength":64}},` +
		`"required":["distribution","component"],"additionalProperties":false}}`
)

func init() {
	// A package file of a local repository was removed: by a DELETE of its pool path, a raw delete or a removal without a request.
	Register(EventType{
		Name: EventDebPackageDeleted, Category: CategoryContent, Mode: Transactional, Webhook: true, Journal: JournalRevocation,
		Schema: `{"type":"object","properties":{` + debPath + `,"package":{"type":"string","maxLength":128},` +
			`"version":{"type":"string","maxLength":128},"architecture":{"type":"string","maxLength":32},` + debSHA256 + `},` +
			`"required":["path","package","version","architecture","sha256"],"additionalProperties":false}`,
	})
	// The coordinates of a file were replaced (R-DEB-009); the removal of any is also recorded as deb.coordinates.removed.
	Register(EventType{
		Name: EventDebCoordinatesChanged, Category: CategoryContent, Mode: Transactional, Webhook: true,
		Schema: `{"type":"object","properties":{` + debPath + `,"added":` + debCoordSet + `,"removed":` + debCoordSet + `},` +
			`"required":["path","added","removed"],"additionalProperties":false}`,
	})
	// Coordinates were removed from a file: pulling a package out of a suite narrows what clients get, so a restore removes them again.
	Register(EventType{
		Name: EventDebCoordinatesRemoved, Category: CategoryContent, Mode: Transactional, Webhook: true, Journal: JournalRevocation,
		Schema: `{"type":"object","properties":{` + debPath + `,` + debSHA256 + `,"removed":` + debCoordSet + `},` +
			`"required":["path","sha256","removed"],"additionalProperties":false}`,
	})
	Register(EventType{
		Name: EventDebSigningKeyCreated, Category: CategoryRepository, Mode: Transactional, Webhook: true,
		Schema: `{"type":"object","properties":{"fingerprint":{"type":"string","pattern":` + debFpr + `},` +
			`"state":{"type":"string","enum":["primary","next"]},"origin":{"type":"string","enum":["generated","imported"]}},` +
			`"required":["fingerprint","state","origin"],"additionalProperties":false}`,
	})
	// The next key became primary and the old primary was retired (R-DEB-017).
	Register(EventType{
		Name: EventDebSigningKeyPromoted, Category: CategoryRepository, Mode: Transactional, Webhook: true, Journal: JournalRevocation,
		Schema: `{"type":"object","properties":{"oldFingerprint":{"type":"string","pattern":` + debFpr + `},` +
			`"newFingerprint":{"type":"string","pattern":` + debFpr + `}},` +
			`"required":["oldFingerprint","newFingerprint"],"additionalProperties":false}`,
	})
	// A next key was removed, or the retirement of a withheld key was confirmed: its private key is erased.
	Register(EventType{
		Name: EventDebSigningKeyRetired, Category: CategoryRepository, Mode: Transactional, Webhook: true, Journal: JournalRevocation,
		Schema: `{"type":"object","properties":{"fingerprint":{"type":"string","pattern":` + debFpr + `}},` +
			`"required":["fingerprint"],"additionalProperties":false}`,
	})
	// A withheld key was returned to the repository as primary or next (R-DEB-032).
	Register(EventType{
		Name: EventDebSigningKeyReinstated, Category: CategoryRepository, Mode: Transactional, Webhook: true,
		Schema: `{"type":"object","properties":{"fingerprint":{"type":"string","pattern":` + debFpr + `},` +
			`"state":{"type":"string","enum":["primary","next"]}},` +
			`"required":["fingerprint","state"],"additionalProperties":false}`,
	})
	// `trove admin deb reinstate`: a package file the replay withheld is served again. Not journaled: it restores access.
	Register(EventType{
		Name: EventDebReinstate, Category: CategoryContent, Mode: Transactional,
		Schema: `{"type":"object","properties":{` + debPath + `,` + debSHA256 + `},` +
			`"required":["path","sha256"],"additionalProperties":false}`,
	})
	// A remote's release document failed the verification against deb.upstreamKeys (R-DEB-020).
	Register(EventType{
		Name: EventDebUpstreamSignatureError, Category: CategoryRepository, Mode: Async,
		Schema: `{"type":"object","properties":{"dir":{"type":"string","maxLength":512},"reason":{"type":"string","maxLength":128}},` +
			`"required":["dir","reason"],"additionalProperties":false}`,
	})
}
