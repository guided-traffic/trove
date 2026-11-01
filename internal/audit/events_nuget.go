package audit

// The package events of the NuGet format (R-NUGET-028, R-OPS-016). The three types are
// shared by the formats that publish packages: the registry names them by what happened to
// a package version, not by a format, and NuGet is the producer today (npm writes its own
// npm.* events).
//
// Every one is transactional: the producer records it in the transaction of the change
// (R-OPS-015). package.published and package.deleted carry the revocation journal flag with
// an optional entry (R-OPS-014, Q-NUGET-10): the NuGet producer supplies the entry of
// R-NUGET-033 for its local repositories, so a restore's replay can reserve the numbers of
// versions that were published after the snapshot and keep the deleted ones deleted. An event
// written without an entry is not journaled and the transaction goes on. package.metadata_changed
// is not journaled: an unlist or a relist reserves and withdraws nothing.

// Names of the package events, for the producers.
const (
	EventPackagePublished       = "package.published"
	EventPackageDeleted         = "package.deleted"
	EventPackageMetadataChanged = "package.metadata_changed"
)

// The values of the change member of package.metadata_changed that NuGet writes (R-NUGET-007).
const (
	ChangeUnlist = "unlist"
	ChangeRelist = "relist"
)

func init() {
	// A package version was accepted. files lists the stored files the push wrote (the
	// .nupkg, with its SHA-256 and size); package is the lower-cased ID and version the URL
	// form of the version (R-NUGET-003).
	Register(EventType{
		Name: EventPackagePublished, Category: CategoryContent, Mode: Transactional, Webhook: true,
		Journal: JournalRevocation, Entry: EntryOptional,
		Schema: `{"type":"object","properties":{` +
			`"package":{"type":"string"},"version":{"type":"string"},` +
			`"files":{"type":"array","maxItems":1000,"items":{"type":"object","properties":{` +
			`"path":{"type":"string"},"sha256":{"type":"string","pattern":"^[0-9a-f]{64}$"},"size":{"type":"integer","minimum":0}},` +
			`"required":["path","sha256","size"],"additionalProperties":false}},` +
			`"claim":{"type":"string"},"shadowCheck":{"type":"string","enum":["undetermined"]}},` +
			`"required":["package","version","files"],"additionalProperties":false}`,
	})
	// A package version was removed: a hard delete, a raw delete of its .nupkg or a move out
	// of the repository. The version number is tombstoned in the same transaction.
	Register(EventType{
		Name: EventPackageDeleted, Category: CategoryContent, Mode: Transactional, Webhook: true,
		Journal: JournalRevocation, Entry: EntryOptional,
		Schema: `{"type":"object","properties":{` +
			`"package":{"type":"string"},"version":{"type":"string"}},` +
			`"required":["package","version"],"additionalProperties":false}`,
	})
	// A version was unlisted or relisted: change is "unlist" or "relist".
	Register(EventType{
		Name: EventPackageMetadataChanged, Category: CategoryContent, Mode: Transactional, Webhook: true,
		Schema: `{"type":"object","properties":{` +
			`"package":{"type":"string"},"version":{"type":"string"},` +
			`"change":{"type":"string","enum":["unlist","relist"]}},` +
			`"required":["package","version","change"],"additionalProperties":false}`,
	})
}
