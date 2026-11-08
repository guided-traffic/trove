package audit

// The event of the OCI registry that the revocation journal copies (R-OCI-018, R-OCI-033, ADR-161). A manifest that is deleted may
// have been malicious, and a restore returns the repository to the snapshot, so the deletion is journaled with an entry that names the
// repository, the image name and the digest. The replay of the work package of the deletion WITHHOLDS a restored manifest the journal
// says was deleted after the snapshot (it writes the mark in oci_tombstones and deletes nothing, R-HA-054) and an administrator
// reinstates it (R-OCI-033, Q-OCI-33). The event is transactional: the producer records it in the transaction of the deletion; a live
// deletion writes no mark.

// EventOCIManifestDeleted names the event, for the producers.
const EventOCIManifestDeleted = "oci.manifest.deleted"

func init() {
	// A manifest of an image name was deleted: by a DELETE of the manifest or of its last tag with the cascade of R-OCI-018, by a
	// raw delete, or by a removal without a request. name is the image name, digest the manifest's, deleted the number of items
	// the cascade removed.
	Register(EventType{
		Name: EventOCIManifestDeleted, Category: CategoryContent, Mode: Transactional, Webhook: true,
		Journal: JournalRevocation,
		Schema: `{"type":"object","properties":{` +
			`"name":{"type":"string","maxLength":255},"digest":{"type":"string","pattern":"^sha256:[0-9a-f]{64}$"},` +
			`"deleted":{"type":"integer","minimum":0}},` +
			`"required":["name","digest"],"additionalProperties":false}`,
	})
}
